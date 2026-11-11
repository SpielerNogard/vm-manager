package hub

import (
	"errors"
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

func (f *fakeNode) last() proto.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent[len(f.sent)-1]
}

func newTestRegistry(t *testing.T) (*Registry, *fakeNode) {
	t.Helper()
	r := NewRegistry()
	n := &fakeNode{}
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192}, n)
	return r, n
}

func validReq() CreateVMRequest {
	return CreateVMRequest{NodeID: "n1", Name: "dev", CPUs: 2, MemoryMB: 2048, DiskGB: 20, SSHPublicKeys: []string{"ssh-ed25519 AAA"}}
}

func TestCreateVMSendsCreate(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	if vm.State != proto.StatePending || vm.Provider != "vz" {
		t.Fatalf("vm = %+v", vm)
	}
	m := n.last()
	if m.Type != "create" || m.Create == nil || m.Create.ID != vm.ID {
		t.Fatalf("sent = %+v", m)
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
}

func TestDeleteVM(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	if err := r.DeleteVM(vm.ID); err != nil {
		t.Fatal(err)
	}
	if m := n.last(); m.Type != "delete" || m.DeleteID != vm.ID {
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
	r.UpdateStatus(proto.VMStatus{ID: vm.ID, State: proto.StateRunning, IP: "192.168.64.5"})
	got := r.VMs()[0]
	if got.State != proto.StateRunning || got.IP != "192.168.64.5" {
		t.Fatalf("vm = %+v", got)
	}
}

func TestReconnectSameNodeKeepsVMs(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	r.NodeDisconnected("n1", n)
	spec := vm.VMSpec
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192,
		VMs: []proto.VMRecord{{Spec: spec, Status: proto.VMStatus{ID: vm.ID, State: proto.StateRunning}}}}, &fakeNode{})
	vms := r.VMs()
	if len(vms) != 1 || vms[0].State != proto.StateRunning {
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
