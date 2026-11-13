//go:build linux

package cloudhypervisor

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"vm-manager/internal/chassisconf"
	"vm-manager/internal/netframe"
	"vm-manager/internal/node"
	"vm-manager/internal/proto"
	"vm-manager/internal/provider/linuxvm"
)

type Provider struct {
	VMsDir, ImagesDir, ImageURL, Firmware string

	mu  sync.Mutex // serializes base image downloads and NAT address allocation
	nbd linuxvm.NBDWatch
}

func New(c node.Config) (*Provider, error) {
	s, err := parseSettings(c)
	if err != nil {
		return nil, err
	}
	if err := checkFirmware(s.Firmware); err != nil {
		return nil, err
	}
	if err := linuxvm.CheckHost("cloud-hypervisor", "qemu-nbd"); err != nil {
		return nil, err
	}
	return &Provider{VMsDir: c.Paths.VMs, ImagesDir: c.Paths.Images, ImageURL: s.ImageURL, Firmware: s.Firmware}, nil
}

func (p *Provider) Name() string { return "cloud-hypervisor" }

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

func (p *Provider) StartChassis(_ context.Context, c node.Chassis) error {
	_, err := chassisconf.Apply(netframe.Configure(c), "/", chassisconf.Run)
	return err
}

// StopChassis leaves OVS/OVN running: they belong to the host, not to this node process.
func (p *Provider) StopChassis() error { return nil }
