package node

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCloneFileFallback(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	want := bytes.Repeat([]byte("vmm"), 100000)
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cloneFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(dst); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("clone differs: %v", err)
	}
}
