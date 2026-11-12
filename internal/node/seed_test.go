package node

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	diskfs "github.com/diskfs/go-diskfs"
)

func TestWriteSeedISORoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := map[string]string{"user-data": "a", "meta-data": "b", "network-config": "c", "vmm-disks.sh": "d"}
	files := map[string][]byte{}
	for k, v := range want {
		files[k] = []byte(v)
	}
	if err := WriteSeedISO(dir, files); err != nil {
		t.Fatal(err)
	}
	// vz rejects disk images whose size is not a whole number of sectors.
	if st, err := os.Stat(filepath.Join(dir, SeedISO)); err != nil || st.Size()%2048 != 0 {
		t.Fatalf("seed size not a multiple of 2048: %v %v", st.Size(), err)
	}
	d, err := diskfs.Open(filepath.Join(dir, SeedISO), diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	fs, err := d.GetFilesystem(0)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimRight(fs.Label(), "\x00 "); got != "cidata" {
		t.Errorf("label = %q, want cidata", got)
	}
	for name, content := range want {
		f, err := fs.OpenFile("/"+name, os.O_RDONLY)
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		b, err := io.ReadAll(f)
		if err != nil || string(b) != content {
			t.Errorf("%s = %q, %v; want %q", name, b, err, content)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("leftover files: %v", entries)
	}
}

func TestWriteSeedISOFailureKeepsOldSeed(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, SeedISO)
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if err := WriteSeedISO(dir, map[string][]byte{"user-data": []byte("x")}); err == nil {
		t.Fatal("expected error writing into a read-only dir")
	}
	if b, err := os.ReadFile(old); err != nil || string(b) != "old" {
		t.Fatalf("seed.iso = %q, %v; want old seed kept", b, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}
