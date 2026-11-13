package hub

import (
	"slices"
	"strings"
	"testing"
	"time"

	"vm-manager/internal/proto"
	"vm-manager/internal/wgkey"
)

func gatewayRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	var cfg Config
	cfg.Underlay.Port, cfg.GuestMTU = 51820, 1300
	r := NewRegistry(newTestNet(t), newTestStore(t), newTestDisks(t), newTestConfig(t, cfg))
	_, pub, _ := wgkey.Generate()
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192, ChassisKey: pub}, &fakeNode{})
	return r, pub
}

func TestGatewayStateListsPortsAndPeers(t *testing.T) {
	r, pub := gatewayRegistry(t)
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	dev, err := r.net.AddDevice("laptop", false)
	if err != nil {
		t.Fatal(err)
	}
	st := r.GatewayState()
	if len(st.Ports) != 1 || st.Ports[0] != (GatewayPort{Name: vm.ID + "-mgmt", MAC: "02:0a:c8:00:00:0a", IP: "10.200.0.10", Hostname: "dev.mgmt.internal", Network: "mgmt"}) {
		t.Fatalf("ports = %+v", st.Ports)
	}
	if len(st.Chassis) != 1 || st.Chassis[0].PublicKey != pub || len(st.Devices) != 1 || st.Devices[0] != (DevicePeer{PublicKey: dev.PublicKey, IP: dev.IP}) {
		t.Fatalf("state = %+v", st)
	}
	if st.GuestMTU != 1300 || st.UnderlayKey == "" || st.ClientsKey == "" {
		t.Fatalf("state = %+v", st)
	}
}

func TestGatewayStatusFillsNodeChassis(t *testing.T) {
	r, pub := gatewayRegistry(t)
	if r.Nodes()[0].Chassis != nil {
		t.Fatalf("chassis before status = %+v", r.Nodes()[0].Chassis)
	}
	hs := time.Now().UTC()
	r.SetGatewayStatus(GatewayStatus{Handshakes: map[string]time.Time{pub: hs}, Chassis: []string{"n1"}})
	ch := r.Nodes()[0].Chassis
	if ch == nil || ch.IP != "10.198.0.11" || !ch.Registered || ch.LastHandshake == nil || !ch.LastHandshake.Equal(hs) {
		t.Fatalf("chassis = %+v", ch)
	}
}

func TestDeviceConfig(t *testing.T) {
	cfg := DeviceConfig(Device{IP: "10.201.0.2", PrivateKey: "PRIV"}, "CPUB", "vmm.lan", 51821, 1300)
	for _, want := range []string{"Address = 10.201.0.2/32", "PrivateKey = PRIV", "DNS = 10.201.0.1", "MTU = 1300", "PublicKey = CPUB",
		"Endpoint = vmm.lan:51821", "AllowedIPs = 10.200.0.0/16, 10.201.0.0/24", "PersistentKeepalive = 25"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
}

func TestGatewayStateKeepsPortsAfterHubRestart(t *testing.T) {
	r1, _ := newTestRegistry(t)
	vm, err := r1.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	q := validReq()
	q.Name, q.CPUs = "db", 1
	db, err := r1.CreateVM(q)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := LoadNetModel(r1.net.path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := LoadVMStore(r1.store.path)
	if err != nil {
		t.Fatal(err)
	}
	st := NewRegistry(m2, store, newTestDisks(t), newTestConfig(t, Config{})).GatewayState()
	want := []GatewayPort{
		{Name: db.ID + "-mgmt", MAC: "02:0a:c8:00:00:0b", IP: "10.200.0.11", Hostname: "db.mgmt.internal", Network: "mgmt"},
		{Name: vm.ID + "-mgmt", MAC: "02:0a:c8:00:00:0a", IP: "10.200.0.10", Hostname: "dev.mgmt.internal", Network: "mgmt"},
	}
	if !slices.Equal(st.Ports, want) {
		t.Fatalf("ports = %+v, want %+v", st.Ports, want)
	}
}

func TestGatewayStateNetworks(t *testing.T) {
	r1, _ := newTestRegistry(t)
	a, err := r1.net.AddNetwork("a", "10.50.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	q := validReq()
	q.NetworkIDs = []string{a.ID}
	vm, err := r1.CreateVM(q)
	if err != nil {
		t.Fatal(err)
	}
	st := r1.GatewayState()
	if !slices.Equal(st.Networks, []GatewayNetwork{{ID: a.ID, Name: "a", Subnet: "10.50.0.0/24"}}) {
		t.Fatalf("networks = %+v", st.Networks)
	}
	want := []GatewayPort{
		{Name: vm.ID + "-" + a.ID, MAC: "02:0a:32:00:00:0a", IP: "10.50.0.10", Hostname: "dev.a.internal", Network: a.ID},
		{Name: vm.ID + "-mgmt", MAC: "02:0a:c8:00:00:0a", IP: "10.200.0.10", Hostname: "dev.mgmt.internal", Network: "mgmt"},
	}
	if !slices.Equal(st.Ports, want) {
		t.Fatalf("ports = %+v, want %+v", st.Ports, want)
	}
	m2, err := LoadNetModel(r1.net.path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := LoadVMStore(r1.store.path)
	if err != nil {
		t.Fatal(err)
	}
	st2 := NewRegistry(m2, store, newTestDisks(t), newTestConfig(t, Config{})).GatewayState()
	if !slices.Equal(st2.Networks, st.Networks) || !slices.Equal(st2.Ports, st.Ports) {
		t.Fatalf("after restart: %+v", st2)
	}
}

func TestGatewayStateReflectsGuestMTUChange(t *testing.T) {
	r, _ := gatewayRegistry(t)
	if _, err := r.cfg.Update(func(c *Config) error { c.GuestMTU = 1200; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := r.GatewayState().GuestMTU; got != 1200 {
		t.Fatalf("GuestMTU = %d", got)
	}
}

func TestGatewayVersion(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.SetGatewayStatus(GatewayStatus{Version: "v0.5.0"})
	if v := r.GatewayVersion(); v != "v0.5.0" {
		t.Errorf("version = %q", v)
	}
	r.SetGatewayStatus(GatewayStatus{})
	if v := r.GatewayVersion(); v != "" {
		t.Errorf("version of an older gateway = %q", v)
	}
}
