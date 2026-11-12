package storage

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"vm-manager/internal/proto"
)

func stat(t *testing.T, path string) (size, alloc int64) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size(), st.Sys().(*syscall.Stat_t).Blocks * 512
}

func apply(t *testing.T, d Dir, disks []proto.StorageDisk, deleted []string) {
	t.Helper()
	if err := d.Apply(disks, deleted); err != nil {
		t.Fatal(err)
	}
}

func TestApplyCreatesSparse(t *testing.T) {
	d := Dir{t.TempDir()}
	apply(t, d, []proto.StorageDisk{{ID: "a", SizeBytes: 1 << 30, Create: true}}, nil)
	size, alloc := stat(t, filepath.Join(d.Path, "a.img"))
	if size != 1<<30 || alloc >= 1<<20 {
		t.Fatalf("size %d alloc %d", size, alloc)
	}
}

func TestApplyGrowsKeepsData(t *testing.T) {
	d := Dir{t.TempDir()}
	apply(t, d, []proto.StorageDisk{{ID: "a", SizeBytes: 1 << 30, Create: true}}, nil)
	p := filepath.Join(d.Path, "a.img")
	f, _ := os.OpenFile(p, os.O_WRONLY, 0)
	f.WriteAt([]byte("hello"), 0)
	f.Close()
	apply(t, d, []proto.StorageDisk{{ID: "a", SizeBytes: 2 << 30}}, nil)
	size, _ := stat(t, p)
	b := make([]byte, 5)
	f, _ = os.Open(p)
	defer f.Close()
	f.ReadAt(b, 0)
	if size != 2<<30 || string(b) != "hello" {
		t.Fatalf("size %d data %q", size, b)
	}
}

func TestApplyNeverShrinks(t *testing.T) {
	d := Dir{t.TempDir()}
	apply(t, d, []proto.StorageDisk{{ID: "a", SizeBytes: 2 << 30, Create: true}}, nil)
	apply(t, d, []proto.StorageDisk{{ID: "a", SizeBytes: 1 << 30, Create: true}}, nil)
	if size, _ := stat(t, filepath.Join(d.Path, "a.img")); size != 2<<30 {
		t.Fatalf("size %d", size)
	}
}

func TestApplyCopies(t *testing.T) {
	d := Dir{t.TempDir()}
	apply(t, d, []proto.StorageDisk{{ID: "a", SizeBytes: 1 << 30, Create: true}}, nil)
	f, _ := os.OpenFile(filepath.Join(d.Path, "a.img"), os.O_WRONLY, 0)
	f.WriteAt([]byte("start"), 0)
	f.WriteAt([]byte("middle"), 512<<20)
	f.Close()
	apply(t, d, []proto.StorageDisk{{ID: "a", SizeBytes: 1 << 30, Create: true}, {ID: "b", SizeBytes: 1 << 30, CopyFrom: "a", Create: true}}, nil)
	p := filepath.Join(d.Path, "b.img")
	f, _ = os.Open(p)
	defer f.Close()
	b1, b2 := make([]byte, 5), make([]byte, 6)
	f.ReadAt(b1, 0)
	f.ReadAt(b2, 512<<20)
	size, alloc := stat(t, p)
	if string(b1) != "start" || string(b2) != "middle" || size != 1<<30 || alloc >= 4<<20 {
		t.Fatalf("%q %q size %d alloc %d", b1, b2, size, alloc)
	}
}

func TestApplyCopyMissingSourceSkips(t *testing.T) {
	d := Dir{t.TempDir()}
	b := proto.StorageDisk{ID: "b", SizeBytes: 1 << 20, CopyFrom: "a", Create: true}
	apply(t, d, []proto.StorageDisk{b}, nil)
	if _, err := os.Stat(filepath.Join(d.Path, "b.img")); !os.IsNotExist(err) {
		t.Fatalf("b.img exists: %v", err)
	}
	apply(t, d, []proto.StorageDisk{{ID: "a", SizeBytes: 1 << 20, Create: true}}, nil)
	apply(t, d, []proto.StorageDisk{b}, nil)
	if _, err := os.Stat(filepath.Join(d.Path, "b.img")); err != nil {
		t.Fatal(err)
	}
}

func TestApplyDeletesOnlyTombstones(t *testing.T) {
	d := Dir{t.TempDir()}
	for _, n := range []string{"a.img", "b.img", "x.img"} {
		os.WriteFile(filepath.Join(d.Path, n), nil, 0o644)
	}
	apply(t, d, []proto.StorageDisk{{ID: "a", SizeBytes: 0, Create: true}}, []string{"b"})
	files, _ := d.Scan()
	if len(files) != 2 || files[0].Name != "a.img" || files[1].Name != "x.img" {
		t.Fatalf("%+v", files)
	}
}

func TestScanIgnoresPartial(t *testing.T) {
	d := Dir{t.TempDir()}
	os.MkdirAll(filepath.Join(d.Path, ".partial"), 0o755)
	os.WriteFile(filepath.Join(d.Path, ".partial", "p.img"), nil, 0o644)
	if files, _ := d.Scan(); len(files) != 0 {
		t.Fatalf("%+v", files)
	}
}

func TestApplyRejectsPathIDs(t *testing.T) {
	root := t.TempDir()
	d := Dir{filepath.Join(root, "disks")}
	os.MkdirAll(d.Path, 0o755)
	os.WriteFile(filepath.Join(root, "victim.img"), nil, 0o644)
	os.WriteFile(filepath.Join(d.Path, "ok.img"), []byte("data"), 0o644)
	bad := []string{"", ".", "..", "../victim", "sub/x", `sub\x`}
	var disks []proto.StorageDisk
	for _, id := range bad {
		disks = append(disks, proto.StorageDisk{ID: id, SizeBytes: 1 << 20, Create: true})
	}
	disks = append(disks, proto.StorageDisk{ID: "copy", SizeBytes: 1 << 20, CopyFrom: "../victim", Create: true}, proto.StorageDisk{ID: "good", SizeBytes: 1 << 20, Create: true})
	if err := d.Apply(disks, append(bad, "ok/../../victim")); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Stat(filepath.Join(root, "victim.img")); err != nil {
		t.Fatalf("victim: %v", err)
	}
	files, _ := d.Scan()
	if len(files) != 2 || files[0].Name != "good.img" || files[1].Name != "ok.img" {
		t.Fatalf("%+v", files)
	}
}

func TestApplyCreatesOnlyWhenAsked(t *testing.T) {
	d := Dir{t.TempDir()}
	os.WriteFile(filepath.Join(d.Path, "src.img"), []byte("data"), 0o644)
	apply(t, d, []proto.StorageDisk{{ID: "a", SizeBytes: 1 << 20}, {ID: "b", SizeBytes: 1 << 20, CopyFrom: "src"}}, nil)
	for _, n := range []string{"a.img", "b.img"} {
		if _, err := os.Stat(filepath.Join(d.Path, n)); !os.IsNotExist(err) {
			t.Fatalf("%s created without Create: %v", n, err)
		}
	}
}
