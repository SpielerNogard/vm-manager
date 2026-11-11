//go:build darwin

package vz

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Code-Hex/vz/v3"

	"vm-manager/internal/netframe"
	"vm-manager/internal/node"
	"vm-manager/internal/proto"
)

const (
	netagentPort = 6000
	netvmDiskGB  = 10
)

// StartChassis boots the netvm on the first call and (re)sends the chassis config on every call.
func (p *Provider) StartChassis(ctx context.Context, c node.Chassis) error {
	p.netMu.Lock()
	booted := p.netStop != nil
	p.netMu.Unlock()
	if !booted {
		if err := p.bootNetvm(ctx); err != nil {
			return fmt.Errorf("netvm: %w", err)
		}
	}
	payload, err := json.Marshal(netframe.Configure(c))
	if err != nil {
		return err
	}
	for {
		err := p.sendConfigure(ctx, payload)
		if err == nil {
			log.Printf("netvm configured (chassis %s, %s)", c.Name, c.Address)
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("netvm configure: %w (last error: %v)", ctx.Err(), err)
		case <-time.After(relayRetry):
		}
	}
}

// sendConfigure waits for netagent to close the connection, which it does once the config is applied.
func (p *Provider) sendConfigure(ctx context.Context, payload []byte) error {
	conn, err := p.dialNetvm()
	if err != nil {
		return err
	}
	defer conn.Close()
	defer context.AfterFunc(ctx, func() { conn.Close() })()
	if err := netframe.Write(conn, netframe.ConfigureFrame, 0, payload); err != nil {
		return err
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
	_, err = io.Copy(io.Discard, conn)
	return err
}

func (p *Provider) dialNetvm() (net.Conn, error) {
	p.netMu.Lock()
	inst := p.netvm
	p.netMu.Unlock()
	if inst == nil {
		return nil, errors.New("netvm not running")
	}
	c, err := inst.vm.SocketDevices()[0].Connect(netagentPort)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// StopChassis shuts the netvm down; the agent calls it only after all guests stopped.
func (p *Provider) StopChassis() error {
	p.netMu.Lock()
	if p.netStop == nil {
		p.netMu.Unlock()
		return nil
	}
	select {
	case <-p.netStop:
	default:
		close(p.netStop)
	}
	inst := p.netvm
	p.netMu.Unlock()
	if inst == nil {
		return nil
	}
	return inst.Stop()
}

func (p *Provider) bootNetvm(ctx context.Context) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	agent := filepath.Join(filepath.Dir(exe), "netagent-linux-arm64")
	if _, err := os.Stat(agent); err != nil {
		return fmt.Errorf("netagent binary %s: %w", agent, err)
	}
	dir := filepath.Join(p.VMsDir, "_netvm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mac, err := netvmMAC(filepath.Join(dir, "mac"))
	if err != nil {
		return err
	}
	if err := p.ensureNetvmDisk(dir); err != nil {
		return err
	}
	if err := makeSeedISO(ctx, dir, func(src string) error { return writeNetvmSeed(src, agent, mac) }); err != nil {
		return err
	}
	f := node.VMFile{Spec: proto.VMSpec{ID: "_netvm", Name: "netvm", CPUs: 1, MemoryMB: 1024, DiskGB: netvmDiskGB}, MAC: mac}
	inst, err := p.startNetvmVM(f, dir)
	if err != nil {
		return err
	}
	stop := make(chan struct{})
	p.netMu.Lock()
	p.netvm, p.netStop = inst, stop
	p.netMu.Unlock()
	go p.superviseNetvm(f, dir, inst, stop)
	return nil
}

func (p *Provider) startNetvmVM(f node.VMFile, dir string) (*instance, error) {
	cfg, err := p.vmConfig(f, dir, nil, true)
	if err != nil {
		return nil, err
	}
	vm, err := vz.NewVirtualMachine(cfg)
	if err != nil {
		return nil, err
	}
	if err := vm.Start(); err != nil {
		return nil, err
	}
	inst := &instance{vm: vm, done: make(chan struct{})}
	go inst.watch()
	return inst, nil
}

// superviseNetvm reboots the netvm whenever it stops, until StopChassis closes stop.
func (p *Provider) superviseNetvm(f node.VMFile, dir string, inst *instance, stop chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-inst.Done():
		}
		select {
		case <-stop:
			return
		default:
		}
		log.Print("netvm stopped, restarting")
		for {
			select {
			case <-stop:
				return
			case <-time.After(relayRetry):
			}
			next, err := p.startNetvmVM(f, dir)
			if err != nil {
				log.Printf("netvm restart: %v", err)
				continue
			}
			p.netMu.Lock()
			select {
			case <-stop:
				p.netMu.Unlock()
				next.Stop()
				return
			default:
			}
			p.netvm = next
			p.netMu.Unlock()
			inst = next
			break
		}
	}
}

func (p *Provider) ensureNetvmDisk(dir string) error {
	root := filepath.Join(dir, "root.img")
	if _, err := os.Stat(root); err == nil {
		return nil
	}
	p.imageMu.Lock()
	base, err := node.EnsureBaseImage(p.ImagesDir, p.ImageURL)
	p.imageMu.Unlock()
	if err != nil {
		return fmt.Errorf("base image: %w", err)
	}
	// Clone to a temp name so an interrupted clone is not mistaken for a finished disk.
	tmp := root + ".tmp"
	os.Remove(tmp)
	if err := node.CloneResize(base, tmp, netvmDiskGB); err != nil {
		return err
	}
	return os.Rename(tmp, root)
}

// netvmMAC keeps the netvm's NAT MAC stable across boots so it keeps its DHCP lease.
func netvmMAC(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b)), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[0] = b[0]&^0x01 | 0x02 // unicast, locally administered
	mac := net.HardwareAddr(b).String()
	return mac, os.WriteFile(path, []byte(mac+"\n"), 0o644)
}
