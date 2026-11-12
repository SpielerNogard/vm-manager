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
	sizes := map[string]int64{"root.img": 1 << 20, "seed.iso": 4096}
	for name, size := range sizes {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(filepath.Join(dir, name), size); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "root.img"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Truncate(filepath.Join(dir, "root.img"), sizes["root.img"])

	f := node.VMFile{Spec: proto.VMSpec{}}
	got, err := (&Provider{}).Disks(f, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name, file, device string
		ro                 bool
	}{
		{"root", "root.img", "vda", false},
		{"seed", "seed.iso", "vdb", true},
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
	if got[0].AllocatedBytes <= 0 || got[0].AllocatedBytes > got[0].SizeBytes {
		t.Errorf("allocated = %d", got[0].AllocatedBytes)
	}
}
