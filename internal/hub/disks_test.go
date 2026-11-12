package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"

	"vm-manager/internal/proto"
)

type fakeStorage struct {
	mu   sync.Mutex
	sent []proto.StorageMessage
}

func (f *fakeStorage) Send(m proto.StorageMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeStorage) last() proto.StorageMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return proto.StorageMessage{}
	}
	return f.sent[len(f.sent)-1]
}

func (f *fakeStorage) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func newDiskRegistry(t *testing.T) (*Registry, *fakeStorage) {
	t.Helper()
	r := NewRegistry(newTestNet(t), newTestStore(t), newTestDisks(t), newTestConfig(t, Config{}))
	fs := &fakeStorage{}
	setStorageEndpoint(t, r, "s:1")
	r.StorageConnected(fs)
	return r, fs
}

func TestCreateDiskValidation(t *testing.T) {
	r, _ := newDiskRegistry(t)
	if _, err := r.CreateDisk("ok", 1); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		size int
	}{{"Bad Name", 1}, {"zero", 0}, {"ok", 1}} {
		if _, err := r.CreateDisk(c.name, c.size); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: err = %v", c, err)
		}
	}
}

func TestCreateDiskPushesStorage(t *testing.T) {
	r, fs := newDiskRegistry(t)
	d, err := r.CreateDisk("data", 10)
	if err != nil {
		t.Fatal(err)
	}
	m := fs.last()
	if len(m.Disks) != 1 || m.Disks[0].ID != d.ID || m.Disks[0].SizeBytes != 10<<30 {
		t.Fatalf("desired = %+v", m)
	}
}

func TestDiskStateCreatingThenReady(t *testing.T) {
	r, _ := newDiskRegistry(t)
	d, _ := r.CreateDisk("data", 1)
	if d.State != "creating" {
		t.Fatalf("state = %q", d.State)
	}
	r.StorageReport([]proto.StorageFile{{Name: proto.StorageFileName(d.ID), SizeBytes: 1 << 30, AllocatedBytes: 4096}})
	got := r.Disks()[0]
	if got.State != "ready" || got.AllocatedBytes != 4096 {
		t.Fatalf("view = %+v", got)
	}
}

func TestCopyDiskSetsCopyFrom(t *testing.T) {
	r, fs := newDiskRegistry(t)
	src, _ := r.CreateDisk("src", 2)
	cp, warn, err := r.CopyDisk(src.ID, "dst")
	if err != nil || warn != "" || cp.CopyFrom != src.ID || cp.SizeGB != 2 {
		t.Fatalf("copy = %+v %q %v", cp, warn, err)
	}
	if m := fs.last(); !slices.Contains(m.Disks, proto.StorageDisk{ID: cp.ID, SizeBytes: 2 << 30, CopyFrom: src.ID, Create: true}) {
		t.Fatalf("desired = %+v", m)
	}
	if _, _, err := r.CopyDisk("nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestCopyFromCreatingSourceIsRepushed(t *testing.T) {
	r, fs := newDiskRegistry(t)
	src, _ := r.CreateDisk("src", 1)
	cp, _, _ := r.CopyDisk(src.ID, "dst")
	before := fs.count()
	r.StorageReport(nil)
	if fs.count() != before {
		t.Fatal("pushed although the source file is absent")
	}
	r.StorageReport([]proto.StorageFile{{Name: proto.StorageFileName(src.ID), SizeBytes: 1 << 30}})
	if fs.count() != before+1 {
		t.Fatalf("pushes = %d, want %d", fs.count(), before+1)
	}
	if m := fs.last(); !slices.ContainsFunc(m.Disks, func(d proto.StorageDisk) bool { return d.ID == cp.ID && d.CopyFrom == src.ID }) {
		t.Fatalf("desired = %+v", m)
	}
}

func TestGrowDiskRejectsShrink(t *testing.T) {
	r, fs := newDiskRegistry(t)
	d, _ := r.CreateDisk("data", 5)
	for _, size := range []int{5, 3} {
		if _, err := r.GrowDisk(d.ID, size, false); !errors.Is(err, ErrInvalid) {
			t.Fatalf("size %d: err = %v", size, err)
		}
	}
	g, err := r.GrowDisk(d.ID, 8, false)
	if err != nil || g.SizeGB != 8 || fs.last().Disks[0].SizeBytes != 8<<30 {
		t.Fatalf("grow = %+v %v", g, err)
	}
}

func TestDeleteDiskTombstones(t *testing.T) {
	r, fs := newDiskRegistry(t)
	d, _ := r.CreateDisk("data", 1)
	if err := r.DeleteDisk(d.ID); err != nil {
		t.Fatal(err)
	}
	if m := fs.last(); !slices.Contains(m.Deleted, d.ID) || len(m.Disks) != 0 {
		t.Fatalf("desired = %+v", m)
	}
	if err := r.DeleteDisk(d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteDiskRefusedWhileCopySource(t *testing.T) {
	r, _ := newDiskRegistry(t)
	src, _ := r.CreateDisk("src", 1)
	cp, _, _ := r.CopyDisk(src.ID, "dst")
	if err := r.DeleteDisk(src.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	r.StorageReport([]proto.StorageFile{{Name: proto.StorageFileName(cp.ID), SizeBytes: 1 << 30}})
	if err := r.DeleteDisk(src.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteDiskRefusedWhileReleasing(t *testing.T) {
	r, _ := newDiskRegistry(t)
	d, _ := r.CreateDisk("data", 1)
	st, _ := r.disks.Get(d.ID)
	st.ReleasingOn = "n1"
	r.disks.Put(st)
	if err := r.DeleteDisk(d.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	if got := r.Disks()[0]; got.State != "releasing" || got.ReleasingOn != "n1" {
		t.Fatalf("view = %+v", got)
	}
}

func TestAPIDisksLifecycle(t *testing.T) {
	srv, _, _ := apiServerWith(t, testAPIConfig())
	s, body := doJSON(t, "POST", srv.URL+"/api/disks", `{"name":"data","sizeGB":2}`)
	var d DiskView
	json.Unmarshal(body, &d)
	if s != http.StatusCreated || d.ID == "" || d.Name != "data" || d.SizeGB != 2 || d.State != "creating" {
		t.Fatalf("create: %d %s", s, body)
	}
	if s, _ := doJSON(t, "POST", srv.URL+"/api/disks", `{"name":"data","sizeGB":2}`); s != http.StatusBadRequest {
		t.Fatalf("duplicate: %d", s)
	}
	s, body = doJSON(t, "GET", srv.URL+"/api/disks", "")
	var list []DiskView
	json.Unmarshal(body, &list)
	if s != 200 || len(list) != 1 || list[0].ID != d.ID {
		t.Fatalf("list: %d %s", s, body)
	}
	if s, _ := doJSON(t, "PATCH", srv.URL+"/api/disks/"+d.ID+"?restartAllowed=true", `{"sizeGB":2}`); s != http.StatusBadRequest {
		t.Fatalf("shrink: %d", s)
	}
	s, body = doJSON(t, "PATCH", srv.URL+"/api/disks/"+d.ID, `{"sizeGB":4}`)
	json.Unmarshal(body, &d)
	if s != 200 || d.SizeGB != 4 {
		t.Fatalf("grow: %d %s", s, body)
	}
	s, body = doJSON(t, "POST", srv.URL+"/api/disks/"+d.ID+"/copy", `{"name":"copy"}`)
	var cp struct {
		Disk    DiskView
		Warning *string
	}
	json.Unmarshal(body, &cp)
	if s != http.StatusCreated || cp.Disk.CopyFrom != d.ID || cp.Warning != nil {
		t.Fatalf("copy: %d %s", s, body)
	}
	if s, _ := doJSON(t, "POST", srv.URL+"/api/disks/nope/copy", `{"name":"x"}`); s != http.StatusNotFound {
		t.Fatalf("copy unknown: %d", s)
	}
	if s, _ := doJSON(t, "DELETE", srv.URL+"/api/disks/"+d.ID, ""); s != http.StatusConflict {
		t.Fatalf("delete copy source: %d", s)
	}
	if s, _ := doJSON(t, "DELETE", srv.URL+"/api/disks/"+cp.Disk.ID, ""); s != http.StatusNoContent {
		t.Fatalf("delete: %d", s)
	}
}

func TestAPIStorageStatus(t *testing.T) {
	srv, r, _ := apiServerWith(t, testAPIConfig())
	setStorageEndpoint(t, r, "s:1")
	r.StorageConnected(&fakeStorage{})
	r.StorageReport([]proto.StorageFile{{Name: "zzz.img", SizeBytes: 1}})
	s, body := doJSON(t, "GET", srv.URL+"/api/storage", "")
	var v StorageView
	json.Unmarshal(body, &v)
	if s != 200 || !v.Online || v.Endpoint != "s:1" || len(v.Orphans) != 1 {
		t.Fatalf("status: %d %s", s, body)
	}
}
