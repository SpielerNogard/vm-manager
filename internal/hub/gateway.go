package hub

import (
	"fmt"
	"net"
	"strconv"
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
	// Network is "mgmt" or a network ID.
	Network string `json:"network"`
}

type GatewayNetwork struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Subnet string `json:"subnet"`
}

// GatewayState is the desired state the gateway agent applies.
type GatewayState struct {
	UnderlayKey string           `json:"underlayKey"`
	ClientsKey  string           `json:"clientsKey"`
	GuestMTU    int              `json:"guestMTU"`
	Chassis     []Chassis        `json:"chassis"`
	Devices     []DevicePeer     `json:"devices"`
	Networks    []GatewayNetwork `json:"networks"`
	Ports       []GatewayPort    `json:"ports"`
}

// GatewayStatus is what the gateway observes: WireGuard handshakes by peer public key and chassis registered in the OVN SB.
type GatewayStatus struct {
	Handshakes map[string]time.Time `json:"handshakes"`
	Chassis    []string             `json:"chassis"`
}

func (r *Registry) GatewayState() GatewayState {
	underlay, clients := r.net.GatewayKeys()
	st := GatewayState{UnderlayKey: underlay, ClientsKey: clients, GuestMTU: r.cfg.Get().GuestMTU,
		Chassis: r.net.Chassis(), Devices: []DevicePeer{}, Networks: []GatewayNetwork{}, Ports: []GatewayPort{}}
	for _, d := range r.net.Devices() {
		st.Devices = append(st.Devices, DevicePeer{PublicKey: d.PublicKey, IP: d.IP})
	}
	netNames := map[string]string{mgmtNetwork: mgmtNetwork}
	for _, n := range r.net.Networks() {
		st.Networks = append(st.Networks, GatewayNetwork{ID: n.ID, Name: n.Name, Subnet: n.Subnet})
		netNames[n.ID] = n.Name
	}
	// ports come from the persisted model so a hub restart does not drop LSPs of VMs whose node has not re-registered yet
	nics, names := r.net.AllNICs(), r.net.Names()
	r.mu.Lock()
	for id, vm := range r.vms {
		names[id] = vm.Name
	}
	r.mu.Unlock()
	for id, list := range nics {
		for _, n := range list {
			hostname := ""
			if names[id] != "" && netNames[n.Network] != "" {
				hostname = names[id] + "." + netNames[n.Network] + ".internal"
			}
			st.Ports = append(st.Ports, GatewayPort{Name: n.Port, MAC: n.MAC, IP: n.IP, Hostname: hostname, Network: n.Network})
		}
	}
	sort.Slice(st.Ports, func(i, j int) bool {
		a, b := st.Ports[i], st.Ports[j]
		return a.Hostname < b.Hostname || a.Hostname == b.Hostname && a.Name < b.Name
	})
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
Endpoint = %s
AllowedIPs = 10.200.0.0/16, 10.201.0.0/24
PersistentKeepalive = 25
`, d.IP, d.PrivateKey, mtu, clientsPub, net.JoinHostPort(endpoint, strconv.Itoa(port)))
}

func (r *Registry) clientsPublicKey() (string, error) {
	_, priv := r.net.GatewayKeys()
	return wgkey.Public(priv)
}
