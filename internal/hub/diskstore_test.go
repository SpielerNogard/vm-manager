package hub

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func testDisk(id, name string) StoredDisk {
	return StoredDisk{ID: id, Name: name, SizeGB: 10, CreatedAt: time.Unix(1700000000, 0).UTC()}
}

func TestDiskStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disks.json")
	s, err := LoadDiskStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(testDisk("2", "b")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(testDisk("1", "a")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEndpoint("10.0.0.1:10809"); err != nil {
		t.Fatal(err)
	}
	r, err := LoadDiskStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Disks(), s.Disks()) || len(r.Disks()) != 2 || r.Disks()[0].Name != "a" {
		t.Fatalf("got %+v", r.Disks())
	}
	if r.Endpoint() != "10.0.0.1:10809" {
		t.Fatalf("endpoint %q", r.Endpoint())
	}
}

func TestDiskStoreDeleteTombstones(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disks.json")
	s, _ := LoadDiskStore(path)
	if err := s.Put(testDisk("1", "a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("1"); ok {
		t.Fatal("disk still present")
	}
	r, _ := LoadDiskStore(path)
	if !reflect.DeepEqual(r.Tombstones(), []string{"1"}) {
		t.Fatalf("tombstones %v", r.Tombstones())
	}
	if err := r.ClearTombstone("1"); err != nil {
		t.Fatal(err)
	}
	if len(r.Tombstones()) != 0 {
		t.Fatalf("tombstones %v", r.Tombstones())
	}
}

func TestDiskStoreRollbackOnSaveError(t *testing.T) {
	dir := t.TempDir()
	s, _ := LoadDiskStore(filepath.Join(dir, "sub", "disks.json"))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if err := s.Put(testDisk("1", "a")); err == nil {
		t.Skip("directory permissions not enforced (running as root?)")
	}
	if len(s.Disks()) != 0 {
		t.Fatalf("not rolled back: %+v", s.Disks())
	}
}
