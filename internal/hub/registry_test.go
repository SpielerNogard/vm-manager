package hub

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"vm-manager/internal/proto"
	"vm-manager/internal/version"
	"vm-manager/internal/wgkey"
)

type fakeNode struct {
	mu     sync.Mutex
	sent   []proto.Message
	closed bool
}

func (f *fakeNode) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeNode) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeNode) Send(m proto.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeNode) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeNode) last() proto.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent[len(f.sent)-1]
}

func newTestNet(t *testing.T) *NetModel {
	t.Helper()
	m, err := LoadNetModel(filepath.Join(t.TempDir(), "network.json"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func newTestDisks(t *testing.T) *DiskStore {
	t.Helper()
	s, err := LoadDiskStore(filepath.Join(t.TempDir(), "disks.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newTestStore(t *testing.T) *VMStore {
	t.Helper()
	s, err := LoadVMStore(filepath.Join(t.TempDir(), "vms.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newTestRegistry(t *testing.T) (*Registry, *fakeNode) {
	t.Helper()
	var cfg Config
	cfg.Underlay.Port = 51820
	r := NewRegistry(newTestNet(t), newTestStore(t), newTestDisks(t), newTestConfig(t, cfg))
	n := &fakeNode{}
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192}, n)
	return r, n
}

func validReq() CreateVMRequest {
	return CreateVMRequest{NodeID: "n1", Name: "dev", CPUs: 2, MemoryMB: 2048, DiskGB: 20, SSHPublicKeys: []string{"ssh-ed25519 AAA"}}
}

func TestCreateVMSendsDesired(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	if vm.State != proto.StatePending || vm.Provider != "vz" || vm.Power != proto.PowerRunning {
		t.Fatalf("vm = %+v", vm)
	}
	m := n.last()
	if m.Type != "desired" || len(m.Desired) != 1 || m.Desired[0].Spec.ID != vm.ID ||
		m.Desired[0].Generation != 1 || m.Desired[0].Power != proto.PowerRunning || m.Desired[0].Provisioned {
		t.Fatalf("sent = %+v", m)
	}
}

func TestRegisterSendsDesired(t *testing.T) {
	r, _ := newTestRegistry(t)
	n := &fakeNode{}
	r.NodeConnected(proto.Register{NodeID: "n2", Provider: "vz", CPUs: 4, MemoryMB: 8192}, n)
	if m := n.last(); m.Type != "desired" || len(m.Desired) != 0 {
		t.Fatalf("sent = %+v", m)
	}
}

func TestCreateOnOfflineNodeIsPending(t *testing.T) {
	r, n := newTestRegistry(t)
	r.NodeDisconnected("n1", n)
	sent := n.count()
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	if n.count() != sent {
		t.Fatal("sent to offline node")
	}
	if s := r.VMs()[0].State; s != proto.StatePending {
		t.Fatalf("state = %s", s)
	}
	n2 := &fakeNode{}
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192}, n2)
	if m := n2.last(); m.Type != "desired" || len(m.Desired) != 1 || m.Desired[0].Spec.ID != vm.ID {
		t.Fatalf("sent = %+v", m)
	}
}

func TestDeleteOnOfflineNode(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	r.NodeDisconnected("n1", n)
	if err := r.DeleteVM(vm.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.VMs()) != 0 || len(r.store.VMs()) != 0 || len(r.net.NICs(vm.ID)) != 0 {
		t.Fatalf("vms = %+v store = %+v nics = %+v", r.VMs(), r.store.VMs(), r.net.NICs(vm.ID))
	}
}

func TestStaleStatusKeepsPending(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateRunning, Generation: 1})
	if s := r.VMs()[0].State; s != proto.StateRunning {
		t.Fatalf("state = %s", s)
	}
	if _, err := r.bump(vm.ID, func(*VM) {}); err != nil {
		t.Fatal(err)
	}
	if m := n.last(); m.Type != "desired" || m.Desired[0].Generation != 2 {
		t.Fatalf("sent = %+v", m)
	}
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateRunning, Generation: 1})
	if s := r.VMs()[0].State; s != proto.StatePending {
		t.Fatalf("state = %s", s)
	}
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateRunning, Generation: 2})
	if s := r.VMs()[0].State; s != proto.StateRunning {
		t.Fatalf("state = %s", s)
	}
	if _, err := r.bump("nope", func(*VM) {}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestBumpRollsBackOnStoreError(t *testing.T) {
	r, _ := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	// a regular file as parent directory makes saving fail
	r.store.path = filepath.Join(r.net.path, "vms.json")
	if _, err := r.bump(vm.ID, func(v *VM) { v.Power = proto.PowerStopped }); err == nil {
		t.Fatal("bump succeeded")
	}
	if got := r.VMs()[0]; got.Power != proto.PowerRunning || got.generation != 1 {
		t.Fatalf("vm = %+v gen %d", got, got.generation)
	}
}

func TestHubRestartRestoresVMs(t *testing.T) {
	r, _ := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateRunning, Generation: 1})
	q := validReq()
	q.Name, q.CPUs = "fresh", 1
	fresh, _ := r.CreateVM(q)
	net, err := LoadNetModel(r.net.path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := LoadVMStore(r.store.path)
	if err != nil {
		t.Fatal(err)
	}
	r2 := NewRegistry(net, store, newTestDisks(t), newTestConfig(t, Config{}))
	byID := func() map[string]VM {
		out := map[string]VM{}
		for _, v := range r2.VMs() {
			out[v.ID] = v
		}
		return out
	}
	vms := byID()
	// the never-reported VM is still pending; the provisioned one is unknown until its node reports
	if len(vms) != 2 || vms[vm.ID].State != proto.StateUnknown || vms[vm.ID].NICs[0].IP != "10.200.0.10" ||
		vms[vm.ID].Power != proto.PowerRunning || vms[fresh.ID].State != proto.StatePending {
		t.Fatalf("vms = %+v", vms)
	}
	nodes := r2.Nodes()
	if len(nodes) != 1 || nodes[0].ID != "n1" || nodes[0].State != "offline" || nodes[0].FreeCPUs != 1 || nodes[0].Provider != "vz" {
		t.Fatalf("nodes = %+v", nodes)
	}
	n := &fakeNode{}
	r2.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192,
		VMs: []proto.VMStatus{{ID: vm.ID, State: proto.StateRunning, Generation: 1}}}, n)
	if s := byID()[vm.ID].State; s != proto.StateRunning {
		t.Fatalf("state after reconnect = %s", s)
	}
	if m := n.last(); m.Type != "desired" || len(m.Desired) != 2 || !m.Desired[0].Provisioned && !m.Desired[1].Provisioned {
		t.Fatalf("sent = %+v", m)
	}
}

func TestCreateOnStoredOfflineNode(t *testing.T) {
	r, n := newTestRegistry(t)
	r.NodeDisconnected("n1", n)
	store, err := LoadVMStore(r.store.path)
	if err != nil {
		t.Fatal(err)
	}
	r2 := NewRegistry(newTestNet(t), store, newTestDisks(t), newTestConfig(t, Config{}))
	if _, err := r2.CreateVM(validReq()); err != nil {
		t.Fatal(err)
	}
	q := validReq()
	q.Name, q.CPUs = "dev2", 3
	if _, err := r2.CreateVM(q); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownReportedVMIgnored(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192,
		VMs: []proto.VMStatus{{ID: "ghost", State: proto.StateRunning}}}, &fakeNode{})
	if vms := r.VMs(); len(vms) != 0 {
		t.Fatalf("vms = %+v", vms)
	}
}

func TestRegisterRefusedForOtherNodesVM(t *testing.T) {
	r, _ := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	n2 := &fakeNode{}
	err := r.NodeConnected(proto.Register{NodeID: "n2", Provider: "vz", CPUs: 4, MemoryMB: 8192,
		VMs: []proto.VMStatus{{ID: vm.ID, State: proto.StateRunning, Generation: 1}}}, n2)
	if err == nil || !strings.Contains(err.Error(), vm.ID) || !strings.Contains(err.Error(), "n1") {
		t.Fatalf("err = %v", err)
	}
	if n2.count() != 0 {
		t.Fatalf("sent to refused node: %+v", n2.sent)
	}
	for _, n := range r.Nodes() {
		if n.ID == "n2" {
			t.Fatalf("refused node recorded: %+v", n)
		}
	}
	if v := r.VMs()[0]; v.State != proto.StatePending || v.NodeID != "n1" {
		t.Fatalf("vm = %+v", v)
	}
}

func TestStatusFromOtherNodeIgnored(t *testing.T) {
	r, _ := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	r.UpdateStatus("n2", proto.VMStatus{ID: vm.ID, State: proto.StateRunning, IP: "10.0.0.9", Generation: 1})
	if v := r.VMs()[0]; v.State != proto.StatePending || v.IP != "" || r.store.VMs()[0].Provisioned {
		t.Fatalf("vm = %+v", v)
	}
}

func TestLateOlderStatusKeepsApplied(t *testing.T) {
	r, _ := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	if _, err := r.SetPower(vm.ID, proto.PowerStopped, proto.StopGraceful); err != nil {
		t.Fatal(err)
	}
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateStopped, Generation: 2})
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateStopped, Generation: 1})
	if s := r.VMs()[0].State; s != proto.StateStopped {
		t.Fatalf("state = %s", s)
	}
}

func TestStatusMarksProvisioned(t *testing.T) {
	r, _ := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateError, Error: "prepare: boom", Generation: 1})
	if r.store.VMs()[0].Provisioned {
		t.Fatal("provisioned after error")
	}
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateRunning, Generation: 1})
	store, err := LoadVMStore(r.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !store.VMs()[0].Provisioned {
		t.Fatal("not provisioned")
	}
}

func TestCreateVMRejectsInvalid(t *testing.T) {
	cases := map[string]func(*CreateVMRequest){
		"bad name":      func(q *CreateVMRequest) { q.Name = "Dev_1" },
		"name dash end": func(q *CreateVMRequest) { q.Name = "dev-" },
		"cpus":          func(q *CreateVMRequest) { q.CPUs = 0 },
		"memory":        func(q *CreateVMRequest) { q.MemoryMB = 256 },
		"disk":          func(q *CreateVMRequest) { q.DiskGB = 3 },
		"empty key":     func(q *CreateVMRequest) { q.SSHPublicKeys = nil },
		"unknown node":  func(q *CreateVMRequest) { q.NodeID = "nope" },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := newTestRegistry(t)
			q := validReq()
			mod(&q)
			if _, err := r.CreateVM(q); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v", err)
			}
			if len(r.VMs()) != 0 {
				t.Fatal("vm created")
			}
		})
	}
	t.Run("duplicate name", func(t *testing.T) {
		r, _ := newTestRegistry(t)
		q := validReq()
		q.CPUs = 1
		if _, err := r.CreateVM(q); err != nil {
			t.Fatal(err)
		}
		if _, err := r.CreateVM(q); !errors.Is(err, ErrInvalid) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCreateVMCapacity(t *testing.T) {
	r, _ := newTestRegistry(t)
	if _, err := r.CreateVM(validReq()); err != nil {
		t.Fatal(err)
	}
	q := validReq()
	q.Name, q.CPUs = "dev2", 3
	if _, err := r.CreateVM(q); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	if got := r.Nodes()[0].FreeCPUs; got != 2 {
		t.Fatalf("FreeCPUs = %d", got)
	}
}

func TestNodeOffline(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateRunning, Generation: 1})
	r.NodeDisconnected("n1", n)
	if r.Nodes()[0].State != "offline" {
		t.Fatal("node still online")
	}
	if s := r.VMs()[0].State; s != proto.StateUnknown {
		t.Fatalf("state = %s", s)
	}
}

func TestDeleteVM(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	if err := r.DeleteVM(vm.ID); err != nil {
		t.Fatal(err)
	}
	if m := n.last(); m.Type != "desired" || len(m.Desired) != 0 {
		t.Fatalf("sent = %+v", m)
	}
	if len(r.VMs()) != 0 {
		t.Fatalf("vms=%d", len(r.VMs()))
	}
	if err := r.DeleteVM(vm.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateStatus(t *testing.T) {
	r, _ := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateRunning, IP: "192.168.64.5", Generation: 1})
	got := r.VMs()[0]
	if got.State != proto.StateRunning || got.IP != "192.168.64.5" {
		t.Fatalf("vm = %+v", got)
	}
}

func TestReconnectSameNodeKeepsVMs(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	r.NodeDisconnected("n1", n)
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192,
		VMs: []proto.VMStatus{{ID: vm.ID, State: proto.StateRunning, Generation: 1}}}, &fakeNode{})
	vms := r.VMs()
	if len(vms) != 1 || vms[0].State != proto.StateRunning || vms[0].NICs[0].IP != "10.200.0.10" {
		t.Fatalf("vms = %+v", vms)
	}
}

func TestCreateVMAllocatesMgmtNIC(t *testing.T) {
	r, n := newTestRegistry(t)
	if _, err := r.CreateVM(validReq()); err != nil {
		t.Fatal(err)
	}
	spec := n.last().Desired[0].Spec
	if len(spec.NICs) != 1 || spec.NICs[0].IP != "10.200.0.10" || spec.NICs[0].MAC != "02:0a:c8:00:00:0a" {
		t.Fatalf("nics = %+v", spec.NICs)
	}
	b, _ := json.Marshal(spec)
	if strings.Contains(string(b), "sshPort") {
		t.Fatalf("json has sshPort: %s", b)
	}
}

func TestDeleteVMReleasesNIC(t *testing.T) {
	r, _ := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	if err := r.DeleteVM(vm.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.net.NICs(vm.ID); len(got) != 0 {
		t.Fatalf("nics = %+v", got)
	}
}

func TestNodeConnectedEnrollsChassisAndSendsUnderlay(t *testing.T) {
	r, _ := newTestRegistry(t)
	n := &fakeNode{}
	_, pub, _ := wgkey.Generate()
	r.NodeConnected(proto.Register{NodeID: "n2", Provider: "vz", CPUs: 4, MemoryMB: 8192, ChassisKey: pub}, n)
	m := n.last()
	if m.Type != "underlay" || m.Underlay == nil || m.Underlay.Address != "10.198.0.11/24" ||
		m.Underlay.Port != 51820 || m.Underlay.SBRemote != "tcp:10.198.0.1:6642" || m.Underlay.HubKey == "" {
		t.Fatalf("sent = %+v", m)
	}
	if ch := r.net.Chassis(); len(ch) != 1 || ch[0].PublicKey != pub {
		t.Fatalf("chassis = %+v", ch)
	}
}

func TestStatusAndRegisterStoreDisks(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	disks := []proto.Disk{{Name: "root", File: "root.img", Device: "vda", SizeBytes: 10, AllocatedBytes: 4}}
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateRunning, Disks: disks, Generation: 1})
	if got := r.VMs()[0].Disks; len(got) != 1 || got[0].Device != "vda" || got[0].AllocatedBytes != 4 {
		t.Fatalf("after status: %+v", got)
	}
	r.NodeDisconnected("n1", n)
	more := append(disks, proto.Disk{Name: "seed", Device: "vdb", ReadOnly: true})
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192,
		VMs: []proto.VMStatus{{ID: vm.ID, State: proto.StateRunning, Disks: more, Generation: 1}}}, &fakeNode{})
	if got := r.VMs()[0].Disks; len(got) != 2 || !got[1].ReadOnly {
		t.Fatalf("after register: %+v", got)
	}
	b, _ := json.Marshal(r.VMs()[0])
	if !strings.Contains(string(b), `"disks":[{"name":"root"`) {
		t.Fatalf("json = %s", b)
	}
}

type blockingNode struct {
	entered, release chan struct{}
}

func (b *blockingNode) Close() error { return nil }

func (b *blockingNode) Send(proto.Message) error {
	b.entered <- struct{}{}
	<-b.release
	return nil
}

func TestStalledNodeDoesNotBlockOthers(t *testing.T) {
	r, _ := newTestRegistry(t)
	stalled := &blockingNode{entered: make(chan struct{}, 1), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		r.NodeConnected(proto.Register{NodeID: "n2", Provider: "vz", CPUs: 4, MemoryMB: 8192}, stalled)
		close(done)
	}()
	<-stalled.entered
	defer func() { close(stalled.release); <-done }()

	created := make(chan error, 1)
	other := &fakeNode{}
	go func() {
		r.NodeConnected(proto.Register{NodeID: "n3", Provider: "vz", CPUs: 4, MemoryMB: 8192}, other)
		q := validReq()
		q.NodeID = "n3"
		_, err := r.CreateVM(q)
		created <- err
	}()
	select {
	case err := <-created:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CreateVM on n3 blocked by stalled n2")
	}
	if m := other.last(); m.Type != "desired" || len(m.Desired) != 1 {
		t.Fatalf("sent = %+v", m)
	}
}

func TestSetPowerBumpsGeneration(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	got, err := r.SetPower(vm.ID, proto.PowerStopped, proto.StopGraceful)
	if err != nil {
		t.Fatal(err)
	}
	if got.Power != proto.PowerStopped || got.generation != 2 {
		t.Fatalf("vm = %+v", got)
	}
	if m := n.last(); m.Desired[0].Generation != 2 || m.Desired[0].StopMode != proto.StopGraceful {
		t.Fatalf("sent = %+v", m)
	}
	if _, err := r.SetPower("nope", proto.PowerRunning, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestStartResetsProvisionedOnMissingDir(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateRunning, Generation: 1})
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateError, Error: proto.ErrVMDirMissing, Generation: 1})
	if _, err := r.SetPower(vm.ID, proto.PowerRunning, ""); err != nil {
		t.Fatal(err)
	}
	if d := n.last().Desired[0]; d.Provisioned || d.Generation != 2 {
		t.Fatalf("desired = %+v", d)
	}
}

func TestStartOnOfflineNodeResetsProvisionedOnMissingDir(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateRunning, Generation: 1})
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateError, Error: proto.ErrVMDirMissing, Generation: 1})
	r.NodeDisconnected("n1", n)
	if _, err := r.SetPower(vm.ID, proto.PowerRunning, ""); err != nil {
		t.Fatal(err)
	}
	if r.store.VMs()[0].Provisioned {
		t.Fatal("provisioned not reset")
	}
}

func TestStartKeepsProvisionedOnOtherError(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateRunning, Generation: 1})
	r.UpdateStatus("n1", proto.VMStatus{ID: vm.ID, State: proto.StateError, Error: "start boom", Generation: 1})
	if _, err := r.SetPower(vm.ID, proto.PowerRunning, ""); err != nil {
		t.Fatal(err)
	}
	if !n.last().Desired[0].Provisioned {
		t.Fatal("provisioned reset")
	}
}

func addTestNetwork(t *testing.T, r *Registry, name string) Network {
	t.Helper()
	n, err := r.net.AddNetwork(name, "")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateVMWithNetworks(t *testing.T) {
	r, n := newTestRegistry(t)
	a := addTestNetwork(t, r, "a")
	q := validReq()
	q.NetworkIDs = []string{a.ID}
	if _, err := r.CreateVM(q); err != nil {
		t.Fatal(err)
	}
	if nics := n.last().Desired[0].Spec.NICs; len(nics) != 2 || nics[0].Network != "mgmt" || nics[1].Network != a.ID {
		t.Fatalf("nics = %+v", nics)
	}
}

func TestSetVMNetworksNeedsRestartFlag(t *testing.T) {
	r, n := newTestRegistry(t)
	a := addTestNetwork(t, r, "a")
	vm, _ := r.CreateVM(validReq())
	if _, err := r.SetVMNetworks(vm.ID, []string{a.ID}, false); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "restartAllowed=true") {
		t.Fatalf("err = %v", err)
	}
	if got := r.VMs()[0]; got.generation != 1 || len(got.NICs) != 1 {
		t.Fatalf("vm = %+v gen %d", got, got.generation)
	}
	got, err := r.SetVMNetworks(vm.ID, []string{a.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.generation != 2 || len(got.NICs) != 2 || got.NICs[1].Network != a.ID {
		t.Fatalf("vm = %+v gen %d", got, got.generation)
	}
	if d := n.last().Desired[0]; d.Generation != 2 || len(d.Spec.NICs) != 2 || d.Spec.NICs[1].Network != a.ID {
		t.Fatalf("desired = %+v", d)
	}
	if _, err := r.SetVMNetworks("nope", nil, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetVMNetworksStoppedNoFlag(t *testing.T) {
	r, _ := newTestRegistry(t)
	a := addTestNetwork(t, r, "a")
	vm, _ := r.CreateVM(validReq())
	if _, err := r.SetPower(vm.ID, proto.PowerStopped, proto.StopGraceful); err != nil {
		t.Fatal(err)
	}
	got, err := r.SetVMNetworks(vm.ID, []string{a.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Power != proto.PowerStopped || len(got.NICs) != 2 {
		t.Fatalf("vm = %+v", got)
	}
}

func TestSetVMNetworksSameListNoop(t *testing.T) {
	r, n := newTestRegistry(t)
	a, b := addTestNetwork(t, r, "a"), addTestNetwork(t, r, "b")
	vm, _ := r.CreateVM(validReq())
	set, err := r.SetVMNetworks(vm.ID, []string{a.ID, b.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	sent := n.count()
	got, err := r.SetVMNetworks(vm.ID, []string{b.ID, a.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.generation != set.generation || n.count() != sent {
		t.Fatalf("gen %d -> %d, sent %d -> %d", set.generation, got.generation, sent, n.count())
	}
}

func TestSetVMNetworksRestoresOnStoreError(t *testing.T) {
	r, _ := newTestRegistry(t)
	a := addTestNetwork(t, r, "a")
	vm, _ := r.CreateVM(validReq())
	r.store.path = filepath.Join(r.net.path, "vms.json")
	if _, err := r.SetVMNetworks(vm.ID, []string{a.ID}, true); err == nil {
		t.Fatal("SetVMNetworks succeeded")
	}
	if nics := r.net.NICs(vm.ID); len(nics) != 1 {
		t.Fatalf("net NICs = %+v", nics)
	}
	if got := r.VMs()[0]; got.generation != 1 || len(got.NICs) != 1 {
		t.Fatalf("vm = %+v gen %d", got, got.generation)
	}
}

func TestDeleteNetworkAfterDetachOffline(t *testing.T) {
	r, n := newTestRegistry(t)
	a := addTestNetwork(t, r, "a")
	q := validReq()
	q.NetworkIDs = []string{a.ID}
	vm, _ := r.CreateVM(q)
	if _, err := r.SetPower(vm.ID, proto.PowerStopped, proto.StopGraceful); err != nil {
		t.Fatal(err)
	}
	r.NodeDisconnected("n1", n)
	if err := r.net.DeleteNetwork(a.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete in use: err = %v", err)
	}
	if _, err := r.SetVMNetworks(vm.ID, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := r.net.DeleteNetwork(a.ID); err != nil {
		t.Fatal(err)
	}
}

func TestNewRegistryReleasesOrphanNICs(t *testing.T) {
	nm := newTestNet(t)
	if _, err := nm.AllocNICs("ghost", "x", nil); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(nm, newTestStore(t), newTestDisks(t), newTestConfig(t, Config{}))
	if len(nm.NICs("ghost")) != 0 {
		t.Fatalf("NICs = %+v", nm.NICs("ghost"))
	}
	if _, ok := nm.Names()["ghost"]; ok {
		t.Fatal("name kept")
	}
	for _, p := range r.GatewayState().Ports {
		if p.Name == "ghost-mgmt" {
			t.Fatalf("orphan port %+v", p)
		}
	}
}

func nodeByID(r *Registry, id string) (Node, bool) {
	for _, n := range r.Nodes() {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

func TestCreateNodePending(t *testing.T) {
	r, _ := newTestRegistry(t)
	if _, err := r.CreateNode("mac-x", "vz"); err != nil {
		t.Fatal(err)
	}
	if n, ok := nodeByID(r, "mac-x"); !ok || n.State != "pending" {
		t.Fatalf("node = %+v ok = %v", n, ok)
	}
	if _, err := r.CreateNode("connect", "vz"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("connect err = %v", err)
	}
	if _, err := r.CreateNode("Mac X", "vz"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.CreateNode("mac-x", "vz"); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	store, err := LoadVMStore(r.store.path)
	if err != nil {
		t.Fatal(err)
	}
	r2 := NewRegistry(newTestNet(t), store, newTestDisks(t), newTestConfig(t, Config{}))
	if n, _ := nodeByID(r2, "mac-x"); n.State != "pending" {
		t.Fatalf("after restart: %+v", n)
	}
	if n, _ := nodeByID(r2, "n1"); n.State != "offline" {
		t.Fatalf("after restart: %+v", n)
	}
}

func TestPendingNodeGoesOnline(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.CreateNode("mac-x", "vz")
	c := &fakeNode{}
	r.NodeConnected(proto.Register{NodeID: "mac-x", Provider: "vz", CPUs: 4, MemoryMB: 8192}, c)
	if len(r.Nodes()) != 2 {
		t.Fatalf("nodes = %+v", r.Nodes())
	}
	if n, _ := nodeByID(r, "mac-x"); n.State != "online" || n.CPUs != 4 {
		t.Fatalf("node = %+v", n)
	}
	r.NodeDisconnected("mac-x", c)
	if n, _ := nodeByID(r, "mac-x"); n.State != "offline" {
		t.Fatalf("node = %+v", n)
	}
	store, _ := LoadVMStore(r.store.path)
	if n, _ := nodeByID(NewRegistry(newTestNet(t), store, newTestDisks(t), newTestConfig(t, Config{})), "mac-x"); n.State != "offline" {
		t.Fatalf("after restart: %+v", n)
	}
}

func TestSelfRegistrationStillWorks(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.NodeConnected(proto.Register{NodeID: "Some.Host", Provider: "vz", CPUs: 4, MemoryMB: 8192}, &fakeNode{})
	if n, ok := nodeByID(r, "Some.Host"); !ok || n.State != "online" {
		t.Fatalf("node = %+v", n)
	}
}

func TestDeleteNode(t *testing.T) {
	r, _ := newTestRegistry(t)
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteNode("n1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	r.DeleteVM(vm.ID)
	if err := r.DeleteNode("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}

	c := &fakeNode{}
	_, pub, _ := wgkey.Generate()
	r.NodeConnected(proto.Register{NodeID: "n2", Provider: "vz", CPUs: 4, MemoryMB: 8192, ChassisKey: pub}, c)
	if err := r.DeleteNode("n2"); err != nil {
		t.Fatal(err)
	}
	if !c.isClosed() {
		t.Fatal("connection not closed")
	}
	if _, ok := nodeByID(r, "n2"); ok {
		t.Fatal("node still listed")
	}
	if ch := r.GatewayState().Chassis; len(ch) != 0 {
		t.Fatalf("chassis = %+v", ch)
	}
	r.NodeDisconnected("n2", c)
	if _, ok := nodeByID(r, "n2"); ok {
		t.Fatal("disconnect resurrected the node")
	}
	if err := r.DeleteNode("n1"); err != nil {
		t.Fatal(err)
	}
	store, _ := LoadVMStore(r.store.path)
	if nodes := NewRegistry(newTestNet(t), store, newTestDisks(t), newTestConfig(t, Config{})).Nodes(); len(nodes) != 0 {
		t.Fatalf("after restart: %+v", nodes)
	}
}

func TestCreateVMOnPendingNode(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.CreateNode("mac-x", "vz")
	q := validReq()
	q.NodeID = "mac-x"
	if _, err := r.CreateVM(q); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestNodeConfig(t *testing.T) {
	var c Config
	c.Token, c.Hub.Port, c.Clients.Endpoint = "tok-1234567890abcdef", 8080, "100.114.108.108"
	cfg := newTestConfig(t, c)
	r := NewRegistry(newTestNet(t), newTestStore(t), newTestDisks(t), cfg)
	r.CreateNode("mac-x", "vz")
	parse := func(host string) map[string]any {
		t.Helper()
		b, err := r.NodeConfig("mac-x", host)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := parse("")
	hub := got["hub"].(map[string]any)
	paths := got["paths"].(map[string]any)
	if got["nodeId"] != "mac-x" || hub["url"] != "ws://100.114.108.108:8080/api/nodes/connect" || hub["token"] != c.Token ||
		got["provider"] != "vz" || paths["vms"] != "~/.vm-manager/vms" || paths["images"] != "~/.vm-manager/images" {
		t.Fatalf("config = %v", got)
	}
	cfg.Update(func(c *Config) error { c.Token = "tok-new-0123456789"; return nil })
	got = parse("localhost")
	if hub := got["hub"].(map[string]any); hub["url"] != "ws://localhost:8080/api/nodes/connect" || hub["token"] != "tok-new-0123456789" {
		t.Fatalf("config = %v", got)
	}
	if hub := parse("fd00::1")["hub"].(map[string]any); hub["url"] != "ws://[fd00::1]:8080/api/nodes/connect" {
		t.Fatalf("url = %v", hub["url"])
	}
	if _, err := r.NodeConfig("mac-x", "http://x:1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.NodeConfig("nope", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestNodeConfigWithoutEndpointConflicts(t *testing.T) {
	r := NewRegistry(newTestNet(t), newTestStore(t), newTestDisks(t), newTestConfig(t, Config{Token: "t"}))
	r.CreateNode("mac-x", "vz")
	if _, err := r.NodeConfig("mac-x", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteNodeStoreFailureKeepsNodeAndChassis(t *testing.T) {
	r, _ := newTestRegistry(t)
	if _, err := r.net.Enroll("n1", "k"); err != nil {
		t.Fatal(err)
	}
	r.store.path = brokenStorePath(t)
	if err := r.DeleteNode("n1"); err == nil {
		t.Fatal("want error")
	}
	if _, ok := nodeByID(r, "n1"); !ok || len(r.net.Chassis()) != 1 {
		t.Errorf("node or chassis lost: chassis = %v", r.net.Chassis())
	}
}

func TestPendingNodeStoreFailureRefusesRegistration(t *testing.T) {
	r := NewRegistry(newTestNet(t), newTestStore(t), newTestDisks(t), newTestConfig(t, Config{}))
	r.CreateNode("mac-x", "vz")
	good := r.store.path
	r.store.path = brokenStorePath(t)
	if err := r.NodeConnected(proto.Register{NodeID: "mac-x"}, &fakeNode{}); err == nil {
		t.Fatal("want error")
	}
	if n, _ := nodeByID(r, "mac-x"); n.State != NodePending {
		t.Errorf("state = %s", n.State)
	}
	r.store.path = good
	if err := r.NodeConnected(proto.Register{NodeID: "mac-x"}, &fakeNode{}); err != nil {
		t.Fatal(err)
	}
	if n, _ := nodeByID(r, "mac-x"); n.State != NodeOnline || r.store.Nodes()[0].Pending {
		t.Errorf("node = %+v stored = %+v", n, r.store.Nodes())
	}
}

func brokenStorePath(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(f, "vms.json")
}

func TestNewRegistryReleasesOrphanChassis(t *testing.T) {
	nm := newTestNet(t)
	for _, id := range []string{"gone", "kept"} {
		if _, err := nm.Enroll(id, "k-"+id); err != nil {
			t.Fatal(err)
		}
	}
	store := newTestStore(t)
	if err := store.PutNode(StoredNode{ID: "kept"}); err != nil {
		t.Fatal(err)
	}
	NewRegistry(nm, store, newTestDisks(t), newTestConfig(t, Config{}))
	if ch := nm.Chassis(); len(ch) != 1 || ch[0].NodeID != "kept" {
		t.Errorf("chassis = %+v", ch)
	}
}

func TestCreateNodeRequiresProvider(t *testing.T) {
	r, _ := newTestRegistry(t)
	for _, p := range []string{"", "qemu"} {
		if _, err := r.CreateNode("fc-x", p); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "cloud-hypervisor") {
			t.Fatalf("provider %q: err = %v", p, err)
		}
	}
	n, err := r.CreateNode("fc-x", "firecracker")
	if err != nil || n.Provider != "firecracker" {
		t.Fatalf("node = %+v, %v", n, err)
	}
	if n, err := r.CreateNode("ch-x", "cloud-hypervisor"); err != nil || n.Provider != "cloud-hypervisor" {
		t.Fatalf("node = %+v, %v", n, err)
	}
	store, err := LoadVMStore(r.store.path)
	if err != nil {
		t.Fatal(err)
	}
	r2 := NewRegistry(newTestNet(t), store, newTestDisks(t), newTestConfig(t, Config{}))
	if n, _ := nodeByID(r2, "fc-x"); n.Provider != "firecracker" {
		t.Fatalf("after restart: %+v", n)
	}
}

func TestNodeRegistersWithOtherProvider(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.CreateNode("n2", "vz")
	if err := r.NodeConnected(proto.Register{NodeID: "n2", Provider: "firecracker", CPUs: 2, MemoryMB: 2048}, &fakeNode{}); err != nil {
		t.Fatal(err)
	}
	n, _ := nodeByID(r, "n2")
	if n.State != NodeOnline || n.Provider != "firecracker" || n.Error != "created for vz, registered with firecracker" {
		t.Fatalf("node = %+v", n)
	}
}

func TestInstallCommand(t *testing.T) {
	var c Config
	c.Token, c.Hub.Port, c.Clients.Endpoint = "tok", 8080, "vmm.lan"
	r := NewRegistry(newTestNet(t), newTestStore(t), newTestDisks(t), newTestConfig(t, c))
	r.CreateNode("n2", "firecracker")
	got, err := r.InstallCommand("n2", "")
	want := "curl -fsSL http://vmm.lan:8080/node/install.sh | sh -s -- --hub http://vmm.lan:8080 --token tok --name n2 --provider firecracker"
	if err != nil || got != want {
		t.Fatalf("got %q, %v\nwant %q", got, err, want)
	}
	if _, err := r.InstallCommand("nope", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.InstallCommand("n2", "http://x:1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestNodeConfigLinuxPaths(t *testing.T) {
	for _, provider := range []string{"firecracker", "cloud-hypervisor"} {
		var c Config
		c.Token, c.Hub.Port, c.Clients.Endpoint = "tok", 8080, "vmm.lan"
		r := NewRegistry(newTestNet(t), newTestStore(t), newTestDisks(t), newTestConfig(t, c))
		r.CreateNode("n2", provider)
		b, err := r.NodeConfig("n2", "")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Provider string
			Paths    struct{ VMs, Images string }
		}
		json.Unmarshal(b, &got)
		if got.Provider != provider || got.Paths.VMs != "/var/lib/vm-manager/vms" || got.Paths.Images != "/var/lib/vm-manager/images" {
			t.Fatalf("config = %s", b)
		}
	}
}

func TestInstallCommandQuotesToken(t *testing.T) {
	var c Config
	c.Token, c.Hub.Port, c.Clients.Endpoint = "abc&def'0123456789", 8080, "vmm.lan"
	r := NewRegistry(newTestNet(t), newTestStore(t), newTestDisks(t), newTestConfig(t, c))
	r.CreateNode("n2", "firecracker")
	got, err := r.InstallCommand("n2", "")
	// Settings accept any token without whitespace, so it must survive being pasted into a shell.
	if err != nil || !strings.Contains(got, `--token 'abc&def'\''0123456789' --name n2`) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func setHubVersion(t *testing.T, v string) {
	t.Helper()
	old := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = old })
}

func TestNodeVersion(t *testing.T) {
	setHubVersion(t, "v0.5.0")
	var cfg Config
	store := newTestStore(t)
	r := NewRegistry(newTestNet(t), store, newTestDisks(t), newTestConfig(t, cfg))
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", Version: "v0.4.0", Updatable: true}, &fakeNode{})
	r.NodeConnected(proto.Register{NodeID: "n2", Provider: "vz"}, &fakeNode{})
	r.NodeConnected(proto.Register{NodeID: "n3", Provider: "vz", Version: "v0.5.0"}, &fakeNode{})
	if n, _ := nodeByID(r, "n1"); n.Version != "v0.4.0" || !n.UpdateAvailable || !n.Updatable {
		t.Errorf("n1 = %+v", n)
	}
	if n, _ := nodeByID(r, "n2"); n.Version != "" || n.UpdateAvailable {
		t.Errorf("n2 = %+v", n)
	}
	if n, _ := nodeByID(r, "n3"); n.UpdateAvailable {
		t.Errorf("n3 = %+v", n)
	}
	r2 := NewRegistry(newTestNet(t), store, newTestDisks(t), newTestConfig(t, cfg))
	if n, _ := nodeByID(r2, "n1"); n.State != NodeOffline || n.Version != "v0.4.0" {
		t.Errorf("after restart n1 = %+v", n)
	}
}
