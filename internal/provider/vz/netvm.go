//go:build darwin

package vz

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
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
	endpoint, err := netvmEndpoint(c.Endpoint, func() (string, error) {
		return waitForLease(ctx, p.LeasesPath, p.netMAC, 0, relayRetry)
	})
	if err != nil {
		return fmt.Errorf("netvm endpoint: %w", err)
	}
	c.Endpoint = endpoint
	payload, err := json.Marshal(netframe.Configure(c))
	if err != nil {
		return err
	}
	logged := false
	for {
		err := p.sendConfigure(ctx, payload)
		if err == nil {
			log.Printf("netvm configured (chassis %s, %s, endpoint %s)", c.Name, c.Address, c.Endpoint)
			return nil
		}
		var applyErr configureError
		if errors.As(err, &applyErr) && !logged {
			log.Printf("netvm configure: %v, retrying", err)
			logged = true
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("netvm configure: %w (last error: %v)", ctx.Err(), err)
		case <-time.After(relayRetry):
		}
	}
}

func (p *Provider) sendConfigure(ctx context.Context, payload []byte) error {
	conn, err := p.dialNetvm()
	if err != nil {
		return err
	}
	return configureOver(ctx, conn, payload, 2*time.Minute)
}

// configureError is netagent's reply to a Configure it could not apply.
type configureError string

func (e configureError) Error() string { return "netagent: " + string(e) }

// configureOver sends one Configure on conn and succeeds only on an ok ConfigureResult.
func configureOver(ctx context.Context, conn net.Conn, payload []byte, timeout time.Duration) error {
	defer conn.Close()
	defer context.AfterFunc(ctx, func() { conn.Close() })()
	conn.SetDeadline(time.Now().Add(timeout))
	if err := netframe.Write(conn, netframe.ConfigureFrame, 0, payload); err != nil {
		return err
	}
	typ, _, reply, err := netframe.Read(conn, nil)
	if err != nil {
		return fmt.Errorf("waiting for configure result: %w", err)
	}
	if typ != netframe.ConfigureResult {
		return fmt.Errorf("unexpected frame type %d in reply to configure", typ)
	}
	if len(reply) > 0 {
		return configureError(reply)
	}
	return nil
}

// netvmEndpoint points a loopback endpoint at the vmnet NAT gateway, which is how the netvm reaches this Mac.
func netvmEndpoint(endpoint string, leaseIP func() (string, error)) (string, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return endpoint, nil
	}
	lease, err := leaseIP()
	if err != nil {
		return "", err
	}
	gw := net.ParseIP(lease).To4()
	if gw == nil {
		return "", fmt.Errorf("netvm lease %q is not IPv4", lease)
	}
	// vmnet's NAT gateway is .1 of the guest's /24.
	gw[3] = 1
	return net.JoinHostPort(gw.String(), port), nil
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
	inst, done := p.netvm, p.netDone
	p.netMu.Unlock()
	err := inst.Stop()
	// A restart in flight is stopped by the supervisor; the node must not exit before that.
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		return errors.New("netvm supervisor did not exit within 30s")
	}
	return err
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
	stop, done := make(chan struct{}), make(chan struct{})
	p.netMu.Lock()
	p.netvm, p.netStop, p.netDone, p.netMAC = inst, stop, done, mac
	p.netMu.Unlock()
	go func() {
		defer close(done)
		p.superviseNetvm(f, dir, inst, stop)
	}()
	return nil
}

func (p *Provider) startNetvmVM(f node.VMFile, dir string) (*instance, error) {
	cfg, _, err := p.vmConfig(f, dir, nil, true)
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
