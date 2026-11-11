package hub

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"vm-manager/internal/proto"
	"vm-manager/internal/wgkey"
)

type fakeNode struct {
	mu   sync.Mutex
	sent []proto.Message
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
	r := NewRegistry(newTestNet(t), newTestStore(t), cfg)
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
	r2 := NewRegistry(net, store, Config{})
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
	if len(nodes) != 1 || nodes[0].ID != "n1" || nodes[0].Online || nodes[0].FreeCPUs != 1 || nodes[0].Provider != "vz" {
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
	r2 := NewRegistry(newTestNet(t), store, Config{})
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
		"disk size": func(q *CreateVMRequest) {
			q.DataDisks = []proto.DataDisk{{Name: "data", MountPath: "/data", SizeGB: 0}}
		},
		"disk name": func(q *CreateVMRequest) {
			q.DataDisks = []proto.DataDisk{{Name: "Data_1", MountPath: "/data", SizeGB: 1}}
		},
		"disk name len": func(q *CreateVMRequest) {
			q.DataDisks = []proto.DataDisk{{Name: "a234567890123456x", MountPath: "/data", SizeGB: 1}}
		},
		"mount root": func(q *CreateVMRequest) { q.DataDisks = []proto.DataDisk{{Name: "data", MountPath: "/", SizeGB: 1}} },
		"mount rel":  func(q *CreateVMRequest) { q.DataDisks = []proto.DataDisk{{Name: "data", MountPath: "data", SizeGB: 1}} },
		"mount unclean": func(q *CreateVMRequest) {
			q.DataDisks = []proto.DataDisk{{Name: "data", MountPath: "/a/../etc", SizeGB: 1}}
		},
		"mount space": func(q *CreateVMRequest) {
			q.DataDisks = []proto.DataDisk{{Name: "data", MountPath: "/my data", SizeGB: 1}}
		},
		"mount etc": func(q *CreateVMRequest) { q.DataDisks = []proto.DataDisk{{Name: "data", MountPath: "/etc", SizeGB: 1}} },
		"mount usr sub": func(q *CreateVMRequest) {
			q.DataDisks = []proto.DataDisk{{Name: "data", MountPath: "/usr/local", SizeGB: 1}}
		},
		"mount home": func(q *CreateVMRequest) {
			q.DataDisks = []proto.DataDisk{{Name: "data", MountPath: "/home/ubuntu", SizeGB: 1}}
		},
		"mount ssh": func(q *CreateVMRequest) {
			q.DataDisks = []proto.DataDisk{{Name: "data", MountPath: "/home/ubuntu/.ssh", SizeGB: 1}}
		},
		"dup disk name": func(q *CreateVMRequest) {
			q.DataDisks = []proto.DataDisk{{Name: "a", MountPath: "/a", SizeGB: 1}, {Name: "a", MountPath: "/b", SizeGB: 1}}
		},
		"dup mount": func(q *CreateVMRequest) {
			q.DataDisks = []proto.DataDisk{{Name: "a", MountPath: "/a", SizeGB: 1}, {Name: "b", MountPath: "/a", SizeGB: 1}}
		},
		"empty key":    func(q *CreateVMRequest) { q.SSHPublicKeys = nil },
		"unknown node": func(q *CreateVMRequest) { q.NodeID = "nope" },
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
	if r.Nodes()[0].Online {
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

func TestCreateVMAcceptsDataDisks(t *testing.T) {
	r, _ := newTestRegistry(t)
	q := validReq()
	q.DataDisks = []proto.DataDisk{
		{Name: "data", MountPath: "/data", SizeGB: 1},
		{Name: "cache", MountPath: "/var/cache/build", SizeGB: 1},
		{Name: "work", MountPath: "/home/ubuntu/work", SizeGB: 1},
	}
	if _, err := r.CreateVM(q); err != nil {
		t.Fatal(err)
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
