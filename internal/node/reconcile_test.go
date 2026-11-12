package node

import (
	"context"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"vm-manager/internal/proto"
)

func dv(id, name string, power proto.Power, gen uint64) proto.DesiredVM {
	return proto.DesiredVM{
		Spec:  proto.VMSpec{ID: id, Name: name, CPUs: 1, MemoryMB: 1024, DiskGB: 10, SSHPublicKeys: []string{"k"}},
		Power: power, Generation: gen,
	}
}

func newReconcileAgent(t *testing.T) (*Agent, *fakeProvider) {
	t.Helper()
	p := newFakeProvider()
	a := &Agent{NodeID: "n1", VMsDir: t.TempDir(), Provider: p, vms: map[string]*vmEntry{}}
	t.Cleanup(a.stopAll)
	return a, p
}

func (a *Agent) vmStatus(id string) (proto.VMStatus, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.vms[id]
	if !ok {
		return proto.VMStatus{}, false
	}
	return e.status, true
}

func (p *fakeProvider) gotEvents() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.events...)
}

func (p *fakeProvider) gotPrepared() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.prepared...)
}

func loadVM(t *testing.T, a *Agent, id string) VMFile {
	t.Helper()
	files, err := LoadVMs(a.VMsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Spec.ID == id {
			return f
		}
	}
	t.Fatalf("vm.json for %s missing", id)
	return VMFile{}
}

func waitState(t *testing.T, a *Agent, id string, state proto.VMState, gen uint64) {
	t.Helper()
	eventually(t, string(state), func() bool {
		st, ok := a.vmStatus(id)
		return ok && st.State == state && st.Generation == gen
	})
}

func TestReconcileCreatesMissing(t *testing.T) {
	a, p := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerRunning, 1)})
	waitState(t, a, "a", proto.StateRunning, 1)
	if p.instance("a") == nil {
		t.Fatal("not started")
	}
	if f := loadVM(t, a, "a"); f.Generation != 1 || f.Power != proto.PowerRunning || f.MAC == "" {
		t.Fatalf("vm.json = %+v", f)
	}
}

func TestReconcileCreatesStoppedWithoutStarting(t *testing.T) {
	a, p := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerStopped, 1)})
	eventually(t, "prepared", func() bool { return slices.Contains(p.gotPrepared(), "a") })
	waitState(t, a, "a", proto.StateStopped, 1)
	if p.instance("a") != nil {
		t.Fatal("stopped VM was started")
	}
}

func TestReconcileDeletesUnknownBeforeCreate(t *testing.T) {
	a, p := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("old", "old", proto.PowerRunning, 1)})
	waitState(t, a, "old", proto.StateRunning, 1)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("new", "new", proto.PowerRunning, 1)})
	if !p.instance("old").killed.Load() {
		t.Fatal("old not killed")
	}
	if _, err := os.Stat(VMDir(a.VMsDir, "old")); !os.IsNotExist(err) {
		t.Fatalf("old dir still there: %v", err)
	}
	if _, ok := a.vmStatus("old"); ok {
		t.Fatal("old entry still there")
	}
	waitState(t, a, "new", proto.StateRunning, 1)
	ev := strings.Join(p.gotEvents(), ",")
	if k, n := strings.Index(ev, "kill-guest"), strings.Index(ev, "prepare:new"); k < 0 || n < 0 || k > n {
		t.Fatalf("events = %s", ev)
	}
}

func TestReconcileProvisionedMissingDirReportsError(t *testing.T) {
	a, p := newReconcileAgent(t)
	d := dv("a", "a", proto.PowerRunning, 1)
	d.Provisioned = true
	a.reconcile(context.Background(), 0, []proto.DesiredVM{d})
	st, ok := a.vmStatus("a")
	if !ok || st.State != proto.StateError || st.Error != proto.ErrVMDirMissing || st.Generation != 1 {
		t.Fatalf("status = %+v", st)
	}
	if len(p.gotPrepared()) != 0 {
		t.Fatalf("prepared = %v", p.gotPrepared())
	}
}

func TestReconcileRebuildsPlaceholder(t *testing.T) {
	a, p := newReconcileAgent(t)
	d := dv("a", "a", proto.PowerRunning, 1)
	d.Provisioned = true
	a.reconcile(context.Background(), 0, []proto.DesiredVM{d})
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerRunning, 2)})
	waitState(t, a, "a", proto.StateRunning, 2)
	if !slices.Contains(p.gotPrepared(), "a") {
		t.Fatal("placeholder started without Prepare")
	}
	if f := loadVM(t, a, "a"); f.Generation != 2 {
		t.Fatalf("vm.json = %+v", f)
	}
}

func testReconcileStop(t *testing.T, mode proto.StopMode) *fakeInstance {
	a, p := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerRunning, 1)})
	waitState(t, a, "a", proto.StateRunning, 1)
	d := dv("a", "a", proto.PowerStopped, 2)
	d.StopMode = mode
	a.reconcile(context.Background(), 0, []proto.DesiredVM{d})
	waitState(t, a, "a", proto.StateStopped, 2)
	a.mu.Lock()
	intentional := a.vms["a"].stopRequested.Load()
	a.mu.Unlock()
	if !intentional {
		t.Fatal("stop not marked as requested")
	}
	if f := loadVM(t, a, "a"); f.Power != proto.PowerStopped || f.StopMode != mode || f.Generation != 2 {
		t.Fatalf("vm.json = %+v", f)
	}
	return p.instance("a")
}

func TestReconcileShutdownGraceful(t *testing.T) {
	if inst := testReconcileStop(t, proto.StopGraceful); !inst.stopped.Load() || inst.killed.Load() {
		t.Fatalf("stopped=%v killed=%v", inst.stopped.Load(), inst.killed.Load())
	}
}

func TestReconcileStopHard(t *testing.T) {
	if inst := testReconcileStop(t, proto.StopHard); !inst.killed.Load() || inst.stopped.Load() {
		t.Fatalf("stopped=%v killed=%v", inst.stopped.Load(), inst.killed.Load())
	}
}

func TestReconcileStartsStopped(t *testing.T) {
	a, p := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerStopped, 1)})
	waitState(t, a, "a", proto.StateStopped, 1)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerRunning, 2)})
	waitState(t, a, "a", proto.StateRunning, 2)
	if got := p.gotPrepared(); len(got) != 1 {
		t.Fatalf("prepared = %v, want once", got)
	}
}

func TestReconcileRetriesFailedPrepare(t *testing.T) {
	a, p := newReconcileAgent(t)
	p.mu.Lock()
	p.failPrepare["a"] = true
	p.mu.Unlock()
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerRunning, 1)})
	waitState(t, a, "a", proto.StateError, 1)
	if f := loadVM(t, a, "a"); !f.NeedsPrepare {
		t.Fatalf("vm.json = %+v, want NeedsPrepare after failed Prepare", f)
	}
	p.mu.Lock()
	p.failPrepare["a"] = false
	p.mu.Unlock()
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerRunning, 2)})
	waitState(t, a, "a", proto.StateRunning, 2)
	if got := p.gotPrepared(); len(got) != 2 {
		t.Fatalf("prepared = %v, want twice", got)
	}
	if f := loadVM(t, a, "a"); f.NeedsPrepare {
		t.Fatalf("vm.json = %+v, want prepared", f)
	}
}

func TestReconcileSameGenerationNoop(t *testing.T) {
	a, p := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerRunning, 1)})
	waitState(t, a, "a", proto.StateRunning, 1)
	inst := p.instance("a")
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerStopped, 1)})
	if p.instance("a") != inst {
		t.Fatal("instance replaced")
	}
	for _, e := range p.gotEvents() {
		if e == "stop-guest" || e == "kill-guest" {
			t.Fatalf("events = %v", p.gotEvents())
		}
	}
	if st, _ := a.vmStatus("a"); st.State != proto.StateRunning {
		t.Fatalf("status = %+v", st)
	}
}

func TestReconcileBackToBack(t *testing.T) {
	a, _ := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerRunning, 1)})
	waitState(t, a, "a", proto.StateRunning, 1)
	graceful, hard := dv("a", "a", proto.PowerStopped, 2), dv("a", "a", proto.PowerStopped, 3)
	graceful.StopMode, hard.StopMode = proto.StopGraceful, proto.StopHard
	var wg sync.WaitGroup
	for _, d := range []proto.DesiredVM{graceful, hard} {
		wg.Add(1)
		go func() { defer wg.Done(); a.reconcile(context.Background(), 0, []proto.DesiredVM{d}) }()
	}
	wg.Wait()
	waitState(t, a, "a", proto.StateStopped, 3)
	if f := loadVM(t, a, "a"); f.Generation != 3 {
		t.Fatalf("vm.json = %+v", f)
	}
}

func TestRestoreKeepsStoppedVMsStopped(t *testing.T) {
	a, p := newReconcileAgent(t)
	on := VMFile{Spec: dv("on", "on", "", 1).Spec, MAC: "02:00:00:00:00:01"}
	off := VMFile{Spec: dv("off", "off", "", 1).Spec, MAC: "02:00:00:00:00:02", Power: proto.PowerStopped, Generation: 4}
	SaveVM(a.VMsDir, on)
	SaveVM(a.VMsDir, off)
	if err := a.restoreAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitState(t, a, "on", proto.StateRunning, 0)
	waitState(t, a, "off", proto.StateStopped, 4)
	if p.instance("off") != nil || len(p.gotPrepared()) != 0 {
		t.Fatalf("off started=%v prepared=%v", p.instance("off") != nil, p.gotPrepared())
	}
}

func fastRestarts(t *testing.T, backoff ...time.Duration) {
	t.Helper()
	oldBackoff, oldQuick := restartBackoff, quickRun
	restartBackoff, quickRun = backoff, time.Hour
	t.Cleanup(func() { restartBackoff, quickRun = oldBackoff, oldQuick })
}

func TestRestartAfterGuestPoweroff(t *testing.T) {
	fastRestarts(t, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond)
	a, p := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerRunning, 1)})
	waitState(t, a, "a", proto.StateRunning, 1)
	first := p.instance("a")
	first.guestShutdown()
	eventually(t, "restarted", func() bool {
		st, _ := a.vmStatus("a")
		return p.instance("a") != first && st.State == proto.StateRunning
	})
}

func TestRestartGivesUpAfterQuickFailures(t *testing.T) {
	fastRestarts(t, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond)
	a, p := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerRunning, 1)})
	waitState(t, a, "a", proto.StateRunning, 1)
	p.mu.Lock()
	p.failStart["a"] = true
	p.mu.Unlock()
	p.instance("a").guestShutdown()
	eventually(t, "given up", func() bool {
		st, _ := a.vmStatus("a")
		return st.State == proto.StateError && strings.Contains(st.Error, "restarting failed repeatedly")
	})
	p.mu.Lock()
	p.failStart["a"] = false
	p.mu.Unlock()
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerRunning, 2)})
	waitState(t, a, "a", proto.StateRunning, 2)
}

func TestShutdownCancelsBackoff(t *testing.T) {
	fastRestarts(t, time.Hour)
	a, p := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dv("a", "a", proto.PowerRunning, 1)})
	waitState(t, a, "a", proto.StateRunning, 1)
	first := p.instance("a")
	first.guestShutdown()
	waitState(t, a, "a", proto.StateStopped, 1)
	d := dv("a", "a", proto.PowerStopped, 2)
	d.StopMode = proto.StopGraceful
	a.reconcile(context.Background(), 0, []proto.DesiredVM{d})
	waitState(t, a, "a", proto.StateStopped, 2)
	time.Sleep(50 * time.Millisecond)
	if p.instance("a") != first {
		t.Fatal("restarted after shutdown")
	}
	if st, _ := a.vmStatus("a"); st.State != proto.StateStopped {
		t.Fatalf("status = %+v", st)
	}
}

func TestStopAllDoesNotRestart(t *testing.T) {
	fastRestarts(t, 10*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond)
	p := newFakeProvider()
	a := &Agent{HubURL: "ws://127.0.0.1:1", NodeID: "n1", VMsDir: t.TempDir(), Provider: p}
	SaveVM(a.VMsDir, VMFile{Spec: dv("a", "a", "", 1).Spec, MAC: "02:00:00:00:00:01", Power: proto.PowerRunning, Generation: 1})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	eventually(t, "started", func() bool { return p.instance("a") != nil })
	first := p.instance("a")
	cancel()
	<-done
	time.Sleep(50 * time.Millisecond)
	if p.instance("a") != first || !first.stopped.Load() {
		t.Fatalf("restarted=%v stopped=%v", p.instance("a") != first, first.stopped.Load())
	}
	if f := loadVM(t, a, "a"); f.Power != proto.PowerRunning {
		t.Fatalf("vm.json = %+v", f)
	}
}

var (
	mgmtNIC = proto.NIC{Network: "mgmt", MAC: "02:0a:c8:00:00:0a"}
	netNIC  = proto.NIC{Network: "n1", MAC: "02:00:00:00:01:0a"}
)

func dvNICs(power proto.Power, gen uint64, nics ...proto.NIC) proto.DesiredVM {
	d := dv("a", "a", power, gen)
	d.Spec.NICs = nics
	return d
}

func TestReconcileNICChangeRestartsWithReseed(t *testing.T) {
	a, p := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dvNICs(proto.PowerRunning, 1, mgmtNIC)})
	waitState(t, a, "a", proto.StateRunning, 1)
	first := p.instance("a")
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dvNICs(proto.PowerRunning, 2, mgmtNIC, netNIC)})
	eventually(t, "restarted", func() bool {
		st, _ := a.vmStatus("a")
		return p.instance("a") != first && st.State == proto.StateRunning && st.Generation == 2
	})
	ev := strings.Join(p.gotEvents(), ",")
	if s, r := strings.Index(ev, "stop-guest"), strings.Index(ev, "reseed:a"); s < 0 || r < s {
		t.Fatalf("events = %s", ev)
	}
	time.Sleep(50 * time.Millisecond)
	p.mu.Lock()
	starts := p.starts["a"]
	p.mu.Unlock()
	if starts != 2 {
		t.Fatalf("starts = %d, want 2 (restart policy must not start another)", starts)
	}
	if got := p.gotPrepared(); len(got) != 1 {
		t.Fatalf("prepared = %v, want once (disks untouched)", got)
	}
	if f := loadVM(t, a, "a"); len(f.Spec.NICs) != 2 || f.Spec.NICs[1] != netNIC || f.Generation != 2 {
		t.Fatalf("vm.json = %+v", f)
	}
}

func TestReconcileNICChangeWhileStopped(t *testing.T) {
	a, p := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dvNICs(proto.PowerStopped, 1, mgmtNIC)})
	waitState(t, a, "a", proto.StateStopped, 1)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dvNICs(proto.PowerStopped, 2, mgmtNIC, netNIC)})
	waitState(t, a, "a", proto.StateStopped, 2)
	if !p.hasEvent("reseed:a") || p.instance("a") != nil {
		t.Fatalf("events = %v started = %v", p.gotEvents(), p.instance("a") != nil)
	}
	if f := loadVM(t, a, "a"); len(f.Spec.NICs) != 2 {
		t.Fatalf("vm.json = %+v", f)
	}
}

func TestReconcileReseedFailure(t *testing.T) {
	a, p := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dvNICs(proto.PowerRunning, 1, mgmtNIC)})
	waitState(t, a, "a", proto.StateRunning, 1)
	first := p.instance("a")
	p.mu.Lock()
	p.failReseed = true
	p.mu.Unlock()
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dvNICs(proto.PowerRunning, 2, mgmtNIC, netNIC)})
	waitState(t, a, "a", proto.StateError, 2)
	time.Sleep(50 * time.Millisecond)
	st, _ := a.vmStatus("a")
	if st.State != proto.StateError || !strings.Contains(st.Error, "reseed") || p.instance("a") != first {
		t.Fatalf("status = %+v restarted = %v", st, p.instance("a") != first)
	}
}

func TestReconcileReseedRetriedAfterFailure(t *testing.T) {
	a, p := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dvNICs(proto.PowerRunning, 1, mgmtNIC)})
	waitState(t, a, "a", proto.StateRunning, 1)
	first := p.instance("a")
	p.mu.Lock()
	p.failReseed = true
	p.mu.Unlock()
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dvNICs(proto.PowerStopped, 2, mgmtNIC, netNIC)})
	waitState(t, a, "a", proto.StateError, 2)
	if f := loadVM(t, a, "a"); len(f.Spec.NICs) != 1 {
		t.Fatalf("vm.json after failed reseed = %+v, want old NICs", f)
	}
	p.mu.Lock()
	p.failReseed = false
	p.events = nil
	p.mu.Unlock()
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dvNICs(proto.PowerRunning, 3, mgmtNIC, netNIC)})
	eventually(t, "restarted", func() bool {
		st, _ := a.vmStatus("a")
		return p.instance("a") != first && st.State == proto.StateRunning && st.Generation == 3
	})
	if !p.hasEvent("reseed:a") {
		t.Fatalf("events = %v, want reseed retried", p.gotEvents())
	}
	if f := loadVM(t, a, "a"); len(f.Spec.NICs) != 2 || f.Spec.NICs[1] != netNIC {
		t.Fatalf("vm.json = %+v", f)
	}
}

func TestReconcilePowerOnlyNoReseed(t *testing.T) {
	a, p := newReconcileAgent(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dvNICs(proto.PowerRunning, 1, mgmtNIC)})
	waitState(t, a, "a", proto.StateRunning, 1)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dvNICs(proto.PowerStopped, 2, mgmtNIC)})
	waitState(t, a, "a", proto.StateStopped, 2)
	if p.hasEvent("reseed:a") {
		t.Fatalf("events = %v", p.gotEvents())
	}
}

// storageAddr is a listening host:port standing in for the NBD server.
func storageAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

func att(id, path, addr string) proto.DiskAttachment {
	return proto.DiskAttachment{DiskID: id, MountPath: path, SizeGB: 1, URL: "nbd://" + addr + "/" + proto.StorageFileName(id)}
}

func dvAtts(id string, power proto.Power, gen uint64, atts ...proto.DiskAttachment) proto.DesiredVM {
	d := dv(id, id, power, gen)
	d.Spec.Attachments = atts
	return d
}

func TestReconcileAttachmentChangeRestartsWithReseed(t *testing.T) {
	a, p := newReconcileAgent(t)
	addr := storageAddr(t)
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dvAtts("a", proto.PowerRunning, 1, att("d1", "/data", addr))})
	waitState(t, a, "a", proto.StateRunning, 1)
	first := p.instance("a")
	a.reconcile(context.Background(), 0, []proto.DesiredVM{dvAtts("a", proto.PowerRunning, 2, att("d1", "/data", addr), att("d2", "/logs", addr))})
	eventually(t, "restarted", func() bool {
		st, _ := a.vmStatus("a")
		return p.instance("a") != first && st.State == proto.StateRunning && st.Generation == 2
	})
	ev := strings.Join(p.gotEvents(), ",")
	if s, r := strings.Index(ev, "stop-guest"), strings.Index(ev, "reseed:a"); s < 0 || r < s {
		t.Fatalf("events = %s", ev)
	}
	if got := p.gotPrepared(); len(got) != 1 {
		t.Fatalf("prepared = %v, want once", got)
	}
	if f := loadVM(t, a, "a"); len(f.Spec.Attachments) != 2 || f.Generation != 2 {
		t.Fatalf("vm.json = %+v", f)
	}
}

func captureDisks(a *Agent) func() proto.Message {
	var mu sync.Mutex
	var sent []proto.Message
	a.mu.Lock()
	a.send = func(m proto.Message) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, m)
		return nil
	}
	a.mu.Unlock()
	// Boot goroutines keep sending status updates, so this returns the newest "disks" message.
	return func() proto.Message {
		mu.Lock()
		defer mu.Unlock()
		for i := len(sent) - 1; i >= 0; i-- {
			if sent[i].Type == "disks" {
				return sent[i]
			}
		}
		return proto.Message{}
	}
}

func TestReconcileSendsDisksInUse(t *testing.T) {
	a, _ := newReconcileAgent(t)
	lastDisks := captureDisks(a)
	addr := storageAddr(t)
	a.reconcile(context.Background(), 3, []proto.DesiredVM{
		dvAtts("a", proto.PowerRunning, 1, att("da", "/data", addr)),
		dvAtts("b", proto.PowerRunning, 1, att("db", "/data", addr)),
	})
	if m := lastDisks(); m.Type != "disks" || !slices.Equal(m.DisksInUse, []string{"da", "db"}) || m.AppliedSeq != 3 {
		t.Fatalf("last message = %+v", m)
	}
	a.reconcile(context.Background(), 7, []proto.DesiredVM{dvAtts("b", proto.PowerRunning, 1, att("db", "/data", addr))})
	if m := lastDisks(); m.Type != "disks" || !slices.Equal(m.DisksInUse, []string{"db"}) || m.AppliedSeq != 7 {
		t.Fatalf("last message = %+v", m)
	}
	a.mu.Lock()
	reg := a.registerMsg()
	a.mu.Unlock()
	if reg.AppliedSeq != 7 || !slices.Equal(reg.DisksInUse, []string{"db"}) {
		t.Fatalf("register = %+v", reg)
	}
}

func TestRestoreDefersVMsWithDisks(t *testing.T) {
	a, p := newReconcileAgent(t)
	addr := storageAddr(t)
	keep, drop, newer := cachedVM("k", "keep"), cachedVM("d", "drop"), cachedVM("n", "newer")
	keep.Spec.Attachments = []proto.DiskAttachment{att("dk", "/data", addr)}
	drop.Spec.Attachments = []proto.DiskAttachment{att("dd", "/data", addr)}
	newer.Spec.Attachments = []proto.DiskAttachment{att("dn", "/data", addr)}
	for _, f := range []VMFile{keep, drop, newer, cachedVM("p", "plain")} {
		if err := SaveVM(a.VMsDir, f); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.restoreAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "plain started", func() bool { return p.instance("plain") != nil })
	time.Sleep(50 * time.Millisecond)
	for _, n := range []string{"keep", "drop", "newer"} {
		if p.instance(n) != nil {
			t.Fatalf("%s started before the hub confirmed it", n)
		}
	}
	a.mu.Lock()
	reg := a.registerMsg()
	a.mu.Unlock()
	if !slices.Equal(reg.DisksInUse, []string{"dd", "dk", "dn"}) {
		t.Fatalf("register disks = %v", reg.DisksInUse)
	}
	// The hub detached newer's disk while the node was down; it must boot without it.
	nd := proto.DesiredVM{Spec: newer.Spec, Power: proto.PowerRunning, Generation: 2}
	nd.Spec.Attachments = nil
	kd := proto.DesiredVM{Spec: keep.Spec, Power: proto.PowerRunning, Generation: 1}
	pd := proto.DesiredVM{Spec: cachedVM("p", "plain").Spec, Power: proto.PowerRunning, Generation: 1}
	a.reconcile(context.Background(), 1, []proto.DesiredVM{kd, nd, pd})
	waitState(t, a, "k", proto.StateRunning, 1)
	waitState(t, a, "n", proto.StateRunning, 2)
	if _, err := os.Stat(VMDir(a.VMsDir, "d")); !os.IsNotExist(err) {
		t.Fatalf("drop dir still there: %v", err)
	}
	if p.instance("drop") != nil {
		t.Fatal("drop started")
	}
	p.mu.Lock()
	starts := p.starts["newer"]
	p.mu.Unlock()
	if starts != 1 || len(loadVM(t, a, "n").Spec.Attachments) != 0 || !p.hasEvent("reseed:newer") {
		t.Fatalf("newer starts = %d, vm.json = %+v, events = %v", starts, loadVM(t, a, "n"), p.gotEvents())
	}
}

func TestStartFailsWhenStorageUnreachable(t *testing.T) {
	a, p := newReconcileAgent(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	noEndpoint := att("d2", "/data", "")
	noEndpoint.URL = ""
	a.reconcile(context.Background(), 0, []proto.DesiredVM{
		dvAtts("a", proto.PowerRunning, 1, att("d1", "/data", closed)),
		dvAtts("b", proto.PowerRunning, 1, noEndpoint),
	})
	waitState(t, a, "a", proto.StateError, 1)
	waitState(t, a, "b", proto.StateError, 1)
	if st, _ := a.vmStatus("a"); !strings.Contains(st.Error, "storage unreachable") {
		t.Fatalf("a error = %q", st.Error)
	}
	if st, _ := a.vmStatus("b"); st.Error != "storage not ready: disk d2 has no NBD URL yet" {
		t.Fatalf("b error = %q", st.Error)
	}
	if p.instance("a") != nil || p.instance("b") != nil {
		t.Fatal("started without storage")
	}
}

func TestDestroyKeepsDisksInUseUntilInstanceExits(t *testing.T) {
	old := killWait
	killWait = 50 * time.Millisecond
	t.Cleanup(func() { killWait = old })
	a, p := newReconcileAgent(t)
	lastDisks := captureDisks(a)
	addr := storageAddr(t)
	a.reconcile(context.Background(), 1, []proto.DesiredVM{dvAtts("a", proto.PowerRunning, 1, att("da", "/data", addr))})
	waitState(t, a, "a", proto.StateRunning, 1)
	inst := p.instance("a")
	inst.hang = true
	a.reconcile(context.Background(), 2, nil)
	if m := lastDisks(); m.AppliedSeq != 2 || !slices.Equal(m.DisksInUse, []string{"da"}) {
		t.Fatalf("disks freed while the instance still runs: %+v", m)
	}
	inst.guestShutdown()
	eventually(t, "disk freed after exit", func() bool {
		m := lastDisks()
		return m.AppliedSeq == 2 && len(m.DisksInUse) == 0
	})
}
