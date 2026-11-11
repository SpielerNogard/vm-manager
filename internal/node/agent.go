package node

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"vm-manager/internal/proto"
)

const (
	reconnectDelay = 2 * time.Second
	ipTimeout      = 120 * time.Second

	defaultUnderlayMTU = 1420
)

type Agent struct {
	HubURL, Token, NodeID, VMsDir string
	CPUs, MemoryMB                int
	Provider                      Provider
	Config                        json.RawMessage
	Machine                       *proto.Machine
	ChassisKey                    string // public, sent in Register
	ChassisPrivateKey             string
	UnderlayEndpoint              string // host:port override of the hub's underlay address
	UnderlayMTU                   int

	chassisMu       sync.Mutex
	chassisPending  *Chassis // newest config not yet applied; older ones are superseded
	chassisRunning  bool
	chassisStopping bool
	chassisWG       sync.WaitGroup

	mu   sync.Mutex
	vms  map[string]*vmEntry
	send func(proto.Message) error // nil while disconnected
}

type vmEntry struct {
	file   VMFile
	status proto.VMStatus
	inst   Instance
	cancel context.CancelFunc
	booted chan struct{} // closed once boot has finished starting (or failed)
}

func (a *Agent) Run(ctx context.Context) error {
	a.vms = map[string]*vmEntry{}
	if err := a.restoreAll(ctx); err != nil {
		return err
	}
	for {
		if err := a.connect(ctx); err != nil && ctx.Err() == nil {
			log.Printf("hub connection: %v", err)
		}
		select {
		case <-ctx.Done():
			a.stopAll()
			return ctx.Err()
		case <-time.After(reconnectDelay):
		}
	}
}

func (a *Agent) restoreAll(ctx context.Context) error {
	files, err := LoadVMs(a.VMsDir)
	if err != nil {
		return err
	}
	for _, f := range files {
		a.startVM(ctx, f, false)
	}
	return nil
}

func (a *Agent) connect(ctx context.Context) error {
	c, _, err := websocket.Dial(ctx, a.HubURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + a.Token}},
	})
	if err != nil {
		return err
	}
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	sess, err := yamux.Client(websocket.NetConn(ctx, c, websocket.MessageBinary), cfg)
	if err != nil {
		return err
	}
	defer sess.Close()
	ctrl, err := sess.Open()
	if err != nil {
		return err
	}

	var encMu sync.Mutex
	enc := proto.NewEncoder(ctrl)
	send := func(m proto.Message) error {
		encMu.Lock()
		defer encMu.Unlock()
		return enc.Encode(m)
	}
	// Register and installing send happen under a.mu so no status update slips between them.
	a.mu.Lock()
	err = send(proto.Message{Type: "register", Register: a.registerMsg()})
	if err == nil {
		a.send = send
	}
	a.mu.Unlock()
	if err != nil {
		return err
	}
	log.Printf("connected to hub %s", a.HubURL)
	defer func() {
		a.mu.Lock()
		a.send = nil
		a.mu.Unlock()
	}()

	dec := json.NewDecoder(ctrl)
	for {
		var m proto.Message
		if err := dec.Decode(&m); err != nil {
			return err
		}
		switch {
		case m.Type == "create" && m.Create != nil:
			a.create(ctx, *m.Create)
		case m.Type == "delete":
			go a.delete(m.DeleteID)
		case m.Type == "underlay" && m.Underlay != nil:
			go a.startChassis(ctx, *m.Underlay)
		}
	}
}

func (a *Agent) startChassis(ctx context.Context, u proto.Underlay) {
	endpoint := a.UnderlayEndpoint
	if endpoint == "" {
		hu, err := url.Parse(a.HubURL)
		if err != nil {
			log.Printf("chassis: hub url: %v", err)
			return
		}
		endpoint = net.JoinHostPort(hu.Hostname(), strconv.Itoa(u.Port))
	}
	mtu := a.UnderlayMTU
	if mtu == 0 {
		mtu = defaultUnderlayMTU
	}
	c := Chassis{
		Name: a.NodeID, PrivateKey: a.ChassisPrivateKey, Address: u.Address, HubKey: u.HubKey,
		Endpoint: endpoint, SBRemote: u.SBRemote, MTU: mtu,
	}
	a.chassisMu.Lock()
	if a.chassisStopping {
		a.chassisMu.Unlock()
		return
	}
	a.chassisPending = &c
	if a.chassisRunning {
		a.chassisMu.Unlock()
		return
	}
	a.chassisRunning = true
	a.chassisWG.Add(1)
	defer a.chassisWG.Done()
	for a.chassisPending != nil {
		next := *a.chassisPending
		a.chassisPending = nil
		a.chassisMu.Unlock()
		if err := a.Provider.StartChassis(ctx, next); err != nil {
			log.Printf("chassis: %v", err)
		}
		a.chassisMu.Lock()
	}
	a.chassisRunning = false
	a.chassisMu.Unlock()
}

func (a *Agent) registerMsg() *proto.Register {
	reg := &proto.Register{NodeID: a.NodeID, ChassisKey: a.ChassisKey, Provider: a.Provider.Name(), CPUs: a.CPUs, MemoryMB: a.MemoryMB, Config: a.Config, Machine: a.Machine}
	reg.Hostname, _ = os.Hostname()
	for _, e := range a.vms {
		reg.VMs = append(reg.VMs, proto.VMRecord{Spec: e.file.Spec, Status: e.status})
	}
	return reg
}

func (a *Agent) create(ctx context.Context, spec proto.VMSpec) {
	a.mu.Lock()
	_, exists := a.vms[spec.ID]
	a.mu.Unlock()
	if exists {
		return
	}
	f := VMFile{Spec: spec, MAC: randomMAC()}
	if err := SaveVM(a.VMsDir, f); err != nil {
		a.report(proto.VMStatus{ID: spec.ID, State: proto.StateError, Error: err.Error()})
		return
	}
	a.startVM(ctx, f, true)
}

func (a *Agent) startVM(ctx context.Context, f VMFile, prepare bool) {
	bootCtx, cancel := context.WithCancel(ctx)
	e := &vmEntry{file: f, status: proto.VMStatus{ID: f.Spec.ID, State: proto.StateStarting}, cancel: cancel, booted: make(chan struct{})}
	a.mu.Lock()
	a.vms[f.Spec.ID] = e
	a.mu.Unlock()
	a.report(e.status)
	go a.boot(bootCtx, e, prepare)
}

func (a *Agent) boot(ctx context.Context, e *vmEntry, prepare bool) {
	id, dir := e.file.Spec.ID, VMDir(a.VMsDir, e.file.Spec.ID)
	fail := func(step string, err error) {
		close(e.booted)
		a.report(proto.VMStatus{ID: id, State: proto.StateError, Error: fmt.Sprintf("%s: %v", step, err)})
	}
	if prepare {
		if err := a.Provider.Prepare(ctx, e.file, dir); err != nil {
			fail("prepare", err)
			return
		}
	}
	inst, err := a.Provider.Start(e.file, dir)
	if err != nil {
		fail("start", err)
		return
	}
	a.mu.Lock()
	e.inst = inst
	a.mu.Unlock()
	close(e.booted)

	ipCtx, cancel := context.WithTimeout(ctx, ipTimeout)
	go func() {
		select {
		case <-inst.Done():
			cancel()
		case <-ipCtx.Done():
		}
	}()
	ip, err := inst.WaitIP(ipCtx)
	cancel()
	// Reporting stopped last from this goroutine keeps a late IP result from overwriting it.
	select {
	case <-inst.Done():
	default:
		if err != nil {
			a.report(proto.VMStatus{ID: id, State: proto.StateError, Error: "waiting for IP: " + err.Error()})
		} else {
			a.report(proto.VMStatus{ID: id, State: proto.StateRunning, IP: ip})
		}
	}
	<-inst.Done()
	a.report(proto.VMStatus{ID: id, State: proto.StateStopped})
}

// stopAll shuts guests down cleanly; vz VMs live in this process and would otherwise be killed hard.
func (a *Agent) stopAll() {
	a.mu.Lock()
	entries := make([]*vmEntry, 0, len(a.vms))
	for _, e := range a.vms {
		entries = append(entries, e)
	}
	a.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-e.booted
			if e.inst != nil {
				if err := e.inst.Stop(); err != nil {
					log.Printf("vm %s: stop: %v", e.file.Spec.ID, err)
				}
			}
		}()
	}
	wg.Wait()
	a.chassisMu.Lock()
	a.chassisStopping = true
	a.chassisMu.Unlock()
	a.chassisWG.Wait()
	if err := a.Provider.StopChassis(); err != nil {
		log.Printf("stop chassis: %v", err)
	}
}

func (a *Agent) delete(id string) {
	a.mu.Lock()
	e, ok := a.vms[id]
	delete(a.vms, id)
	a.mu.Unlock()
	if !ok {
		return
	}
	e.cancel()
	<-e.booted
	if e.inst != nil {
		if err := e.inst.Stop(); err != nil {
			log.Printf("vm %s: stop: %v", id, err)
		}
	}
	if err := DeleteVMDir(a.VMsDir, id); err != nil {
		log.Printf("vm %s: delete dir: %v", id, err)
	}
}

// report records the status and forwards it to the hub; updates for deleted VMs are dropped.
func (a *Agent) report(st proto.VMStatus) {
	a.mu.Lock()
	e, ok := a.vms[st.ID]
	if ok {
		e.status = st
	}
	send := a.send
	a.mu.Unlock()
	if ok && send != nil {
		if err := send(proto.Message{Type: "status", Status: &st}); err != nil {
			log.Printf("vm %s: status: %v", st.ID, err)
		}
	}
}

func randomMAC() string {
	b := make([]byte, 6)
	rand.Read(b)
	b[0] = b[0]&^0x01 | 0x02 // unicast, locally administered
	return net.HardwareAddr(b).String()
}
