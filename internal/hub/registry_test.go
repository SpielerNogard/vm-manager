package hub

import (
	"errors"
	"net"
	"sync"
	"testing"

	"vm-manager/internal/proto"
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

func (f *fakeNode) OpenStream() (net.Conn, error) { return nil, errors.New("no streams") }

func (f *fakeNode) last() proto.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent[len(f.sent)-1]
}

func newTestRegistry(t *testing.T, min, max int) (*Registry, *fakeNode, *[]int) {
	t.Helper()
	r := NewRegistry(min, max)
	var opened []int
	r.OnPortOpen = func(p int) error { opened = append(opened, p); return nil }
	r.OnPortClose = func(int) {}
	n := &fakeNode{}
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192}, n)
	return r, n, &opened
}

func validReq() CreateVMRequest {
	return CreateVMRequest{NodeID: "n1", Name: "dev", CPUs: 2, MemoryMB: 2048, DiskGB: 20, SSHPublicKey: "ssh-ed25519 AAA"}
}

func TestCreateVMSendsCreateAndAllocatesPort(t *testing.T) {
	r, n, opened := newTestRegistry(t, 22000, 22999)
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	if vm.SSHPort != 22000 || vm.State != proto.StatePending || vm.Provider != "vz" {
		t.Fatalf("vm = %+v", vm)
	}
	m := n.last()
	if m.Type != "create" || m.Create == nil || m.Create.SSHPort != 22000 || m.Create.ID != vm.ID {
		t.Fatalf("sent = %+v", m)
	}
	if len(*opened) != 1 || (*opened)[0] != 22000 {
		t.Fatalf("opened = %v", *opened)
	}
}

func TestCreateVMRejectsInvalid(t *testing.T) {
	cases := map[string]func(*CreateVMRequest){
		"bad name":      func(q *CreateVMRequest) { q.Name = "Dev_1" },
		"name dash end": func(q *CreateVMRequest) { q.Name = "dev-" },
		"cpus":          func(q *CreateVMRequest) { q.CPUs = 0 },
		"memory":        func(q *CreateVMRequest) { q.MemoryMB = 256 },
		"disk":          func(q *CreateVMRequest) { q.DiskGB = 3 },
		"data disk":     func(q *CreateVMRequest) { q.DataDisksGB = []int{0} },
		"empty key":     func(q *CreateVMRequest) { q.SSHPublicKey = " " },
		"unknown node":  func(q *CreateVMRequest) { q.NodeID = "nope" },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			r, _, _ := newTestRegistry(t, 22000, 22999)
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
		r, _, _ := newTestRegistry(t, 22000, 22999)
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
	r, _, _ := newTestRegistry(t, 22000, 22999)
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
	r, n, _ := newTestRegistry(t, 22000, 22999)
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	r.NodeDisconnected("n1", n)
	if r.Nodes()[0].Online {
		t.Fatal("node still online")
	}
	if s := r.VMs()[0].State; s != proto.StateUnknown {
		t.Fatalf("state = %s", s)
	}
	q := validReq()
	q.Name = "dev2"
	if _, err := r.CreateVM(q); !errors.Is(err, ErrConflict) {
		t.Fatalf("create err = %v", err)
	}
	if err := r.DeleteVM(vm.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete err = %v", err)
	}
	if _, err := r.StreamForPort(vm.SSHPort); err == nil {
		t.Fatal("stream on offline node")
	}
}

func TestDeleteVM(t *testing.T) {
	r, n, _ := newTestRegistry(t, 22000, 22999)
	closed := 0
	r.OnPortClose = func(int) { closed++ }
	vm, _ := r.CreateVM(validReq())
	if err := r.DeleteVM(vm.ID); err != nil {
		t.Fatal(err)
	}
	if m := n.last(); m.Type != "delete" || m.DeleteID != vm.ID {
		t.Fatalf("sent = %+v", m)
	}
	if len(r.VMs()) != 0 || closed != 1 {
		t.Fatalf("vms=%d closed=%d", len(r.VMs()), closed)
	}
	if err := r.DeleteVM(vm.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateStatus(t *testing.T) {
	r, _, _ := newTestRegistry(t, 22000, 22999)
	vm, _ := r.CreateVM(validReq())
	r.UpdateStatus(proto.VMStatus{ID: vm.ID, State: proto.StateRunning, IP: "192.168.64.5"})
	got := r.VMs()[0]
	if got.State != proto.StateRunning || got.IP != "192.168.64.5" {
		t.Fatalf("vm = %+v", got)
	}
}

func TestReRegisterKeepsPort(t *testing.T) {
	r := NewRegistry(22000, 22999)
	r.OnPortOpen = func(int) error { return nil }
	rec := func(id string, port int) proto.VMRecord {
		return proto.VMRecord{
			Spec:   proto.VMSpec{ID: id, Name: id, CPUs: 1, MemoryMB: 1024, DiskGB: 10, SSHPublicKey: "k", SSHPort: port},
			Status: proto.VMStatus{ID: id, State: proto.StateRunning, IP: "192.168.64.2"},
		}
	}
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192, VMs: []proto.VMRecord{rec("a", 22005)}}, &fakeNode{})
	r.NodeConnected(proto.Register{NodeID: "n2", Provider: "vz", CPUs: 4, MemoryMB: 8192, VMs: []proto.VMRecord{rec("b", 22005), rec("c", 30000)}}, &fakeNode{})
	ports := map[string]int{}
	for _, vm := range r.VMs() {
		ports[vm.ID] = vm.SSHPort
		if vm.State != proto.StateRunning {
			t.Fatalf("%s state = %s", vm.ID, vm.State)
		}
	}
	if ports["a"] != 22005 {
		t.Fatalf("a port = %d", ports["a"])
	}
	if ports["b"] == 22005 || ports["b"] < 22000 || ports["b"] > 22999 {
		t.Fatalf("b port = %d", ports["b"])
	}
	if ports["c"] < 22000 || ports["c"] > 22999 || ports["c"] == ports["b"] {
		t.Fatalf("c port = %d", ports["c"])
	}
}

func TestReconnectSameNodeKeepsPorts(t *testing.T) {
	r, n, _ := newTestRegistry(t, 22000, 22999)
	vm, _ := r.CreateVM(validReq())
	r.NodeDisconnected("n1", n)
	spec := vm.VMSpec
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192,
		VMs: []proto.VMRecord{{Spec: spec, Status: proto.VMStatus{ID: vm.ID, State: proto.StateRunning}}}}, &fakeNode{})
	vms := r.VMs()
	if len(vms) != 1 || vms[0].SSHPort != vm.SSHPort || vms[0].State != proto.StateRunning {
		t.Fatalf("vms = %+v", vms)
	}
}

func TestPortRangeExhausted(t *testing.T) {
	r, _, _ := newTestRegistry(t, 22000, 22000)
	q := validReq()
	q.CPUs = 1
	if _, err := r.CreateVM(q); err != nil {
		t.Fatal(err)
	}
	q.Name = "dev2"
	if _, err := r.CreateVM(q); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
}
