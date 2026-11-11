package vz

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMakeSeedISOFailureKeepsOldSeed(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, seedISO)
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := makeSeedISO(ctx, dir, func(string) error { return nil }); err == nil {
		t.Fatal("expected error from cancelled build")
	}
	if b, err := os.ReadFile(old); err != nil || string(b) != "old" {
		t.Fatalf("seed.iso = %q, %v; want old seed kept", b, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}
