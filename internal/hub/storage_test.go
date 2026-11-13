package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"vm-manager/internal/proto"
	"vm-manager/internal/storage"
)

func startStorage(t *testing.T, prep func(dir string)) (*Registry, *DiskStore, string) {
	t.Helper()
	disks := newTestDisks(t)
	r := NewRegistry(newTestNet(t), newTestStore(t), disks, newTestConfig(t, Config{}))
	setStorageEndpoint(t, r, "storage:10809")
	srv := httptest.NewServer(StorageHandler(r, func() string { return testToken }))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	if prep != nil {
		prep(dir)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		storage.Run(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), func() (string, error) { return testToken, nil }, storage.Dir{Path: dir}, 50*time.Millisecond)
	}()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, "storage online", func() bool { return r.Storage().Online })
	return r, disks, dir
}

func reportedFile(r *Registry, name string) (proto.StorageFile, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := slices.IndexFunc(r.storageFiles, func(f proto.StorageFile) bool { return f.Name == name })
	if i < 0 {
		return proto.StorageFile{}, false
	}
	return r.storageFiles[i], true
}

func TestStorageLinkCreatesAndReports(t *testing.T) {
	r, disks, dir := startStorage(t, nil)
	if err := disks.Put(StoredDisk{ID: "d1", Name: "data", SizeGB: 1}); err != nil {
		t.Fatal(err)
	}
	r.pushStorage()
	waitFor(t, "file created", func() bool {
		st, err := os.Stat(filepath.Join(dir, "d1.img"))
		return err == nil && st.Size() == 1<<30
	})
	waitFor(t, "file reported", func() bool {
		f, ok := reportedFile(r, "d1.img")
		return ok && f.SizeBytes == 1<<30 && f.AllocatedBytes >= 0
	})
	if v := r.Storage(); !v.Online || v.Endpoint != "storage:10809" || disks.Endpoint() != "storage:10809" {
		t.Fatalf("view %+v, stored endpoint %q", v, disks.Endpoint())
	}
}

func TestStorageLinkDeletesTombstoned(t *testing.T) {
	r, disks, dir := startStorage(t, nil)
	if err := disks.Put(StoredDisk{ID: "d1", Name: "data", SizeGB: 1}); err != nil {
		t.Fatal(err)
	}
	r.pushStorage()
	waitFor(t, "file reported", func() bool { _, ok := reportedFile(r, "d1.img"); return ok })
	if err := disks.Delete("d1"); err != nil {
		t.Fatal(err)
	}
	r.pushStorage()
	waitFor(t, "file gone", func() bool { _, err := os.Stat(filepath.Join(dir, "d1.img")); return os.IsNotExist(err) })
	waitFor(t, "tombstone cleared", func() bool { return len(disks.Tombstones()) == 0 })
}

func TestStorageLinkClearsCopyFrom(t *testing.T) {
	r, disks, _ := startStorage(t, nil)
	if err := disks.Put(StoredDisk{ID: "src", Name: "src", SizeGB: 1}); err != nil {
		t.Fatal(err)
	}
	r.pushStorage()
	waitFor(t, "source reported", func() bool { _, ok := reportedFile(r, "src.img"); return ok })
	if err := disks.Put(StoredDisk{ID: "dst", Name: "dst", SizeGB: 1, CopyFrom: "src"}); err != nil {
		t.Fatal(err)
	}
	r.pushStorage()
	waitFor(t, "copyFrom cleared", func() bool { d, _ := disks.Get("dst"); return d.CopyFrom == "" })
}

func TestStorageLinkKeepsUnknownFiles(t *testing.T) {
	r, _, dir := startStorage(t, func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "zzz.img"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	waitFor(t, "orphan listed", func() bool {
		o := r.Storage().Orphans
		return len(o) == 1 && o[0].Name == "zzz.img"
	})
	if _, err := os.Stat(filepath.Join(dir, "zzz.img")); err != nil {
		t.Fatal(err)
	}
}

func TestStorageHandlerNeedsToken(t *testing.T) {
	r := NewRegistry(newTestNet(t), newTestStore(t), newTestDisks(t), newTestConfig(t, Config{}))
	setStorageEndpoint(t, r, "storage:10809")
	srv := httptest.NewServer(StorageHandler(r, func() string { return testToken }))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestStorageVersion(t *testing.T) {
	r, _ := newTestRegistry(t)
	fs := &fakeStorage{}
	r.StorageConnected(fs)
	r.SetStorageVersion("v0.5.0")
	if v := r.Storage().Version; v != "v0.5.0" {
		t.Errorf("version = %q", v)
	}
	r.StorageDisconnected(fs)
	if v := r.Storage().Version; v != "" {
		t.Errorf("version after disconnect = %q", v)
	}
}
