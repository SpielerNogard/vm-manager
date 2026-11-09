package node

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"vm-manager/internal/proto"
)

func writeBase(t *testing.T, size int) (string, []byte) {
	t.Helper()
	data := bytes.Repeat([]byte("base"), size/4)
	p := filepath.Join(t.TempDir(), "base.raw")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, data
}

func TestCloneResize(t *testing.T) {
	base, data := writeBase(t, 1<<20)
	dst := filepath.Join(t.TempDir(), "root.img")
	if err := CloneResize(base, dst, 4); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != 4<<30 {
		t.Fatalf("size = %d", st.Size())
	}
	f, _ := os.Open(dst)
	defer f.Close()
	head := make([]byte, len(data))
	f.ReadAt(head, 0)
	if !bytes.Equal(head, data) {
		t.Fatal("content differs from base")
	}
}

func TestCloneResizeTooSmall(t *testing.T) {
	base, _ := writeBase(t, 1<<20)
	f, _ := os.OpenFile(base, os.O_RDWR, 0)
	f.Truncate(2 << 30)
	f.Close()
	if err := CloneResize(base, filepath.Join(t.TempDir(), "root.img"), 1); err == nil {
		t.Fatal("expected error for disk smaller than base")
	}
}

func TestCreateDataDisks(t *testing.T) {
	dir := t.TempDir()
	if err := CreateDataDisks(dir, []int{1, 2}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int64{1 << 30, 2 << 30} {
		st, err := os.Stat(filepath.Join(dir, DataDiskName(i)))
		if err != nil || st.Size() != want {
			t.Fatalf("disk %d: %v size %d", i, err, st.Size())
		}
	}
}

func TestCloudInit(t *testing.T) {
	f := VMFile{Spec: proto.VMSpec{ID: "abc123", Name: "dev", SSHPublicKey: "ssh-ed25519 AAAAkey me@host"}, MAC: "7e:30:17:4e:4a:00"}
	ud, md, nc := CloudInit(f)
	for _, want := range []string{"#cloud-config\n", "hostname: dev", "name: ubuntu", "NOPASSWD:ALL", `"ssh-ed25519 AAAAkey me@host"`, "console=hvc0", "update-grub"} {
		if !strings.Contains(ud, want) {
			t.Errorf("user-data missing %q:\n%s", want, ud)
		}
	}
	if !strings.Contains(md, "instance-id: abc123") || !strings.Contains(md, "local-hostname: dev") {
		t.Errorf("meta-data = %q", md)
	}
	// Ubuntu defaults to a DUID client id, which hides the MAC from macOS' dhcpd_leases.
	for _, want := range []string{"version: 2", `macaddress: "7e:30:17:4e:4a:00"`, "dhcp4: true", "dhcp-identifier: mac"} {
		if !strings.Contains(nc, want) {
			t.Errorf("network-config missing %q:\n%s", want, nc)
		}
	}
}

func TestEnsureBaseImageDownloadsOnce(t *testing.T) {
	data := bytes.Repeat([]byte{1, 2, 3, 4}, 1<<18)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Write(data)
	}))
	defer srv.Close()

	dir := t.TempDir()
	p1, err := EnsureBaseImage(dir, srv.URL+"/img")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := EnsureBaseImage(dir, srv.URL+"/img")
	if err != nil || p1 != p2 {
		t.Fatalf("second call: %q %v", p2, err)
	}
	got, _ := os.ReadFile(p1)
	if !bytes.Equal(got, data) || hits.Load() != 1 {
		t.Fatalf("content ok=%v hits=%d", bytes.Equal(got, data), hits.Load())
	}
}

func TestEnsureBaseImageHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	dir := t.TempDir()
	if _, err := EnsureBaseImage(dir, srv.URL+"/img"); err == nil {
		t.Fatal("expected error")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("leftover files: %v", entries)
	}
}
