package hub

import (
	"path/filepath"
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
	r := NewRegistry(newTestNet(t), cfg)
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
	dev, err := r.net.AddDevice("laptop")
	if err != nil {
		t.Fatal(err)
	}
	st := r.GatewayState()
	if len(st.Ports) != 1 || st.Ports[0] != (GatewayPort{Name: vm.ID + "-mgmt", MAC: "02:0a:c8:00:00:0a", IP: "10.200.0.10", Hostname: "dev.mgmt.internal"}) {
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
	path := filepath.Join(t.TempDir(), "network.json")
	m1, err := LoadNetModel(path)
	if err != nil {
		t.Fatal(err)
	}
	r1 := NewRegistry(m1, Config{})
	r1.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192}, &fakeNode{})
	vm, err := r1.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	adopted := proto.VMSpec{ID: "vm2", Name: "db", NICs: []proto.NIC{{Network: "mgmt", IP: "10.200.0.77", MAC: nicMAC("10.200.0.77"), Port: "vm2-mgmt"}}}
	r1.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192, VMs: []proto.VMRecord{
		{Spec: vm.VMSpec, Status: proto.VMStatus{ID: vm.ID, State: proto.StateRunning}},
		{Spec: adopted, Status: proto.VMStatus{ID: "vm2", State: proto.StateRunning}},
	}}, &fakeNode{})

	m2, err := LoadNetModel(path)
	if err != nil {
		t.Fatal(err)
	}
	st := NewRegistry(m2, Config{}).GatewayState()
	want := []GatewayPort{
		{Name: "vm2-mgmt", MAC: "02:0a:c8:00:00:4d", IP: "10.200.0.77", Hostname: "db.mgmt.internal"},
		{Name: vm.ID + "-mgmt", MAC: "02:0a:c8:00:00:0a", IP: "10.200.0.10", Hostname: "dev.mgmt.internal"},
	}
	if !slices.Equal(st.Ports, want) {
		t.Fatalf("ports = %+v, want %+v", st.Ports, want)
	}
}
