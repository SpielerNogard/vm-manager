package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"vm-manager/internal/proto"
	"vm-manager/internal/wgkey"
)

const (
	mgmtNetwork  = "mgmt"
	mgmtFirst    = "10.200.0.10"
	mgmtLast     = "10.200.255.254"
	chassisFirst = "10.198.0.11"
	chassisLast  = "10.198.0.254"
	deviceFirst  = "10.201.0.2"
	deviceLast   = "10.201.0.254"

	autoNetworks = "10.210.0.0/16"
	// hostOffset leaves .1 for the OVN DHCP server_id and the low addresses unused.
	hostOffset = 10
)

var (
	privateRanges = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"),
	}
	reservedRanges = []netip.Prefix{
		netip.MustParsePrefix("10.198.0.0/24"), netip.MustParsePrefix("10.199.0.0/30"), netip.MustParsePrefix("10.200.0.0/16"),
		netip.MustParsePrefix("10.201.0.0/24"), netip.MustParsePrefix("192.168.64.0/24"),
	}
)

type Chassis struct {
	NodeID    string `json:"nodeId"`
	PublicKey string `json:"publicKey"`
	IP        string `json:"ip"`
}

type Device struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	IP         string    `json:"ip"`
	PublicKey  string    `json:"publicKey"`
	PrivateKey string    `json:"privateKey"`
	CreatedAt  time.Time `json:"createdAt"`
	// OnHub devices run on the hub machine itself and connect via loopback.
	OnHub bool `json:"onHub,omitempty"`
}

type Network struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Subnet    string    `json:"subnet"`
	CreatedAt time.Time `json:"createdAt"`
}

type netState struct {
	UnderlayKey string                 `json:"underlayKey"`
	ClientsKey  string                 `json:"clientsKey"`
	NICs        map[string][]proto.NIC `json:"nics"`
	Networks    []Network              `json:"networks"`
	// Names lets the gateway state (and DNS) be rebuilt after a hub restart before nodes re-register.
	Names   map[string]string `json:"names"`
	Chassis []Chassis         `json:"chassis"`
	Devices []Device          `json:"devices"`
}

// NetModel owns the hub's address allocations and gateway keys and persists them to a JSON file.
type NetModel struct {
	mu   sync.Mutex
	path string
	st   netState
}

func LoadNetModel(path string) (*NetModel, error) {
	m := &NetModel{path: path, st: netState{NICs: map[string][]proto.NIC{}, Names: map[string]string{}}}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &m.st); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if m.st.NICs == nil {
			m.st.NICs = map[string][]proto.NIC{}
		}
		if m.st.Names == nil {
			m.st.Names = map[string]string{}
		}
		if m.st.UnderlayKey != "" && m.st.ClientsKey != "" {
			return m, nil
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	if m.st.UnderlayKey, _, err = wgkey.Generate(); err != nil {
		return nil, err
	}
	if m.st.ClientsKey, _, err = wgkey.Generate(); err != nil {
		return nil, err
	}
	if err := m.save(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *NetModel) GatewayKeys() (underlayPriv, clientsPriv string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.UnderlayKey, m.st.ClientsKey
}

// mutate applies f to a copy of the state and only keeps it if saving succeeds.
func (m *NetModel) mutate(f func(st *netState) error) error {
	next := netState{
		UnderlayKey: m.st.UnderlayKey, ClientsKey: m.st.ClientsKey,
		NICs:     make(map[string][]proto.NIC, len(m.st.NICs)),
		Networks: slices.Clone(m.st.Networks),
		Names:    maps.Clone(m.st.Names),
		Chassis:  slices.Clone(m.st.Chassis), Devices: slices.Clone(m.st.Devices),
	}
	for id, n := range m.st.NICs {
		next.NICs[id] = n
	}
	if err := f(&next); err != nil {
		return err
	}
	prev := m.st
	m.st = next
	if err := m.save(); err != nil {
		m.st = prev
		return err
	}
	return nil
}

func (m *NetModel) save() error { return saveJSON(m.path, m.st) }

// nextFree returns the first address in [first, last] that taken does not contain.
func nextFree(first, last string, taken func(ip string) bool) (string, error) {
	for a, end := netip.MustParseAddr(first), netip.MustParseAddr(last); a.Compare(end) <= 0; a = a.Next() {
		if !taken(a.String()) {
			return a.String(), nil
		}
	}
	return "", fmt.Errorf("%w: no free address between %s and %s", ErrConflict, first, last)
}

// nicMAC pads the 4 IPv4 bytes to a 6-byte MAC the way the spec example does (10.200.0.20 -> 02:0a:c8:00:00:14).
func nicMAC(ip string) string {
	b := netip.MustParseAddr(ip).As4()
	return fmt.Sprintf("02:%02x:%02x:00:%02x:%02x", b[0], b[1], b[2], b[3])
}

func ipOwner(st *netState, ip string) string {
	for id, nics := range st.NICs {
		if slices.ContainsFunc(nics, func(n proto.NIC) bool { return n.IP == ip }) {
			return id
		}
	}
	return ""
}

func networkByID(st *netState, id string) (Network, bool) {
	i := slices.IndexFunc(st.Networks, func(n Network) bool { return n.ID == id })
	if i < 0 {
		return Network{}, false
	}
	return st.Networks[i], true
}

func allocNetworkNIC(st *netState, vmID string, n Network) (proto.NIC, error) {
	p := netip.MustParsePrefix(n.Subnet)
	base := p.Addr()
	for i := 0; i < hostOffset; i++ {
		base = base.Next()
	}
	last := lastAddr(p).Prev()
	ip, err := nextFree(base.String(), last.String(), func(ip string) bool { return ipOwner(st, ip) != "" })
	if err != nil {
		return proto.NIC{}, err
	}
	return proto.NIC{Network: n.ID, IP: ip, MAC: nicMAC(ip), Port: vmID + "-" + n.ID}, nil
}

func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]) | host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

func validNetworkIDs(st *netState, ids []string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if _, ok := networkByID(st, id); !ok {
			return fmt.Errorf("%w: unknown network %q", ErrInvalid, id)
		}
		if seen[id] {
			return fmt.Errorf("%w: duplicate network %q", ErrInvalid, id)
		}
		seen[id] = true
	}
	return nil
}

func (m *NetModel) AllocNICs(vmID, name string, networkIDs []string) ([]proto.NIC, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []proto.NIC
	err := m.mutate(func(st *netState) error {
		if err := validNetworkIDs(st, networkIDs); err != nil {
			return err
		}
		ip, err := nextFree(mgmtFirst, mgmtLast, func(ip string) bool { return ipOwner(st, ip) != "" })
		if err != nil {
			return err
		}
		out = []proto.NIC{{Network: mgmtNetwork, IP: ip, MAC: nicMAC(ip), Port: vmID + "-mgmt"}}
		st.NICs[vmID] = out
		for _, id := range networkIDs {
			n, _ := networkByID(st, id)
			nic, err := allocNetworkNIC(st, vmID, n)
			if err != nil {
				return err
			}
			out = append(out, nic)
			st.NICs[vmID] = out
		}
		st.Names[vmID] = name
		return nil
	})
	return out, err
}

// SetNetworkNICs keeps the mgmt NIC and NICs of retained networks unchanged.
func (m *NetModel) SetNetworkNICs(vmID string, networkIDs []string) ([]proto.NIC, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []proto.NIC
	err := m.mutate(func(st *netState) error {
		cur, ok := st.NICs[vmID]
		if !ok {
			return fmt.Errorf("%w: vm %q has no NICs", ErrNotFound, vmID)
		}
		if err := validNetworkIDs(st, networkIDs); err != nil {
			return err
		}
		out = nil
		for _, nic := range cur {
			if nic.Network == mgmtNetwork {
				out = append(out, nic)
			}
		}
		st.NICs[vmID] = out
		for _, id := range networkIDs {
			if i := slices.IndexFunc(cur, func(n proto.NIC) bool { return n.Network == id }); i >= 0 {
				out = append(out, cur[i])
				st.NICs[vmID] = out
				continue
			}
			n, _ := networkByID(st, id)
			nic, err := allocNetworkNIC(st, vmID, n)
			if err != nil {
				return err
			}
			out = append(out, nic)
			st.NICs[vmID] = out
		}
		return nil
	})
	return out, err
}

func (m *NetModel) AddNetwork(name, subnet string) (Network, error) {
	if !nameRe.MatchString(name) || name == mgmtNetwork {
		return Network{}, fmt.Errorf("%w: network name must match %s and not be %q", ErrInvalid, nameRe, mgmtNetwork)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out Network
	err := m.mutate(func(st *netState) error {
		if slices.ContainsFunc(st.Networks, func(n Network) bool { return n.Name == name }) {
			return fmt.Errorf("%w: network %q already exists", ErrConflict, name)
		}
		overlapsExisting := func(p netip.Prefix) bool {
			return slices.ContainsFunc(st.Networks, func(n Network) bool { return netip.MustParsePrefix(n.Subnet).Overlaps(p) })
		}
		var p netip.Prefix
		if subnet == "" {
			pool := netip.MustParsePrefix(autoNetworks)
			p = netip.PrefixFrom(pool.Addr(), 24)
			for ; pool.Contains(p.Addr()); p = netip.PrefixFrom(lastAddr(p).Next(), 24) {
				if !overlapsExisting(p) {
					break
				}
			}
			if !pool.Contains(p.Addr()) {
				return fmt.Errorf("%w: no free /24 left in %s", ErrConflict, autoNetworks)
			}
		} else {
			var err error
			if p, err = validateSubnet(subnet); err != nil {
				return err
			}
			if overlapsExisting(p) {
				return fmt.Errorf("%w: subnet %s overlaps an existing network", ErrInvalid, p)
			}
		}
		out = Network{ID: newID(), Name: name, Subnet: p.String(), CreatedAt: time.Now().UTC()}
		st.Networks = append(st.Networks, out)
		return nil
	})
	return out, err
}

func validateSubnet(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("%w: subnet %q is not an IPv4 CIDR", ErrInvalid, s)
	}
	p = p.Masked()
	if p.Bits() < 16 || p.Bits() > 28 {
		return netip.Prefix{}, fmt.Errorf("%w: subnet prefix length must be between /16 and /28", ErrInvalid)
	}
	if !slices.ContainsFunc(privateRanges, func(r netip.Prefix) bool { return r.Contains(p.Addr()) }) {
		return netip.Prefix{}, fmt.Errorf("%w: subnet %s is not in a private range", ErrInvalid, p)
	}
	if i := slices.IndexFunc(reservedRanges, p.Overlaps); i >= 0 {
		return netip.Prefix{}, fmt.Errorf("%w: subnet %s overlaps reserved range %s", ErrInvalid, p, reservedRanges[i])
	}
	return p, nil
}

func (m *NetModel) DeleteNetwork(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mutate(func(st *netState) error {
		i := slices.IndexFunc(st.Networks, func(n Network) bool { return n.ID == id })
		if i < 0 {
			return fmt.Errorf("%w: network %q", ErrNotFound, id)
		}
		for vm, nics := range st.NICs {
			if slices.ContainsFunc(nics, func(n proto.NIC) bool { return n.Network == id }) {
				return fmt.Errorf("%w: network %q is used by vm %q", ErrConflict, id, vm)
			}
		}
		st.Networks = slices.Delete(st.Networks, i, i+1)
		return nil
	})
}

func (m *NetModel) Networks() []Network {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := slices.Clone(m.st.Networks)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *NetModel) Network(id string) (Network, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return networkByID(&m.st, id)
}

func (m *NetModel) NICs(vmID string) []proto.NIC {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.st.NICs[vmID])
}

func (m *NetModel) AllNICs() map[string][]proto.NIC {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string][]proto.NIC, len(m.st.NICs))
	for id, n := range m.st.NICs {
		out[id] = slices.Clone(n)
	}
	return out
}

// Names maps VM IDs to the VM names stored with their NICs.
func (m *NetModel) Names() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.st.Names)
}

func (m *NetModel) Release(vmID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.st.NICs[vmID]; !ok {
		return nil
	}
	return m.mutate(func(st *netState) error {
		delete(st.NICs, vmID)
		delete(st.Names, vmID)
		return nil
	})
}

func (m *NetModel) Enroll(nodeID, publicKey string) (Chassis, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out Chassis
	err := m.mutate(func(st *netState) error {
		if i := slices.IndexFunc(st.Chassis, func(c Chassis) bool { return c.NodeID == nodeID }); i >= 0 {
			st.Chassis[i].PublicKey = publicKey
			out = st.Chassis[i]
			return nil
		}
		ip, err := nextFree(chassisFirst, chassisLast, func(ip string) bool {
			return slices.ContainsFunc(st.Chassis, func(c Chassis) bool { return c.IP == ip })
		})
		if err != nil {
			return err
		}
		out = Chassis{NodeID: nodeID, PublicKey: publicKey, IP: ip}
		st.Chassis = append(st.Chassis, out)
		return nil
	})
	return out, err
}

func (m *NetModel) ReleaseChassis(nodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !slices.ContainsFunc(m.st.Chassis, func(c Chassis) bool { return c.NodeID == nodeID }) {
		return nil
	}
	return m.mutate(func(st *netState) error {
		st.Chassis = slices.DeleteFunc(st.Chassis, func(c Chassis) bool { return c.NodeID == nodeID })
		return nil
	})
}

func (m *NetModel) Chassis() []Chassis {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := slices.Clone(m.st.Chassis)
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func (m *NetModel) AddDevice(name string, onHub bool) (Device, error) {
	if !nameRe.MatchString(name) {
		return Device{}, fmt.Errorf("%w: name must match %s", ErrInvalid, nameRe)
	}
	priv, pub, err := wgkey.Generate()
	if err != nil {
		return Device{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out Device
	err = m.mutate(func(st *netState) error {
		ip, err := nextFree(deviceFirst, deviceLast, func(ip string) bool {
			return slices.ContainsFunc(st.Devices, func(d Device) bool { return d.IP == ip })
		})
		if err != nil {
			return err
		}
		out = Device{ID: newID(), Name: name, IP: ip, PublicKey: pub, PrivateKey: priv, CreatedAt: time.Now().UTC(), OnHub: onHub}
		st.Devices = append(st.Devices, out)
		return nil
	})
	return out, err
}

func (m *NetModel) DeleteDevice(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mutate(func(st *netState) error {
		i := slices.IndexFunc(st.Devices, func(d Device) bool { return d.ID == id })
		if i < 0 {
			return fmt.Errorf("%w: device %q", ErrNotFound, id)
		}
		st.Devices = slices.Delete(st.Devices, i, i+1)
		return nil
	})
}

func (m *NetModel) Devices() []Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.st.Devices)
}
