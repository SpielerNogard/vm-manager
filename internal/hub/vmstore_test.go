package hub

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"vm-manager/internal/proto"
)

func testVM(id, node string) StoredVM {
	return StoredVM{
		Spec:   proto.VMSpec{ID: id, Name: "vm-" + id, CPUs: 2, MemoryMB: 512, DiskGB: 5, SSHPublicKeys: []string{"k"}, NICs: []proto.NIC{}},
		NodeID: node, Provider: "vz", Power: proto.PowerRunning, Generation: 3, Provisioned: true,
	}
}

func TestVMStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vms.json")
	s, err := LoadVMStore(path)
	if err != nil {
		t.Fatal(err)
	}
	vms := []StoredVM{testVM("a", "n1"), testVM("b", "n2")}
	vms[1].Power, vms[1].StopMode = proto.PowerStopped, proto.StopHard
	node := StoredNode{ID: "n1", Hostname: "h", Provider: "vz", CPUs: 4, MemoryMB: 8192, Machine: &proto.Machine{OS: "darwin", Arch: "arm64", CPUs: 4}}
	for _, vm := range []StoredVM{vms[1], vms[0]} {
		if err := s.PutVM(vm); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutNode(node); err != nil {
		t.Fatal(err)
	}
	r, err := LoadVMStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.VMs(), vms) {
		t.Fatalf("VMs = %+v, want %+v", r.VMs(), vms)
	}
	if !reflect.DeepEqual(r.Nodes(), []StoredNode{node}) {
		t.Fatalf("Nodes = %+v", r.Nodes())
	}
}

func TestVMStoreDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vms.json")
	s, _ := LoadVMStore(path)
	if err := s.PutVM(testVM("a", "n1")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteVM("a"); err != nil {
		t.Fatal(err)
	}
	r, err := LoadVMStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.VMs()) != 0 {
		t.Fatalf("VMs = %+v", r.VMs())
	}
}

func TestVMStoreMissingFile(t *testing.T) {
	s, err := LoadVMStore(filepath.Join(t.TempDir(), "vms.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.VMs()) != 0 || len(s.Nodes()) != 0 {
		t.Fatal("expected empty store")
	}
}

func TestVMStoreKeepsStateOnSaveError(t *testing.T) {
	dir := t.TempDir()
	s, _ := LoadVMStore(filepath.Join(dir, "vms.json"))
	if err := s.PutVM(testVM("a", "n1")); err != nil {
		t.Fatal(err)
	}
	before := s.VMs()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if err := s.PutVM(testVM("b", "n1")); err == nil {
		t.Skip("directory not read-only for this user (root?)")
	}
	if !reflect.DeepEqual(s.VMs(), before) {
		t.Fatalf("VMs changed: %+v", s.VMs())
	}
}
