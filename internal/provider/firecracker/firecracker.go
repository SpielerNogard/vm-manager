//go:build linux

package firecracker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"vm-manager/internal/chassisconf"
	"vm-manager/internal/netframe"
	"vm-manager/internal/node"
	"vm-manager/internal/proto"
)

const DefaultImageURL = "https://cloud-images.ubuntu.com/releases/noble/release/ubuntu-24.04-server-cloudimg-amd64.img"

type Provider struct {
	VMsDir, ImagesDir, ImageURL string

	mu sync.Mutex // serializes base image downloads and NAT address allocation
}

func New(c node.Config) (*Provider, error) {
	var s struct {
		ImageURL string `json:"imageURL"`
	}
	if err := c.ProviderSection(&s); err != nil {
		return nil, fmt.Errorf("config firecracker section: %w", err)
	}
	if err := checkHost(); err != nil {
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
	ip, err := allocNATIP(p.VMsDir, f.Spec.ID)
	p.mu.Unlock()
	if err != nil {
		return err
	}
	return node.WriteSeedISO(dir, node.GuestSeedFiles(f, node.PrimaryNet{Address: ip + "/24", Gateway: natGateway}))
}

func (p *Provider) Disks(f node.VMFile, dir string) ([]proto.Disk, error) {
	return node.ListDisks(f, dir, func(string) string { return "" })
}

func (p *Provider) Start(f node.VMFile, dir string) (node.Instance, error) {
	if err := p.Reseed(context.Background(), f, dir); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	kernel, initrd, err := extractBoot(filepath.Join(dir, "root.img"), dir)
	if err != nil {
		return nil, fmt.Errorf("boot files: %w", err)
	}
	var drives []string
	for _, d := range node.AttachOrder(f) {
		if d.NBD {
			return nil, errors.New("NBD disks are not supported on firecracker yet")
		}
		drives = append(drives, filepath.Join(dir, d.File))
	}
	cfg, err := vmConfig(f, kernel, initrd, drives)
	if err != nil {
		return nil, err
	}
	cfgPath, sock := filepath.Join(dir, "fc.json"), filepath.Join(dir, "fc.sock")
	if err := os.WriteFile(cfgPath, cfg, 0o644); err != nil {
		return nil, err
	}
	os.Remove(sock)
	console := filepath.Join(dir, node.ConsoleLog)
	offset, err := node.ConsoleOffset(console)
	if err != nil {
		return nil, err
	}
	out, err := os.OpenFile(console, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if err := setupTaps(f); err != nil {
		out.Close()
		removeTaps(f)
		return nil, fmt.Errorf("taps: %w", err)
	}

	cmd := exec.Command("firecracker", "--api-sock", sock, "--config-file", cfgPath)
	cmd.Stdout, cmd.Stderr = out, out
	// The node's death must take its VMs along, as with vz; see the LockOSThread below.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	inst := &instance{cmd: cmd, sock: sock, console: console, offset: offset, ip: node.MgmtIP(f), done: make(chan struct{})}
	started := make(chan error, 1)
	go func() {
		// Pdeathsig fires when the starting thread exits, so this goroutine keeps its thread until the VM is gone.
		runtime.LockOSThread()
		err := cmd.Start()
		started <- err
		if err == nil {
			cmd.Wait()
		}
		out.Close()
		removeTaps(f)
		close(inst.done)
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	go inst.killOnHalt()
	return inst, nil
}

func (p *Provider) StartChassis(_ context.Context, c node.Chassis) error {
	_, err := chassisconf.Apply(netframe.Configure(c), "/", chassisconf.Run)
	return err
}

// StopChassis leaves OVS/OVN running: they belong to the host, not to this node process.
func (p *Provider) StopChassis() error { return nil }

type instance struct {
	cmd     *exec.Cmd
	sock    string
	console string
	offset  int64
	ip      string
	done    chan struct{}
}

func (i *instance) Done() <-chan struct{} { return i.done }

func (i *instance) WaitIP(ctx context.Context) (string, error) {
	if err := node.WaitReady(ctx, i.console, i.offset, time.Second); err != nil {
		return "", err
	}
	return i.ip, nil
}

// Stop asks the guest to shut down (Ctrl-Alt-Del reboots it, and reboot=k ends Firecracker) and kills it after 10s.
func (i *instance) Stop() error {
	if err := i.sendCtrlAltDel(); err == nil {
		select {
		case <-i.done:
			return nil
		case <-time.After(10 * time.Second):
		}
	}
	return i.Kill()
}

func (i *instance) Kill() error {
	select {
	case <-i.done:
		return nil
	default:
	}
	if err := i.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	<-i.done
	return nil
}

// killOnHalt ends the process when the guest halts: Firecracker has no ACPI power-off and would idle forever.
func (i *instance) killOnHalt() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-i.done
		cancel()
	}()
	if node.WaitConsole(ctx, i.console, i.offset, time.Second, "System halted") == nil {
		i.cmd.Process.Kill()
	}
}

func (i *instance) sendCtrlAltDel() error {
	c := http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", i.sock)
		},
	}}
	req, err := http.NewRequest(http.MethodPut, "http://localhost/actions", strings.NewReader(`{"action_type":"SendCtrlAltDel"}`))
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("SendCtrlAltDel: %s", resp.Status)
	}
	return nil
}
