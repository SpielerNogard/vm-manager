package hub

import (
	"errors"
	"net/netip"
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
	n1, err := m.AllocNICs("vm1", "vm1", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []proto.NIC{{Network: "mgmt", IP: "10.200.0.10", MAC: "02:0a:c8:00:00:0a", Port: "vm1-mgmt"}}
	if !reflect.DeepEqual(n1, want) {
		t.Fatalf("got %+v, want %+v", n1, want)
	}
	n2, _ := m.AllocNICs("vm2", "vm2", nil)
	if n2[0].IP != "10.200.0.11" {
		t.Fatalf("second IP = %s", n2[0].IP)
	}
}

func TestNetModelReloadKeepsAllocations(t *testing.T) {
	m, path := newModel(t)
	nics, _ := m.AllocNICs("vm1", "vm1", nil)
	ch, _ := m.Enroll("nodeA", "keyA")
	dev, err := m.AddDevice("laptop", false)
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
	m.AllocNICs("vm1", "vm1", nil)
	m.AllocNICs("vm2", "vm2", nil)
	if err := m.Release("vm1"); err != nil {
		t.Fatal(err)
	}
	if len(m.NICs("vm1")) != 0 || len(m.AllNICs()) != 1 {
		t.Fatal("vm1 NICs not released")
	}
	n, _ := m.AllocNICs("vm3", "vm3", nil)
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
	d, err := m.AddDevice("laptop", false)
	if err != nil {
		t.Fatal(err)
	}
	if d.IP != "10.201.0.2" || d.ID == "" {
		t.Fatalf("device = %+v", d)
	}
	if pub, _ := wgkey.Public(d.PrivateKey); pub != d.PublicKey {
		t.Fatal("public key does not match private key")
	}
	if _, err := m.AddDevice("Bad Name", false); !errors.Is(err, ErrInvalid) {
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

func mustAddNetwork(t *testing.T, m *NetModel, name, subnet string) Network {
	t.Helper()
	n, err := m.AddNetwork(name, subnet)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAddNetworkAutoSubnet(t *testing.T) {
	m, _ := newModel(t)
	a, b := mustAddNetwork(t, m, "a", ""), mustAddNetwork(t, m, "b", "")
	if a.Subnet != "10.210.0.0/24" || b.Subnet != "10.210.1.0/24" {
		t.Fatalf("subnets = %s, %s", a.Subnet, b.Subnet)
	}
}

func TestAddNetworkCustomSubnet(t *testing.T) {
	m, _ := newModel(t)
	if n := mustAddNetwork(t, m, "db", "192.168.50.7/24"); n.Subnet != "192.168.50.0/24" {
		t.Fatalf("subnet = %s", n.Subnet)
	}
	if n := mustAddNetwork(t, m, "a", ""); n.Subnet != "10.210.0.0/24" {
		t.Fatalf("auto subnet = %s", n.Subnet)
	}
}

func TestAddNetworkRejects(t *testing.T) {
	m, _ := newModel(t)
	mustAddNetwork(t, m, "dup", "192.168.50.0/24")
	for _, tc := range []struct {
		name, subnet string
		want         error
	}{
		{"mgmt", "", ErrInvalid},
		{"Bad_Name", "", ErrInvalid},
		{"dup", "", ErrConflict},
		{"x", "8.8.8.0/24", ErrInvalid},
		{"x", "10.200.5.0/24", ErrInvalid},
		{"x", "192.168.64.0/24", ErrInvalid},
		{"x", "10.198.0.0/16", ErrInvalid},
		{"x", "10.0.0.0/8", ErrInvalid},
		{"x", "10.50.0.0/29", ErrInvalid},
		{"x", "192.168.50.128/25", ErrInvalid},
		{"x", "fd00::/64", ErrInvalid},
	} {
		if _, err := m.AddNetwork(tc.name, tc.subnet); !errors.Is(err, tc.want) {
			t.Errorf("AddNetwork(%q, %q) = %v, want %v", tc.name, tc.subnet, err, tc.want)
		}
	}
}

func TestAllocNICsWithNetworks(t *testing.T) {
	m, _ := newModel(t)
	a := mustAddNetwork(t, m, "a", "")
	nics, err := m.AllocNICs("vm1", "dev", []string{a.ID})
	if err != nil || len(nics) != 2 {
		t.Fatalf("nics = %+v, err = %v", nics, err)
	}
	want := proto.NIC{Network: a.ID, IP: "10.210.0.10", MAC: nicMAC("10.210.0.10"), Port: "vm1-" + a.ID}
	if nics[1] != want {
		t.Fatalf("got %+v, want %+v", nics[1], want)
	}
	n2, _ := m.AllocNICs("vm2", "dev2", []string{a.ID})
	if n2[1].IP != "10.210.0.11" {
		t.Fatalf("second IP = %s", n2[1].IP)
	}
	if _, err := m.AllocNICs("vm3", "x", []string{"nope"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown id: %v", err)
	}
	if _, err := m.AllocNICs("vm3", "x", []string{a.ID, a.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate id: %v", err)
	}
}

func TestSetNetworkNICsKeepsAddresses(t *testing.T) {
	m, _ := newModel(t)
	a, b := mustAddNetwork(t, m, "a", ""), mustAddNetwork(t, m, "b", "")
	before, err := m.AllocNICs("vm1", "dev", []string{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.SetNetworkNICs("vm1", []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != before[0] || got[1] != before[2] {
		t.Fatalf("got %+v, before %+v", got, before)
	}
	got, err = m.SetNetworkNICs("vm1", []string{b.ID, a.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1] != before[2] || got[2].Network != a.ID || !netip.MustParsePrefix(a.Subnet).Contains(netip.MustParseAddr(got[2].IP)) {
		t.Fatalf("got %+v", got)
	}
	if _, err := m.SetNetworkNICs("ghost", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ghost: %v", err)
	}
}

func TestDeleteNetwork(t *testing.T) {
	m, _ := newModel(t)
	a := mustAddNetwork(t, m, "a", "")
	if _, err := m.AllocNICs("vm1", "dev", []string{a.ID}); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteNetwork(a.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("in use: %v", err)
	}
	if _, err := m.SetNetworkNICs("vm1", nil); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteNetwork(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteNetwork(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestNetworksSurviveReload(t *testing.T) {
	m, path := newModel(t)
	a := mustAddNetwork(t, m, "a", "")
	if _, err := m.AllocNICs("vm1", "dev", []string{a.ID}); err != nil {
		t.Fatal(err)
	}
	m2, err := LoadNetModel(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Networks(), m2.Networks()) || !reflect.DeepEqual(m.NICs("vm1"), m2.NICs("vm1")) {
		t.Fatalf("reload mismatch: %+v vs %+v", m2.Networks(), m.Networks())
	}
}
