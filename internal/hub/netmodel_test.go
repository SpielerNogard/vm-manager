package hub

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"vm-manager/internal/proto"
	"vm-manager/internal/wgkey"
)

func newModel(t *testing.T) (*NetModel, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network.json")
	m, err := LoadNetModel(path)
	if err != nil {
		t.Fatal(err)
	}
	return m, path
}

func TestNetModelAllocNICs(t *testing.T) {
	m, _ := newModel(t)
	n1, err := m.AllocNICs("vm1", "vm1")
	if err != nil {
		t.Fatal(err)
	}
	want := []proto.NIC{{Network: "mgmt", IP: "10.200.0.10", MAC: "02:0a:c8:00:00:0a", Port: "vm1-mgmt"}}
	if !reflect.DeepEqual(n1, want) {
		t.Fatalf("got %+v, want %+v", n1, want)
	}
	n2, _ := m.AllocNICs("vm2", "vm2")
	if n2[0].IP != "10.200.0.11" {
		t.Fatalf("second IP = %s", n2[0].IP)
	}
}

func TestNetModelReloadKeepsAllocations(t *testing.T) {
	m, path := newModel(t)
	nics, _ := m.AllocNICs("vm1", "vm1")
	ch, _ := m.Enroll("nodeA", "keyA")
	dev, err := m.AddDevice("laptop")
	if err != nil {
		t.Fatal(err)
	}
	up, cl := m.GatewayKeys()
	if up == "" || cl == "" || up == cl {
		t.Fatal("gateway keys not generated")
	}

	m2, err := LoadNetModel(path)
	if err != nil {
		t.Fatal(err)
	}
	up2, cl2 := m2.GatewayKeys()
	if up != up2 || cl != cl2 {
		t.Fatal("gateway keys changed on reload")
	}
	if !reflect.DeepEqual(m2.NICs("vm1"), nics) {
		t.Fatal("NICs changed on reload")
	}
	if !reflect.DeepEqual(m2.Chassis(), []Chassis{ch}) {
		t.Fatal("chassis changed on reload")
	}
	if !reflect.DeepEqual(m2.Devices(), []Device{dev}) {
		t.Fatal("devices changed on reload")
	}
}

func TestNetModelReleaseFreesIP(t *testing.T) {
	m, _ := newModel(t)
	m.AllocNICs("vm1", "vm1")
	m.AllocNICs("vm2", "vm2")
	if err := m.Release("vm1"); err != nil {
		t.Fatal(err)
	}
	if len(m.NICs("vm1")) != 0 || len(m.AllNICs()) != 1 {
		t.Fatal("vm1 NICs not released")
	}
	n, _ := m.AllocNICs("vm3", "vm3")
	if n[0].IP != "10.200.0.10" {
		t.Fatalf("IP = %s, want reuse of 10.200.0.10", n[0].IP)
	}
}

func TestNetModelEnrollStable(t *testing.T) {
	m, _ := newModel(t)
	a, _ := m.Enroll("nodeA", "k1")
	b, _ := m.Enroll("nodeB", "k2")
	if a.IP != "10.198.0.11" || b.IP != "10.198.0.12" {
		t.Fatalf("IPs = %s, %s", a.IP, b.IP)
	}
	a2, err := m.Enroll("nodeA", "k3")
	if err != nil || a2.IP != "10.198.0.11" || a2.PublicKey != "k3" {
		t.Fatalf("re-enroll = %+v, %v", a2, err)
	}
	if got := m.Chassis(); len(got) != 2 || got[0].PublicKey != "k3" {
		t.Fatalf("chassis = %+v", got)
	}
}

func TestNetModelDevices(t *testing.T) {
	m, _ := newModel(t)
	d, err := m.AddDevice("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if d.IP != "10.201.0.2" || d.ID == "" {
		t.Fatalf("device = %+v", d)
	}
	if pub, _ := wgkey.Public(d.PrivateKey); pub != d.PublicKey {
		t.Fatal("public key does not match private key")
	}
	if _, err := m.AddDevice("Bad Name"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if err := m.DeleteDevice("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := m.DeleteDevice(d.ID); err != nil || len(m.Devices()) != 0 {
		t.Fatalf("delete: %v", err)
	}
}

func TestNetModelFileMode(t *testing.T) {
	_, path := newModel(t)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode().Perm())
	}
}
