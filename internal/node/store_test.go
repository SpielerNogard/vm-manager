package node

import (
	"os"
	"path/filepath"
	"testing"

	"vm-manager/internal/proto"
)

func TestSaveLoadDelete(t *testing.T) {
	dir := t.TempDir()
	a := VMFile{Spec: proto.VMSpec{ID: "a", Name: "a", CPUs: 1, NICs: []proto.NIC{{Network: "mgmt", IP: "10.200.0.10"}}}, MAC: "02:00:00:00:00:01"}
	b := VMFile{Spec: proto.VMSpec{ID: "b", Name: "b", CPUs: 2}, MAC: "02:00:00:00:00:02"}
	for _, f := range []VMFile{a, b} {
		if err := SaveVM(dir, f); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "stray"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := LoadVMs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Spec.ID != "a" || got[0].MAC != a.MAC || len(got[0].Spec.NICs) != 1 || got[0].Spec.NICs[0].IP != "10.200.0.10" || got[1].Spec.ID != "b" {
		t.Fatalf("loaded = %+v", got)
	}

	if err := DeleteVMDir(dir, "a"); err != nil {
		t.Fatal(err)
	}
	got, _ = LoadVMs(dir)
	if len(got) != 1 || got[0].Spec.ID != "b" {
		t.Fatalf("after delete = %+v", got)
	}
	if _, err := os.Stat(VMDir(dir, "a")); !os.IsNotExist(err) {
		t.Fatalf("dir still exists: %v", err)
	}
}

func TestLoadVMsMissingDir(t *testing.T) {
	got, err := LoadVMs(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v err %v", got, err)
	}
}
