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
}

type netState struct {
	UnderlayKey string                 `json:"underlayKey"`
	ClientsKey  string                 `json:"clientsKey"`
	NICs        map[string][]proto.NIC `json:"nics"`
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
		NICs:    make(map[string][]proto.NIC, len(m.st.NICs)),
		Names:   maps.Clone(m.st.Names),
		Chassis: slices.Clone(m.st.Chassis), Devices: slices.Clone(m.st.Devices),
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

func mgmtIPOwner(st *netState, ip string) string {
	for id, nics := range st.NICs {
		if slices.ContainsFunc(nics, func(n proto.NIC) bool { return n.IP == ip }) {
			return id
		}
	}
	return ""
}

func (m *NetModel) AllocNICs(vmID, name string) ([]proto.NIC, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []proto.NIC
	err := m.mutate(func(st *netState) error {
		ip, err := nextFree(mgmtFirst, mgmtLast, func(ip string) bool { return mgmtIPOwner(st, ip) != "" })
		if err != nil {
			return err
		}
		out = []proto.NIC{{Network: mgmtNetwork, IP: ip, MAC: nicMAC(ip), Port: vmID + "-mgmt"}}
		st.NICs[vmID], st.Names[vmID] = out, name
		return nil
	})
	return out, err
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

func (m *NetModel) Chassis() []Chassis {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := slices.Clone(m.st.Chassis)
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func (m *NetModel) AddDevice(name string) (Device, error) {
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
		out = Device{ID: newID(), Name: name, IP: ip, PublicKey: pub, PrivateKey: priv, CreatedAt: time.Now().UTC()}
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
