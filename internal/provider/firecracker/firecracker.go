//go:build linux

package firecracker

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vm-manager/internal/chassisconf"
	"vm-manager/internal/netframe"
	"vm-manager/internal/node"
	"vm-manager/internal/proto"
	"vm-manager/internal/provider/linuxvm"
)

const DefaultImageURL = linuxvm.DefaultImageURL

type Provider struct {
	VMsDir, ImagesDir, ImageURL string

	mu  sync.Mutex // serializes base image downloads and NAT address allocation
	nbd linuxvm.NBDWatch
}

func New(c node.Config) (*Provider, error) {
	var s struct {
		ImageURL string `json:"imageURL"`
	}
	if err := c.ProviderSection(&s); err != nil {
		return nil, fmt.Errorf("config firecracker section: %w", err)
	}
	if err := linuxvm.CheckHost("firecracker"); err != nil {
		return nil, err
	}
	p := &Provider{VMsDir: c.Paths.VMs, ImagesDir: c.Paths.Images, ImageURL: s.ImageURL}
	if p.ImageURL == "" {
		p.ImageURL = DefaultImageURL
	}
	return p, nil
}

func (p *Provider) Name() string { return "firecracker" }

func (p *Provider) Prepare(ctx context.Context, f node.VMFile, dir string) error {
	p.mu.Lock()
	base, err := node.EnsureBaseImage(p.ImagesDir, p.ImageURL)
	p.mu.Unlock()
	if err != nil {
		return fmt.Errorf("base image: %w", err)
	}
	if err := node.CloneResize(base, filepath.Join(dir, "root.img"), f.Spec.DiskGB); err != nil {
		return err
	}
	return p.Reseed(ctx, f, dir)
}

func (p *Provider) Reseed(_ context.Context, f node.VMFile, dir string) error {
	p.mu.Lock()
	ip, err := linuxvm.AllocNATIP(p.VMsDir, f.Spec.ID)
	p.mu.Unlock()
	if err != nil {
		return err
	}
	return node.WriteSeedISO(dir, node.GuestSeedFiles(f, node.PrimaryNet{Address: ip + "/24", Gateway: linuxvm.NATGateway}))
}

func (p *Provider) Disks(f node.VMFile, dir string) ([]proto.Disk, error) {
	return node.ListDisks(f, dir, func(diskID string) string { return p.nbd.Err(f.Spec.ID, diskID) })
}

func (p *Provider) Start(f node.VMFile, dir string) (node.Instance, error) {
	if err := p.Reseed(context.Background(), f, dir); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	kernel, initrd, err := bootFiles(filepath.Join(dir, "root.img"), dir)
	if err != nil {
		return nil, fmt.Errorf("boot files: %w", err)
	}
	drives, nbds, err := linuxvm.AttachDisks(f, dir)
	if err != nil {
		return nil, err
	}
	cfg, err := vmConfig(f, kernel, initrd, drives)
	if err != nil {
		linuxvm.DetachAll(f.Spec.ID, dir, nbds)
		return nil, err
	}
	cfgPath, sock := filepath.Join(dir, "fc.json"), filepath.Join(dir, "fc.sock")
	if err := os.WriteFile(cfgPath, cfg, 0o644); err != nil {
		linuxvm.DetachAll(f.Spec.ID, dir, nbds)
		return nil, err
	}
	os.Remove(sock)
	proc, err := linuxvm.Spawn(f, dir, exec.Command("firecracker", "--api-sock", sock, "--config-file", cfgPath), nbds)
	if err != nil {
		linuxvm.DetachAll(f.Spec.ID, dir, nbds)
		return nil, err
	}
	// Firecracker has no ACPI power-off and would idle forever after a halt.
	go proc.KillOnConsole("System halted")
	for _, id := range nbds {
		go p.nbd.Watch(f.Spec.ID, dir, id, proc.Done())
	}
	return &instance{Process: proc, sock: sock}, nil
}

func (p *Provider) StartChassis(_ context.Context, c node.Chassis) error {
	_, err := chassisconf.Apply(netframe.Configure(c), "/", chassisconf.Run)
	return err
}

// StopChassis leaves OVS/OVN running: they belong to the host, not to this node process.
func (p *Provider) StopChassis() error { return nil }

type instance struct {
	*linuxvm.Process
	sock string
}

// Stop asks the guest to shut down (Ctrl-Alt-Del reboots it, and reboot=k ends Firecracker) and kills it after 10s.
func (i *instance) Stop() error {
	if err := i.sendCtrlAltDel(); err == nil {
		select {
		case <-i.Done():
			return nil
		case <-time.After(10 * time.Second):
		}
	}
	return i.Kill()
}

func (i *instance) sendCtrlAltDel() error {
	req, err := http.NewRequest(http.MethodPut, "http://localhost/actions", strings.NewReader(`{"action_type":"SendCtrlAltDel"}`))
	if err != nil {
		return err
	}
	resp, err := linuxvm.DialSocket(i.sock).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("SendCtrlAltDel: %s", resp.Status)
	}
	return nil
}
