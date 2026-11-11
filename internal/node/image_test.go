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
	if err := CreateDataDisks(dir, []proto.DataDisk{{Name: "a", MountPath: "/a", SizeGB: 1}, {Name: "b", MountPath: "/b", SizeGB: 2}}); err != nil {
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
	f := VMFile{Spec: proto.VMSpec{ID: "abc123", Name: "dev", SSHPublicKeys: []string{"ssh-ed25519 AAAAkey me@host", "ssh-rsa BBBBkey you@host"}}, MAC: "7e:30:17:4e:4a:00"}
	ud, md, nc := CloudInit(f)
	for _, want := range []string{"#cloud-config\n", "hostname: dev", "name: ubuntu", "NOPASSWD:ALL", "      - \"ssh-ed25519 AAAAkey me@host\"\n      - \"ssh-rsa BBBBkey you@host\"\n", "console=hvc0", "update-grub"} {
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

func TestCloudInitDataDisks(t *testing.T) {
	f := VMFile{Spec: proto.VMSpec{Name: "dev", DataDisks: []proto.DataDisk{{Name: "data", MountPath: "/data", SizeGB: 1}, {Name: "cache", MountPath: "/var/cache/x", SizeGB: 2}}}}
	ud, _, _ := CloudInit(f)
	for _, want := range []string{
		"fs_setup:\n",
		`{label: "data", filesystem: ext4, device: /dev/vdb, partition: none}`,
		`{label: "cache", filesystem: ext4, device: /dev/vdc, partition: none}`,
		"mounts:\n",
		`["LABEL=data", "/data", ext4, "defaults,nofail", "0", "2"]`,
		`["LABEL=cache", "/var/cache/x", ext4, "defaults,nofail", "0", "2"]`,
		"runcmd:\n",
		`[chown, "ubuntu:ubuntu", "/data"]`,
		`[chown, "ubuntu:ubuntu", "/var/cache/x"]`,
	} {
		if !strings.Contains(ud, want) {
			t.Errorf("user-data missing %q:\n%s", want, ud)
		}
	}
	if ud, _, _ := CloudInit(VMFile{Spec: proto.VMSpec{Name: "dev"}}); strings.Contains(ud, "fs_setup") || strings.Contains(ud, "mounts") {
		t.Errorf("user-data without disks has disk config:\n%s", ud)
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

func TestCloudInitMgmtNIC(t *testing.T) {
	f := VMFile{Spec: proto.VMSpec{Name: "dev", NICs: []proto.NIC{{Network: "mgmt", MAC: "02:0a:c8:00:00:14", IP: "10.200.0.20", Port: "x-mgmt"}}}, MAC: "7e:30:17:4e:4a:00"}
	_, _, nc := CloudInit(f)
	for _, want := range []string{"mgmt0:", `macaddress: "02:0a:c8:00:00:14"`, "dhcp4: true", "use-dns: false", "use-hostname: false", "use-domains: false"} {
		if !strings.Contains(nc, want) {
			t.Errorf("network-config missing %q:\n%s", want, nc)
		}
	}
	if strings.Contains(nc, "10.200.0.20") || strings.Contains(nc, "mtu") {
		t.Errorf("mgmt nic must not carry static config:\n%s", nc)
	}
	mgmt := nc[strings.Index(nc, "mgmt0:"):]
	primary := nc[:strings.Index(nc, "mgmt0:")]
	for _, want := range []string{"link-local: []", "accept-ra: false"} {
		if !strings.Contains(mgmt, want) {
			t.Errorf("mgmt block missing %q:\n%s", want, nc)
		}
		if strings.Contains(primary, want) {
			t.Errorf("primary block must not contain %q:\n%s", want, nc)
		}
	}
}

func TestCloudInitNetworkNICs(t *testing.T) {
	f := VMFile{Spec: proto.VMSpec{Name: "dev", NICs: []proto.NIC{
		{Network: "mgmt", MAC: "02:0a:c8:00:00:14"},
		{Network: "n1", MAC: "02:00:00:00:01:01"},
		{Network: "n2", MAC: "02:00:00:00:02:01"},
	}}, MAC: "7e:30:17:4e:4a:00"}
	ud, _, nc := CloudInit(f)
	if !strings.Contains(ud, "when: [boot]") {
		t.Errorf("user-data missing network update on boot:\n%s", ud)
	}
	mgmtAt, net0At, net1At := strings.Index(nc, "  mgmt0:"), strings.Index(nc, "  net0:"), strings.Index(nc, "  net1:")
	if mgmtAt < 0 || net0At < mgmtAt || net1At < net0At {
		t.Fatalf("network-config entries missing or out of order:\n%s", nc)
	}
	mgmt, net0, net1 := nc[mgmtAt:net0At], nc[net0At:net1At], nc[net1At:]
	for _, want := range []string{`macaddress: "02:0a:c8:00:00:14"`, "nameservers:", "10.201.0.1", `"~internal"`} {
		if !strings.Contains(mgmt, want) {
			t.Errorf("mgmt0 missing %q:\n%s", want, nc)
		}
	}
	for mac, block := range map[string]string{"02:00:00:00:01:01": net0, "02:00:00:00:02:01": net1} {
		for _, want := range []string{`macaddress: "` + mac + `"`, "dhcp4: true", "link-local: []", "accept-ra: false", "use-dns: false", "use-hostname: false", "use-domains: false"} {
			if !strings.Contains(block, want) {
				t.Errorf("net block for %s missing %q:\n%s", mac, want, nc)
			}
		}
		if strings.Contains(block, "nameservers") {
			t.Errorf("net block for %s has nameservers:\n%s", mac, nc)
		}
	}
}
