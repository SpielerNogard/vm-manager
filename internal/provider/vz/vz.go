//go:build darwin

package vz

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Code-Hex/vz/v3"

	"vm-manager/internal/node"
	"vm-manager/internal/proto"
)

const DefaultImageURL = "https://cloud-images.ubuntu.com/releases/noble/release/ubuntu-24.04-server-cloudimg-arm64.img"

type Provider struct {
	VMsDir, ImagesDir, ImageURL, LeasesPath string

	imageMu  sync.Mutex // concurrent creates must not download the same base image twice
	nextPort atomic.Uint32

	netMu   sync.Mutex
	netvm   *instance
	netStop chan struct{} // closed by StopChassis; nil until the netvm was first booted
	netDone chan struct{} // closed when the supervisor exits
	netMAC  string
}

func New(c node.Config) (*Provider, error) {
	var s struct {
		ImageURL   string `json:"imageURL"`
		LeasesPath string `json:"leasesPath"`
	}
	if err := c.ProviderSection(&s); err != nil {
		return nil, fmt.Errorf("config vz section: %w", err)
	}
	p := &Provider{VMsDir: c.Paths.VMs, ImagesDir: c.Paths.Images, ImageURL: s.ImageURL, LeasesPath: s.LeasesPath}
	if p.ImageURL == "" {
		p.ImageURL = DefaultImageURL
	}
	if p.LeasesPath == "" {
		p.LeasesPath = DefaultLeasesPath
	}
	return p, nil
}

func (p *Provider) Name() string { return "vz" }

func (p *Provider) Prepare(ctx context.Context, f node.VMFile, dir string) error {
	p.imageMu.Lock()
	base, err := node.EnsureBaseImage(p.ImagesDir, p.ImageURL)
	p.imageMu.Unlock()
	if err != nil {
		return fmt.Errorf("base image: %w", err)
	}
	if err := node.CloneResize(base, filepath.Join(dir, "root.img"), f.Spec.DiskGB); err != nil {
		return err
	}
	if err := node.CreateDataDisks(dir, f.Spec.DataDisks); err != nil {
		return err
	}
	return makeSeedISO(ctx, dir, writeGuestSeed(f))
}

func (p *Provider) Start(f node.VMFile, dir string) (node.Instance, error) {
	var nics []guestNIC
	closeNICs := func() {
		for _, n := range nics {
			n.guestEnd.Close()
			n.vmEnd.Close()
		}
	}
	started := false
	defer func() {
		if !started {
			closeNICs()
		}
	}()
	var nicCfgs []*vz.VirtioNetworkDeviceConfiguration
	for _, n := range f.Spec.NICs {
		gn, err := newGuestNIC(n)
		if err != nil {
			return nil, fmt.Errorf("nic %s: %w", n.Port, err)
		}
		nics = append(nics, gn)
		nicCfgs = append(nicCfgs, gn.cfg)
	}
	cfg, err := p.vmConfig(f, dir, nicCfgs, false)
	if err != nil {
		return nil, err
	}
	vm, err := vz.NewVirtualMachine(cfg)
	if err != nil {
		return nil, err
	}
	_, prevLease, _, err := LookupLease(p.LeasesPath, f.MAC)
	if err != nil {
		return nil, err
	}
	if err := vm.Start(); err != nil {
		return nil, err
	}
	started = true
	inst := &instance{vm: vm, mac: f.MAC, leasesPath: p.LeasesPath, prevLease: prevLease, done: make(chan struct{})}
	go inst.watch()

	ctx, cancel := context.WithCancel(context.Background())
	var relays sync.WaitGroup
	for _, n := range nics {
		relays.Add(1)
		// ponytail: provider-wide counter wraps after 65535 attached NICs; reuse freed IDs if a node ever gets there.
		port := uint16(p.nextPort.Add(1))
		go func() {
			defer relays.Done()
			relayNIC(ctx, n.vmEnd, p.dialNetvm, port, n.port)
		}()
	}
	go func() {
		<-inst.done
		cancel()
		relays.Wait()
		closeNICs()
	}()
	return inst, nil
}

type guestNIC struct {
	guestEnd, vmEnd *os.File // guestEnd must stay open while vz uses its fd
	port            string
	cfg             *vz.VirtioNetworkDeviceConfiguration
}

func newGuestNIC(n proto.NIC) (guestNIC, error) {
	hw, err := net.ParseMAC(n.MAC)
	if err != nil {
		return guestNIC{}, err
	}
	mac, err := vz.NewMACAddress(hw)
	if err != nil {
		return guestNIC{}, err
	}
	guestEnd, vmEnd, err := newNICSocketpair()
	if err != nil {
		return guestNIC{}, err
	}
	// MTU stays at vz's default 1500: it rejects smaller values on file-handle attachments; the guest MTU comes from DHCP.
	att, err := vz.NewFileHandleNetworkDeviceAttachment(guestEnd)
	if err == nil {
		var cfg *vz.VirtioNetworkDeviceConfiguration
		if cfg, err = vz.NewVirtioNetworkDeviceConfiguration(att); err == nil {
			cfg.SetMACAddress(mac)
			return guestNIC{guestEnd: guestEnd, vmEnd: vmEnd, port: n.Port, cfg: cfg}, nil
		}
	}
	guestEnd.Close()
	vmEnd.Close()
	return guestNIC{}, err
}

func (p *Provider) vmConfig(f node.VMFile, dir string, extraNICs []*vz.VirtioNetworkDeviceConfiguration, vsock bool) (*vz.VirtualMachineConfiguration, error) {
	varsPath := filepath.Join(dir, "efi.vars")
	var opts []vz.NewEFIVariableStoreOption
	if _, err := os.Stat(varsPath); errors.Is(err, os.ErrNotExist) {
		opts = append(opts, vz.WithCreatingEFIVariableStore())
	}
	store, err := vz.NewEFIVariableStore(varsPath, opts...)
	if err != nil {
		return nil, fmt.Errorf("efi vars: %w", err)
	}
	boot, err := vz.NewEFIBootLoader(vz.WithEFIVariableStore(store))
	if err != nil {
		return nil, err
	}
	cfg, err := vz.NewVirtualMachineConfiguration(boot, uint(f.Spec.CPUs), uint64(f.Spec.MemoryMB)<<20)
	if err != nil {
		return nil, err
	}
	platform, err := vz.NewGenericPlatformConfiguration()
	if err != nil {
		return nil, err
	}
	cfg.SetPlatformVirtualMachineConfiguration(platform)

	disks := []string{"root.img"}
	for i := range f.Spec.DataDisks {
		disks = append(disks, node.DataDiskName(i))
	}
	var storage []vz.StorageDeviceConfiguration
	for _, d := range append(disks, seedISO) {
		// The default caching mode corrupts ext4 in Linux guests (seen as inode checksum errors); Lima uses the same setting.
		att, err := vz.NewDiskImageStorageDeviceAttachmentWithCacheAndSync(filepath.Join(dir, d), d == seedISO, vz.DiskImageCachingModeCached, vz.DiskImageSynchronizationModeFsync)
		if err != nil {
			return nil, fmt.Errorf("disk %s: %w", d, err)
		}
		blk, err := vz.NewVirtioBlockDeviceConfiguration(att)
		if err != nil {
			return nil, err
		}
		storage = append(storage, blk)
	}
	cfg.SetStorageDevicesVirtualMachineConfiguration(storage)

	hw, err := net.ParseMAC(f.MAC)
	if err != nil {
		return nil, err
	}
	mac, err := vz.NewMACAddress(hw)
	if err != nil {
		return nil, err
	}
	nat, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return nil, err
	}
	nic, err := vz.NewVirtioNetworkDeviceConfiguration(nat)
	if err != nil {
		return nil, err
	}
	nic.SetMACAddress(mac)
	cfg.SetNetworkDevicesVirtualMachineConfiguration(append([]*vz.VirtioNetworkDeviceConfiguration{nic}, extraNICs...))

	if vsock {
		dev, err := vz.NewVirtioSocketDeviceConfiguration()
		if err != nil {
			return nil, err
		}
		cfg.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{dev})
	}

	serialAtt, err := vz.NewFileSerialPortAttachment(filepath.Join(dir, "console.log"), true)
	if err != nil {
		return nil, err
	}
	serial, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAtt)
	if err != nil {
		return nil, err
	}
	cfg.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{serial})

	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, err
	}
	cfg.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	if ok, err := cfg.Validate(); !ok || err != nil {
		return nil, fmt.Errorf("invalid vm config: %v", err)
	}
	return cfg, nil
}

type instance struct {
	vm         *vz.VirtualMachine
	mac        string
	leasesPath string
	prevLease  uint64
	done       chan struct{}
}

func (i *instance) watch() {
	for st := range i.vm.StateChangedNotify() {
		if st == vz.VirtualMachineStateStopped || st == vz.VirtualMachineStateError {
			close(i.done)
			return
		}
	}
}

func (i *instance) Done() <-chan struct{} { return i.done }

func (i *instance) WaitIP(ctx context.Context) (string, error) {
	return waitForLease(ctx, i.leasesPath, i.mac, i.prevLease, 2*time.Second)
}

// Stop asks the guest to shut down and forces it off after 10s.
func (i *instance) Stop() error {
	if i.vm.CanRequestStop() {
		if _, err := i.vm.RequestStop(); err == nil {
			select {
			case <-i.done:
				return nil
			case <-time.After(10 * time.Second):
			}
		}
	}
	if i.vm.CanStop() {
		return i.vm.Stop()
	}
	return nil
}
