package hub

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"vm-manager/internal/wgkey"
)

const mgmtSubnet = "10.200.0.0/16"

type DevicePeer struct {
	PublicKey string `json:"publicKey"`
	IP        string `json:"ip"`
}

type GatewayPort struct {
	Name     string `json:"name"`
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
}

// GatewayState is the desired state the gateway agent applies.
type GatewayState struct {
	UnderlayKey string        `json:"underlayKey"`
	ClientsKey  string        `json:"clientsKey"`
	GuestMTU    int           `json:"guestMTU"`
	Chassis     []Chassis     `json:"chassis"`
	Devices     []DevicePeer  `json:"devices"`
	Ports       []GatewayPort `json:"ports"`
}

// GatewayStatus is what the gateway observes: WireGuard handshakes by peer public key and chassis registered in the OVN SB.
type GatewayStatus struct {
	Handshakes map[string]time.Time `json:"handshakes"`
	Chassis    []string             `json:"chassis"`
}

func (r *Registry) GatewayState() GatewayState {
	underlay, clients := r.net.GatewayKeys()
	st := GatewayState{UnderlayKey: underlay, ClientsKey: clients, GuestMTU: r.cfg.GuestMTU,
		Chassis: r.net.Chassis(), Devices: []DevicePeer{}, Ports: []GatewayPort{}}
	for _, d := range r.net.Devices() {
		st.Devices = append(st.Devices, DevicePeer{PublicKey: d.PublicKey, IP: d.IP})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	type namedPort struct {
		name string
		GatewayPort
	}
	var ports []namedPort
	for _, vm := range r.vms {
		for _, n := range vm.NICs {
			ports = append(ports, namedPort{vm.Name, GatewayPort{Name: n.Port, MAC: n.MAC, IP: n.IP, Hostname: vm.Name + ".mgmt.internal"}})
		}
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].name < ports[j].name })
	for _, p := range ports {
		st.Ports = append(st.Ports, p.GatewayPort)
	}
	return st
}

func (r *Registry) SetGatewayStatus(s GatewayStatus) {
	chassis := r.net.Chassis()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range chassis {
		n, ok := r.nodes[c.NodeID]
		if !ok {
			continue
		}
		cs := &ChassisState{IP: c.IP, Registered: slices.Contains(s.Chassis, c.NodeID)}
		if t, ok := s.Handshakes[c.PublicKey]; ok && !t.IsZero() {
			cs.LastHandshake = &t
		}
		n.Chassis = cs
	}
}

func DeviceConfig(d Device, clientsPub, endpoint string, port, mtu int) string {
	return fmt.Sprintf(`[Interface]
Address = %s/32
PrivateKey = %s
DNS = 10.201.0.1
MTU = %d

[Peer]
PublicKey = %s
Endpoint = %s:%d
AllowedIPs = 10.200.0.0/16, 10.201.0.0/24
PersistentKeepalive = 25
`, d.IP, d.PrivateKey, mtu, clientsPub, endpoint, port)
}

func (r *Registry) clientsPublicKey() (string, error) {
	_, priv := r.net.GatewayKeys()
	return wgkey.Public(priv)
}
