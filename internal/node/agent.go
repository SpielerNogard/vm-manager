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
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"vm-manager/internal/proto"
)

const (
	reconnectDelay = 2 * time.Second
	ipTimeout      = 120 * time.Second

	defaultUnderlayMTU = 1420

	defaultDiskRefresh = 60 * time.Second
)

var (
	restartBackoff = []time.Duration{1 * time.Second, 2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second}
	quickRun       = 60 * time.Second
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

	diskRefresh time.Duration // zero means defaultDiskRefresh

	chassisMu       sync.Mutex
	chassisPending  *Chassis // newest config not yet applied; older ones are superseded
	chassisRunning  bool
	chassisStopping bool
	chassisWG       sync.WaitGroup

	reconcileMu sync.Mutex

	mu       sync.Mutex
	vms      map[string]*vmEntry
	send     func(proto.Message) error // nil while disconnected
	stopping bool
}

type vmEntry struct {
	file   VMFile
	status proto.VMStatus
	inst   Instance
	cancel context.CancelFunc
	booted chan struct{} // closed once boot has finished starting (or failed)
	// placeholder entries stand in for provisioned VMs whose directory is gone; they never boot.
	placeholder   bool
	stopRequested atomic.Bool // set before node-initiated Stop/Kill so the exit is not treated as a crash
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
		a.startVM(ctx, f, f.NeedsPrepare)
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
	connCtx, stopRefresh := context.WithCancel(ctx)
	go a.refreshDisksLoop(connCtx)
	defer func() {
		stopRefresh()
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
		case m.Type == "desired":
			a.reconcile(ctx, m.Desired)
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
		st := e.status
		st.Disks = a.disks(e.file)
		reg.VMs = append(reg.VMs, st)
	}
	return reg
}

func (a *Agent) startVM(ctx context.Context, f VMFile, prepare bool) {
	state := proto.StateStarting
	if f.Power == proto.PowerStopped {
		state = proto.StateStopped
	}
	bootCtx, cancel := context.WithCancel(ctx)
	e := &vmEntry{file: f, status: proto.VMStatus{ID: f.Spec.ID, State: state}, cancel: cancel, booted: make(chan struct{})}
	a.mu.Lock()
	a.vms[f.Spec.ID] = e
	a.mu.Unlock()
	a.report(e.status)
	go a.boot(bootCtx, e, f, prepare)
}

// boot prepares (if asked) and starts f; a stopped f is only prepared. Exited instances are restarted while f should run.
func (a *Agent) boot(ctx context.Context, e *vmEntry, f VMFile, prepare bool) {
	backoff, quick := restartBackoff, quickRun
	id, dir := f.Spec.ID, VMDir(a.VMsDir, f.Spec.ID)
	report := func(st proto.VMStatus) { a.reportEntry(e, st) }
	fail := func(step string, err error) {
		close(e.booted)
		report(proto.VMStatus{ID: id, State: proto.StateError, Error: fmt.Sprintf("%s: %v", step, err)})
	}
	if prepare {
		if err := a.prepare(ctx, e, f); err != nil {
			fail("prepare", err)
			return
		}
	}
	if f.Power == proto.PowerStopped {
		close(e.booted)
		report(proto.VMStatus{ID: id, State: proto.StateStopped})
		return
	}
	inst, err := a.Provider.Start(f, dir)
	if err != nil {
		fail("start", err)
		return
	}
	a.mu.Lock()
	e.inst = inst
	a.mu.Unlock()
	close(e.booted)

	failures := 0
	for {
		ranAt := time.Now()
		a.watch(ctx, e, id, inst)
		ran := time.Since(ranAt)
		if ran >= quick {
			failures = 0
		}
		reason := fmt.Errorf("exited after %s", ran.Round(time.Second))
		a.mu.Lock()
		ok := a.restartable(ctx, e)
		a.mu.Unlock()
		if !ok {
			return
		}
		for inst = nil; inst == nil; {
			if failures == len(backoff) {
				report(proto.VMStatus{ID: id, State: proto.StateError, Error: "restarting failed repeatedly: " + reason.Error()})
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff[failures]):
			}
			// Counted up front; a run longer than quick resets it.
			failures++
			inst, err = a.restart(ctx, e)
			if err != nil {
				reason = err
			} else if inst == nil {
				return
			}
		}
		report(proto.VMStatus{ID: id, State: proto.StateStarting})
	}
}

func (a *Agent) prepare(ctx context.Context, e *vmEntry, f VMFile) error {
	if err := a.Provider.Prepare(ctx, f, VMDir(a.VMsDir, f.Spec.ID)); err != nil {
		return err
	}
	a.mu.Lock()
	e.file.NeedsPrepare = false
	saved := e.file
	a.mu.Unlock()
	if err := SaveVM(a.VMsDir, saved); err != nil {
		log.Printf("vm %s: save: %v", f.Spec.ID, err)
	}
	return nil
}

// watch reports inst's progress until it exits, reporting stopped last.
func (a *Agent) watch(ctx context.Context, e *vmEntry, id string, inst Instance) {
	report := func(st proto.VMStatus) { a.reportEntry(e, st) }
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
			report(proto.VMStatus{ID: id, State: proto.StateError, Error: "waiting for IP: " + err.Error()})
		} else {
			report(proto.VMStatus{ID: id, State: proto.StateRunning, IP: ip})
		}
	}
	<-inst.Done()
	report(proto.VMStatus{ID: id, State: proto.StateStopped})
}

func (a *Agent) instance(e *vmEntry) Instance {
	a.mu.Lock()
	defer a.mu.Unlock()
	return e.inst
}

// restartable reports whether e's exited instance should be started again; a.mu must be held.
func (a *Agent) restartable(ctx context.Context, e *vmEntry) bool {
	return ctx.Err() == nil && !a.stopping && !e.stopRequested.Load() && e.file.Power != proto.PowerStopped
}

// restart starts e again; (nil, nil) means it should no longer run.
func (a *Agent) restart(ctx context.Context, e *vmEntry) (Instance, error) {
	// Holding reconcileMu keeps reconcile from starting a second instance while this one comes up.
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()
	a.mu.Lock()
	f, ok := e.file, a.restartable(ctx, e)
	a.mu.Unlock()
	if !ok {
		return nil, nil
	}
	if f.NeedsPrepare {
		if err := a.prepare(ctx, e, f); err != nil {
			return nil, fmt.Errorf("prepare: %w", err)
		}
	}
	inst, err := a.Provider.Start(f, VMDir(a.VMsDir, f.Spec.ID))
	if err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	a.mu.Lock()
	ok = a.restartable(ctx, e)
	if ok {
		e.inst = inst
	}
	a.mu.Unlock()
	if !ok {
		if err := inst.Kill(); err != nil {
			log.Printf("vm %s: kill: %v", f.Spec.ID, err)
		}
		return nil, nil
	}
	return inst, nil
}

// stopAll shuts guests down cleanly; vz VMs live in this process and would otherwise be killed hard.
func (a *Agent) stopAll() {
	a.mu.Lock()
	a.stopping = true
	entries := make(map[string]*vmEntry, len(a.vms))
	for id, e := range a.vms {
		entries[id] = e
	}
	a.mu.Unlock()
	var wg sync.WaitGroup
	for id, e := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-e.booted
			if inst := a.instance(e); inst != nil {
				if err := inst.Stop(); err != nil {
					log.Printf("vm %s: stop: %v", id, err)
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

// report records the status and forwards it to the hub; updates for deleted VMs are dropped.
func (a *Agent) report(st proto.VMStatus) { a.reportEntry(nil, st) }

// reportEntry is report that, with want set, also drops updates once want was replaced by a restart.
func (a *Agent) reportEntry(want *vmEntry, st proto.VMStatus) {
	a.mu.Lock()
	e, ok := a.vms[st.ID]
	ok = ok && (want == nil || e == want)
	var f VMFile
	if ok {
		f = e.file
	}
	a.mu.Unlock()
	if ok {
		st.Disks = a.disks(f)
	}
	a.mu.Lock()
	e, ok = a.vms[st.ID]
	ok = ok && (want == nil || e == want)
	if ok {
		st.Generation = e.file.Generation
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

func (a *Agent) disks(f VMFile) []proto.Disk {
	disks, err := a.Provider.Disks(f, VMDir(a.VMsDir, f.Spec.ID))
	if err != nil {
		log.Printf("vm %s: disks: %v", f.Spec.ID, err)
		return nil
	}
	return disks
}

func (a *Agent) refreshDisksLoop(ctx context.Context) {
	interval := a.diskRefresh
	if interval == 0 {
		interval = defaultDiskRefresh
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.refreshDisks()
		}
	}
}

// refreshDisks re-sends each running VM's stored status with fresh disks, so a newer state is never overwritten.
func (a *Agent) refreshDisks() {
	a.mu.Lock()
	var running []VMFile
	for _, e := range a.vms {
		if e.status.State == proto.StateRunning {
			running = append(running, e.file)
		}
	}
	a.mu.Unlock()
	for _, f := range running {
		disks := a.disks(f)
		a.mu.Lock()
		e, ok := a.vms[f.Spec.ID]
		if !ok || e.status.State != proto.StateRunning {
			a.mu.Unlock()
			continue
		}
		e.status.Disks = disks
		st, send := e.status, a.send
		a.mu.Unlock()
		if send != nil {
			if err := send(proto.Message{Type: "status", Status: &st}); err != nil {
				log.Printf("vm %s: status: %v", f.Spec.ID, err)
			}
		}
	}
}

func randomMAC() string {
	b := make([]byte, 6)
	rand.Read(b)
	b[0] = b[0]&^0x01 | 0x02 // unicast, locally administered
	return net.HardwareAddr(b).String()
}
