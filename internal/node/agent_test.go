package node

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vm-manager/internal/hub"
	"vm-manager/internal/proto"
)

type fakeInstance struct {
	once    sync.Once
	done    chan struct{}
	stopped atomic.Bool
	killed  atomic.Bool
	p       *fakeProvider
	staleIP bool // simulates finding the previous boot's lease only after the VM already died
}

func (i *fakeInstance) WaitIP(context.Context) (string, error) {
	if i.staleIP {
		<-i.done
	}
	return "127.0.0.1", nil
}
func (i *fakeInstance) Done() <-chan struct{} { return i.done }
func (i *fakeInstance) guestShutdown()        { i.once.Do(func() { close(i.done) }) }

func (i *fakeInstance) Stop() error {
	i.once.Do(func() { i.stopped.Store(true); close(i.done) })
	if i.p != nil {
		i.p.record("stop-guest")
	}
	return nil
}

func (i *fakeInstance) Kill() error {
	i.once.Do(func() { i.killed.Store(true); close(i.done) })
	if i.p != nil {
		i.p.record("kill-guest")
	}
	return nil
}

type fakeProvider struct {
	mu           sync.Mutex
	prepared     []string
	started      map[string]*fakeInstance
	failPrepare  map[string]bool
	failStart    map[string]bool
	staleIP      map[string]bool
	chassis      []Chassis
	block        chan struct{} // when set, StartChassis waits for it to close
	inflight     atomic.Int32
	maxInflight  atomic.Int32
	events       []string
	failDisks    bool
	failReseed   bool
	starts       map[string]int
	bigRoot      bool // makes Disks report a different allocation, as if the guest wrote data
	afterPrepare func(dir string)
}

func (p *fakeProvider) Disks(f VMFile, _ string) ([]proto.Disk, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failDisks {
		return nil, errors.New("disks boom")
	}
	d := proto.Disk{Name: "root", File: "root.img", Device: "vda", SizeBytes: int64(f.Spec.DiskGB) << 30}
	if p.bigRoot {
		d.AllocatedBytes = 42
	}
	return []proto.Disk{d}, nil
}

func (p *fakeProvider) record(e string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e)
}

func (p *fakeProvider) StartChassis(_ context.Context, c Chassis) error {
	n := p.inflight.Add(1)
	defer p.inflight.Add(-1)
	if n > p.maxInflight.Load() {
		p.maxInflight.Store(n)
	}
	p.mu.Lock()
	p.chassis = append(p.chassis, c)
	block := p.block
	p.mu.Unlock()
	p.record("start-begin")
	if block != nil {
		<-block
	}
	p.record("start-end")
	return nil
}

func (p *fakeProvider) StopChassis() error {
	p.record("stop-chassis")
	return nil
}

func (p *fakeProvider) gotChassis() []Chassis {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Chassis(nil), p.chassis...)
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{started: map[string]*fakeInstance{}, starts: map[string]int{}, failPrepare: map[string]bool{}, failStart: map[string]bool{}, staleIP: map[string]bool{}}
}

func (p *fakeProvider) Name() string { return "fake" }

func (p *fakeProvider) Prepare(_ context.Context, f VMFile, dir string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prepared = append(p.prepared, f.Spec.Name)
	p.events = append(p.events, "prepare:"+f.Spec.Name)
	if p.failPrepare[f.Spec.Name] {
		return errors.New("prepare boom")
	}
	if p.afterPrepare != nil {
		p.afterPrepare(dir)
	}
	return nil
}

func (p *fakeProvider) Reseed(_ context.Context, f VMFile, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, "reseed:"+f.Spec.Name)
	if p.failReseed {
		return errors.New("reseed boom")
	}
	return nil
}

func (p *fakeProvider) Start(f VMFile, _ string) (Instance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failStart[f.Spec.Name] {
		return nil, errors.New("start boom")
	}
	p.starts[f.Spec.Name]++
	inst := &fakeInstance{p: p, done: make(chan struct{}), staleIP: p.staleIP[f.Spec.Name]}
	p.started[f.Spec.Name] = inst
	return inst, nil
}

func (p *fakeProvider) instance(name string) *fakeInstance {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started[name]
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type testEnv struct {
	reg      *hub.Registry
	agent    *Agent
	provider *fakeProvider
	stop     context.CancelFunc
	runDone  chan struct{}
}

func startEnv(t *testing.T, prep func(a *Agent, p *fakeProvider)) *testEnv {
	t.Helper()
	net, err := hub.LoadNetModel(filepath.Join(t.TempDir(), "network.json"))
	if err != nil {
		t.Fatal(err)
	}
	hcfgPath := filepath.Join(t.TempDir(), "hub.json")
	if err := os.WriteFile(hcfgPath, []byte(`{"token":"tok","underlay":{"port":51820}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hcfg, err := hub.LoadOrCreateConfig(hcfgPath, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	store, err := hub.LoadVMStore(filepath.Join(t.TempDir(), "vms.json"))
	if err != nil {
		t.Fatal(err)
	}
	reg := hub.NewRegistry(net, store, hcfg)
	srv := httptest.NewServer(hub.NodeHandler(reg, func() string { return hcfg.Get().Token }))

	p := newFakeProvider()
	a := &Agent{
		HubURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/nodes/connect", Token: "tok",
		NodeID: "n1", VMsDir: t.TempDir(), CPUs: 4, MemoryMB: 8192, Provider: p,
	}
	if prep != nil {
		prep(a, p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		srv.Close()
		<-done
	})
	eventually(t, "node online", func() bool { n := reg.Nodes(); return len(n) == 1 && n[0].State == "online" })
	return &testEnv{reg: reg, agent: a, provider: p, stop: cancel, runDone: done}
}

func createReq(name string) hub.CreateVMRequest {
	return hub.CreateVMRequest{NodeID: "n1", Name: name, CPUs: 1, MemoryMB: 1024, DiskGB: 10, SSHPublicKeys: []string{"ssh-ed25519 AAA"}}
}

func vmState(reg *hub.Registry, id string) (hub.VM, bool) {
	for _, vm := range reg.VMs() {
		if vm.ID == id {
			return vm, true
		}
	}
	return hub.VM{}, false
}

func TestAgentRegistersProvider(t *testing.T) {
	env := startEnv(t, func(a *Agent, _ *fakeProvider) {
		a.Config = json.RawMessage(`{"provider":"fake"}`)
		a.Machine = &proto.Machine{OS: "darwin", Arch: "arm64", CPUs: 8}
	})
	n := env.reg.Nodes()[0]
	if n.Provider != "fake" || string(n.Config) != `{"provider":"fake"}` || n.Machine == nil || n.Machine.CPUs != 8 || n.ConnectedAt.IsZero() {
		t.Fatalf("node = %+v", n)
	}
}

func TestAgentCreateReachesRunning(t *testing.T) {
	env := startEnv(t, nil)
	vm, err := env.reg.CreateVM(createReq("dev"))
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	if v, _ := vmState(env.reg, vm.ID); v.IP != "127.0.0.1" {
		t.Fatalf("ip = %q", v.IP)
	}
	files, _ := LoadVMs(env.agent.VMsDir)
	if len(files) != 1 || files[0].Spec.ID != vm.ID {
		t.Fatalf("vm.json = %+v", files)
	}
	if mac, err := net.ParseMAC(files[0].MAC); err != nil || mac[0]&0x02 == 0 || mac[0]&0x01 != 0 {
		t.Fatalf("mac %q not locally administered unicast (%v)", files[0].MAC, err)
	}
}

func TestAgentRestartsWhenGuestShutsDown(t *testing.T) {
	fastRestarts(t, 10*time.Millisecond)
	env := startEnv(t, nil)
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	first := env.provider.instance("dev")
	first.guestShutdown()
	eventually(t, "restarted", func() bool {
		v, _ := vmState(env.reg, vm.ID)
		return env.provider.instance("dev") != first && v.State == proto.StateRunning
	})
}

func TestAgentPrepareFails(t *testing.T) {
	env := startEnv(t, func(_ *Agent, p *fakeProvider) { p.failPrepare["bad"] = true })
	vm, _ := env.reg.CreateVM(createReq("bad"))
	eventually(t, "error", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateError })
	if v, _ := vmState(env.reg, vm.ID); !strings.Contains(v.Error, "prepare boom") {
		t.Fatalf("error = %q", v.Error)
	}
}

func TestAgentPrepareSaveFails(t *testing.T) {
	env := startEnv(t, func(_ *Agent, p *fakeProvider) {
		p.afterPrepare = func(dir string) {
			os.Chmod(dir, 0o555)
			t.Cleanup(func() { os.Chmod(dir, 0o755) })
		}
	})
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "error", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateError })
	if v, _ := vmState(env.reg, vm.ID); !strings.HasPrefix(v.Error, "prepare: ") {
		t.Fatalf("error = %q", v.Error)
	}
	if env.provider.instance("dev") != nil {
		t.Fatal("instance started although NeedsPrepare was not saved")
	}
}

func TestAgentWithChangedNodeIDKeepsVMs(t *testing.T) {
	env := startEnv(t, nil)
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	env.stop()
	<-env.runDone

	p := newFakeProvider()
	a := &Agent{HubURL: env.agent.HubURL, Token: "tok", NodeID: "renamed", VMsDir: env.agent.VMsDir, CPUs: 4, MemoryMB: 8192, Provider: p}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, "booted from cache", func() bool { return p.instance("dev") != nil })
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(VMDir(a.VMsDir, vm.ID)); err != nil {
		t.Fatalf("vm dir: %v", err)
	}
	for _, n := range env.reg.Nodes() {
		if n.ID == "renamed" {
			t.Fatalf("renamed node registered: %+v", n)
		}
	}
}

func TestAgentDelete(t *testing.T) {
	env := startEnv(t, nil)
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	if err := env.reg.DeleteVM(vm.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "dir removed", func() bool { _, err := os.Stat(VMDir(env.agent.VMsDir, vm.ID)); return os.IsNotExist(err) })
	if !env.provider.instance("dev").killed.Load() {
		t.Fatal("instance not killed")
	}
}

func cachedVM(id, name string) VMFile {
	return VMFile{Spec: proto.VMSpec{ID: id, Name: name, CPUs: 1, MemoryMB: 1024, DiskGB: 10, SSHPublicKeys: []string{"k"}},
		MAC: "02:00:00:00:00:01", Power: proto.PowerRunning, Generation: 1}
}

func TestAgentDeletesVMsUnknownToHub(t *testing.T) {
	env := startEnv(t, func(a *Agent, _ *fakeProvider) {
		SaveVM(a.VMsDir, cachedVM("g1", "good"))
		SaveVM(a.VMsDir, cachedVM("s1", "sleepy"))
	})
	for _, id := range []string{"g1", "s1"} {
		eventually(t, id+" dir removed", func() bool { _, err := os.Stat(VMDir(env.agent.VMsDir, id)); return os.IsNotExist(err) })
	}
	if len(env.reg.VMs()) != 0 {
		t.Fatalf("hub adopted VMs: %+v", env.reg.VMs())
	}
	if len(env.provider.prepared) != 0 {
		t.Fatalf("Prepare called on restore: %v", env.provider.prepared)
	}
}

func TestAgentBootsFromCacheWithoutHub(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unreachable := "ws://" + ln.Addr().String() + "/api/nodes/connect"
	ln.Close()
	p := newFakeProvider()
	a := &Agent{HubURL: unreachable, Token: "tok", NodeID: "n1", VMsDir: t.TempDir(), CPUs: 4, MemoryMB: 8192, Provider: p}
	stopped := cachedVM("s1", "sleepy")
	stopped.Power = proto.PowerStopped
	SaveVM(a.VMsDir, cachedVM("g1", "good"))
	SaveVM(a.VMsDir, stopped)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, "good started", func() bool { return p.instance("good") != nil })
	time.Sleep(100 * time.Millisecond)
	if p.instance("sleepy") != nil {
		t.Fatal("stopped VM started")
	}
	for _, id := range []string{"g1", "s1"} {
		if _, err := os.Stat(VMDir(a.VMsDir, id)); err != nil {
			t.Fatalf("vm dir %s: %v", id, err)
		}
	}
}

func TestAgentStoppedNotOverwrittenByLateIP(t *testing.T) {
	env := startEnv(t, func(_ *Agent, p *fakeProvider) { p.staleIP["dev"] = true })
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "started", func() bool { return env.provider.instance("dev") != nil })
	env.provider.instance("dev").guestShutdown()
	eventually(t, "stopped", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateStopped })
	time.Sleep(200 * time.Millisecond)
	if v, _ := vmState(env.reg, vm.ID); v.State != proto.StateStopped {
		t.Fatalf("state = %s, want stopped", v.State)
	}
}

func TestAgentStopsVMsOnShutdown(t *testing.T) {
	env := startEnv(t, nil)
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	env.stop()
	<-env.runDone
	if !env.provider.instance("dev").stopped.Load() {
		t.Fatal("VM not stopped on agent shutdown")
	}
	if _, err := os.Stat(VMDir(env.agent.VMsDir, vm.ID)); err != nil {
		t.Fatalf("vm dir must survive shutdown: %v", err)
	}
}

func TestAgentSendsChassisKeyAndStartsChassis(t *testing.T) {
	env := startEnv(t, func(a *Agent, _ *fakeProvider) {
		a.ChassisKey, a.ChassisPrivateKey = "pub-key", "priv-key"
	})
	eventually(t, "chassis started", func() bool { return len(env.provider.gotChassis()) > 0 })
	c := env.provider.gotChassis()[0]
	u, _ := url.Parse(env.agent.HubURL)
	if c.Address != "10.198.0.11/24" {
		t.Fatalf("hub did not enroll the chassis key: address %q", c.Address)
	}
	if c.Name != "n1" || c.PrivateKey != "priv-key" || !strings.HasPrefix(c.Address, "10.198.0.") || !strings.HasSuffix(c.Address, "/24") ||
		c.Endpoint != net.JoinHostPort(u.Hostname(), "51820") || c.MTU != 1420 || c.HubKey == "" || c.SBRemote == "" {
		t.Fatalf("chassis = %+v", c)
	}
}

func TestAgentUnderlayEndpointOverride(t *testing.T) {
	env := startEnv(t, func(a *Agent, _ *fakeProvider) {
		a.ChassisKey, a.ChassisPrivateKey, a.UnderlayEndpoint, a.UnderlayMTU = "pub-key", "priv-key", "1.2.3.4:6000", 1400
	})
	eventually(t, "chassis started", func() bool { return len(env.provider.gotChassis()) > 0 })
	if c := env.provider.gotChassis()[0]; c.Endpoint != "1.2.3.4:6000" || c.MTU != 1400 {
		t.Fatalf("chassis = %+v", c)
	}
}

func TestAgentStopsChassisAfterGuests(t *testing.T) {
	env := startEnv(t, nil)
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	env.stop()
	<-env.runDone
	env.provider.mu.Lock()
	defer env.provider.mu.Unlock()
	if got := strings.Join(env.provider.events, ","); got != "prepare:dev,stop-guest,stop-chassis" {
		t.Fatalf("events = %s", got)
	}
}

func TestAgentSerializesChassisStarts(t *testing.T) {
	env := startEnv(t, nil)
	env.provider.block = make(chan struct{})
	go env.agent.startChassis(context.Background(), proto.Underlay{Address: "10.198.0.11/24"})
	eventually(t, "first call", func() bool { return len(env.provider.gotChassis()) == 1 })
	go env.agent.startChassis(context.Background(), proto.Underlay{Address: "10.198.0.12/24"})
	time.Sleep(100 * time.Millisecond)
	close(env.provider.block)
	eventually(t, "second call", func() bool { return len(env.provider.gotChassis()) == 2 })
	if env.provider.maxInflight.Load() != 1 {
		t.Fatalf("StartChassis calls overlapped")
	}
	if got := env.provider.gotChassis()[1].Address; got != "10.198.0.12/24" {
		t.Fatalf("last call carries %q, want newest config", got)
	}
}

func TestAgentStopChassisWaitsForInFlightStart(t *testing.T) {
	env := startEnv(t, nil)
	env.provider.block = make(chan struct{})
	go env.agent.startChassis(context.Background(), proto.Underlay{Address: "10.198.0.11/24"})
	eventually(t, "start in flight", func() bool { return len(env.provider.gotChassis()) == 1 })
	env.stop()
	time.Sleep(100 * time.Millisecond)
	env.provider.mu.Lock()
	early := strings.Contains(strings.Join(env.provider.events, ","), "stop-chassis")
	env.provider.mu.Unlock()
	if early {
		t.Fatal("StopChassis ran while StartChassis was in flight")
	}
	close(env.provider.block)
	<-env.runDone
	env.provider.mu.Lock()
	defer env.provider.mu.Unlock()
	if got := strings.Join(env.provider.events, ","); got != "start-begin,start-end,stop-chassis" {
		t.Fatalf("events = %s", got)
	}
	env.agent.startChassis(context.Background(), proto.Underlay{Address: "10.198.0.13/24"})
	if len(env.provider.chassis) != 1 {
		t.Fatal("chassis started after shutdown")
	}
}

func TestAgentStatusCarriesDisks(t *testing.T) {
	env := startEnv(t, nil)
	vm, err := env.reg.CreateVM(createReq("dev"))
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "running with disks", func() bool {
		v, _ := vmState(env.reg, vm.ID)
		return v.State == proto.StateRunning && len(v.Disks) == 1 && v.Disks[0].Device == "vda"
	})
}

func TestAgentOmitsDisksOnProviderError(t *testing.T) {
	env := startEnv(t, func(_ *Agent, p *fakeProvider) { p.failDisks = true })
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	if v, _ := vmState(env.reg, vm.ID); len(v.Disks) != 0 {
		t.Fatalf("disks = %+v", v.Disks)
	}
}

func TestAgentRefreshesDisksWhileRunning(t *testing.T) {
	env := startEnv(t, func(a *Agent, _ *fakeProvider) { a.diskRefresh = 20 * time.Millisecond })
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	env.provider.mu.Lock()
	env.provider.bigRoot = true
	env.provider.mu.Unlock()
	eventually(t, "refreshed disks", func() bool {
		v, _ := vmState(env.reg, vm.ID)
		return len(v.Disks) == 1 && v.Disks[0].AllocatedBytes == 42 && v.State == proto.StateRunning
	})
}

func (p *fakeProvider) hasEvent(e string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Contains(p.events, e)
}

func TestAgentShutdownAndStart(t *testing.T) {
	env := startEnv(t, nil)
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	first := env.provider.instance("dev")
	if _, err := env.reg.SetPower(vm.ID, proto.PowerStopped, proto.StopGraceful); err != nil {
		t.Fatal(err)
	}
	eventually(t, "stopped", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateStopped })
	if !env.provider.hasEvent("stop-guest") || env.provider.hasEvent("kill-guest") {
		t.Fatalf("events = %v", env.provider.events)
	}
	if _, err := env.reg.SetPower(vm.ID, proto.PowerRunning, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "running again", func() bool {
		v, _ := vmState(env.reg, vm.ID)
		return v.State == proto.StateRunning && env.provider.instance("dev") != first
	})
}

func TestAgentHardStop(t *testing.T) {
	env := startEnv(t, nil)
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	if _, err := env.reg.SetPower(vm.ID, proto.PowerStopped, proto.StopHard); err != nil {
		t.Fatal(err)
	}
	eventually(t, "stopped", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateStopped })
	if !env.provider.hasEvent("kill-guest") {
		t.Fatalf("events = %v", env.provider.events)
	}
}

func TestAgentRebuildAfterDirMissing(t *testing.T) {
	env := startEnv(t, nil)
	vm, _ := env.reg.CreateVM(createReq("dev"))
	eventually(t, "running", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	env.stop()
	<-env.runDone
	if err := os.RemoveAll(VMDir(env.agent.VMsDir, vm.ID)); err != nil {
		t.Fatal(err)
	}
	old := env.agent
	restarted := &Agent{HubURL: old.HubURL, Token: old.Token, NodeID: old.NodeID, VMsDir: old.VMsDir, CPUs: old.CPUs, MemoryMB: old.MemoryMB, Provider: env.provider}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { restarted.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, "dir missing", func() bool {
		v, _ := vmState(env.reg, vm.ID)
		return v.State == proto.StateError && v.Error == proto.ErrVMDirMissing
	})
	if _, err := env.reg.SetPower(vm.ID, proto.PowerRunning, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "running again", func() bool { v, _ := vmState(env.reg, vm.ID); return v.State == proto.StateRunning })
	if n := len(env.provider.prepared); n != 2 {
		t.Fatalf("prepared = %v", env.provider.prepared)
	}
}
