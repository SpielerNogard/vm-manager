package vz

import (
	"os"
	"path/filepath"
	"testing"

	"vm-manager/internal/node"
	"vm-manager/internal/proto"
)

func TestDisksFollowAttachOrder(t *testing.T) {
	dir := t.TempDir()
	sizes := map[string]int64{"root.img": 1 << 20, "data-0.img": 2 << 20, "data-1.img": 3 << 20, "seed.iso": 4096}
	for name, size := range sizes {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(filepath.Join(dir, name), size); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "data-0.img"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Truncate(filepath.Join(dir, "data-0.img"), sizes["data-0.img"])

	f := node.VMFile{Spec: proto.VMSpec{DataDisks: []proto.DataDisk{{Name: "data"}, {Name: "cache"}}}}
	got, err := (&Provider{}).Disks(f, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name, file, device string
		ro                 bool
	}{
		{"root", "root.img", "vda", false},
		{"data", "data-0.img", "vdb", false},
		{"cache", "data-1.img", "vdc", false},
		{"seed", "seed.iso", "vdd", true},
	}
	if len(got) != len(want) {
		t.Fatalf("disks = %+v", got)
	}
	for i, w := range want {
		d := got[i]
		if d.Name != w.name || d.File != w.file || d.Device != w.device || d.ReadOnly != w.ro || d.SizeBytes != sizes[w.file] {
			t.Errorf("disk %d = %+v, want %+v", i, d, w)
		}
	}
	if got[1].AllocatedBytes <= 0 || got[1].AllocatedBytes > got[1].SizeBytes {
		t.Errorf("allocated = %d", got[1].AllocatedBytes)
	}
}
