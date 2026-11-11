package hub

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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

func newTestRegistry(t *testing.T) (*Registry, *fakeNode) {
	t.Helper()
	var cfg Config
	cfg.Underlay.Port = 51820
	r := NewRegistry(newTestNet(t), cfg)
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

func TestCreateVMAllocatesMgmtNIC(t *testing.T) {
	r, n := newTestRegistry(t)
	if _, err := r.CreateVM(validReq()); err != nil {
		t.Fatal(err)
	}
	spec := n.last().Create
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

func TestReRegisterKnownVMKeepsHubNIC(t *testing.T) {
	r, n := newTestRegistry(t)
	vm, _ := r.CreateVM(validReq())
	r.NodeDisconnected("n1", n)
	spec := vm.VMSpec
	spec.NICs = []proto.NIC{{Network: "mgmt", IP: "10.200.0.99", MAC: "02:0a:c8:00:00:63", Port: vm.ID + "-mgmt"}}
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192,
		VMs: []proto.VMRecord{{Spec: spec, Status: proto.VMStatus{ID: vm.ID, State: proto.StateRunning}}}}, &fakeNode{})
	if got := r.VMs()[0].NICs[0].IP; got != "10.200.0.10" {
		t.Fatalf("ip = %s", got)
	}
}

func reportUnknown(r *Registry, id, ip string) {
	mac := ""
	if validMgmtIP(ip) {
		mac = nicMAC(ip)
	}
	reportUnknownNICs(r, id, []proto.NIC{{Network: "mgmt", IP: ip, MAC: mac, Port: id + "-mgmt"}})
}

func reportUnknownNICs(r *Registry, id string, nics []proto.NIC) {
	spec := proto.VMSpec{ID: id, Name: id, NICs: nics}
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192,
		VMs: []proto.VMRecord{{Spec: spec, Status: proto.VMStatus{ID: id, State: proto.StateRunning}}}}, &fakeNode{})
}

func TestAdoptUnknownVMWithTakenIPMarksError(t *testing.T) {
	r, n := newTestRegistry(t)
	vm1, _ := r.CreateVM(validReq())
	spec1 := vm1.VMSpec
	spec2 := proto.VMSpec{ID: "vm2", Name: "vm2", NICs: []proto.NIC{{Network: "mgmt", IP: "10.200.0.10", MAC: nicMAC("10.200.0.10"), Port: "vm2-mgmt"}}}
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192, VMs: []proto.VMRecord{
		{Spec: spec1, Status: proto.VMStatus{ID: vm1.ID, State: proto.StateRunning}},
		{Spec: spec2, Status: proto.VMStatus{ID: "vm2", State: proto.StateRunning}},
	}}, n)
	byName := map[string]VM{}
	for _, v := range r.VMs() {
		byName[v.Name] = v
	}
	if v := byName["vm2"]; v.State != proto.StateError || !strings.Contains(v.Error, "10.200.0.10") {
		t.Fatalf("vm2 = %+v", v)
	}
	if v := byName["dev"]; v.State != proto.StateRunning || v.NICs[0].IP != "10.200.0.10" {
		t.Fatalf("vm1 = %+v", v)
	}
}

func TestAdoptUnknownVMWithInvalidIPMarksError(t *testing.T) {
	for _, ip := range []string{"not-an-ip", "10.200.0.5", "10.201.0.20", "::1", "10.200.255.255"} {
		r, _ := newTestRegistry(t)
		reportUnknown(r, "vm2", ip)
		v := r.VMs()[0]
		if v.State != proto.StateError || !strings.Contains(v.Error, ip) {
			t.Fatalf("ip %s: vm = %+v", ip, v)
		}
		if len(r.net.NICs("vm2")) != 0 {
			t.Fatalf("ip %s: adopted", ip)
		}
	}
}

func TestAdoptUnknownVMWithBadNICMarksError(t *testing.T) {
	good := proto.NIC{Network: "mgmt", IP: "10.200.0.77", MAC: nicMAC("10.200.0.77"), Port: "vm2-mgmt"}
	with := func(f func(n *proto.NIC)) []proto.NIC { n := good; f(&n); return []proto.NIC{n} }
	cases := map[string][]proto.NIC{
		"network":      with(func(n *proto.NIC) { n.Network = "lan" }),
		"foreign port": with(func(n *proto.NIC) { n.Port = "vm1-mgmt" }),
		"bad port":     with(func(n *proto.NIC) { n.Port = "x;y" }),
		"mac":          with(func(n *proto.NIC) { n.MAC = "02:0a:c8:00:00:0a" }),
		"bad mac":      with(func(n *proto.NIC) { n.MAC = "zz" }),
		"two nics":     {good, {Network: "mgmt", IP: "10.200.0.78", MAC: nicMAC("10.200.0.78"), Port: "vm2-mgmt"}},
	}
	for name, nics := range cases {
		r, _ := newTestRegistry(t)
		reportUnknownNICs(r, "vm2", nics)
		if v := r.VMs()[0]; v.State != proto.StateError || v.Error == "" || len(v.NICs) != 0 {
			t.Errorf("%s: vm = %+v", name, v)
		}
		if len(r.net.NICs("vm2")) != 0 {
			t.Errorf("%s: adopted", name)
		}
	}
}

func TestAdoptUnknownVMKeepsValidNIC(t *testing.T) {
	r, _ := newTestRegistry(t)
	reportUnknown(r, "vm2", "10.200.0.77")
	if v := r.VMs()[0]; v.State != proto.StateRunning || v.NICs[0].IP != "10.200.0.77" {
		t.Fatalf("vm = %+v", v)
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

func TestHubRestartKeepsStoredNIC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.json")
	m1, err := LoadNetModel(path)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := m1.AllocNICs("vm1", "vm1")
	if err != nil {
		t.Fatal(err)
	}
	m2, err := LoadNetModel(path)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(m2, Config{})
	spec := proto.VMSpec{ID: "vm1", Name: "vm1", NICs: []proto.NIC{{Network: "mgmt", IP: "10.200.0.99", MAC: nicMAC("10.200.0.99"), Port: "vm1-mgmt"}}}
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192,
		VMs: []proto.VMRecord{{Spec: spec, Status: proto.VMStatus{ID: "vm1", State: proto.StateRunning}}}}, &fakeNode{})
	got := r.VMs()[0].NICs
	if len(got) != 1 || got[0] != stored[0] {
		t.Fatalf("nics = %+v, want %+v", got, stored)
	}
	if n := m2.NICs("vm1"); len(n) != 1 || n[0].IP != "10.200.0.10" {
		t.Fatalf("netmodel = %+v", n)
	}
}

func TestAdoptionErrorSurvivesStatusUpdate(t *testing.T) {
	r, _ := newTestRegistry(t)
	reportUnknown(r, "vm2", "10.200.0.5")
	before := r.VMs()[0]
	r.UpdateStatus(proto.VMStatus{ID: "vm2", State: proto.StateRunning})
	after := r.VMs()[0]
	if after.State != proto.StateError || after.Error != before.Error || after.Error == "" {
		t.Fatalf("after = %+v", after)
	}
}
