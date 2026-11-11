// Package hub holds the hub's in-memory state, its node transport and REST API.
package hub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"vm-manager/internal/proto"
	"vm-manager/internal/wgkey"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid request")
	ErrConflict = errors.New("conflict")
)

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ext4 labels are limited to 16 bytes.
var diskNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,14}[a-z0-9])?$`)
var mountPathRe = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)

// Mounting here (or below, for the first list) would hide files the guest needs to boot or to let us in via SSH.
var (
	reservedMountTrees = []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/proc", "/run", "/sbin", "/sys", "/usr", "/var/lib/cloud", "/home/ubuntu/.ssh"}
	reservedMountPaths = []string{"/home", "/home/ubuntu", "/root", "/var", "/var/lib"}
)

func reservedMount(p string) bool {
	for _, t := range reservedMountTrees {
		if p == t || strings.HasPrefix(p, t+"/") {
			return true
		}
	}
	return slices.Contains(reservedMountPaths, p)
}

type NodeConn interface {
	Send(proto.Message) error
}

type Node struct {
	ID           string `json:"id"`
	Hostname     string `json:"hostname"`
	Provider     string `json:"provider"`
	CPUs         int    `json:"cpus"`
	MemoryMB     int    `json:"memoryMB"`
	FreeCPUs     int    `json:"freeCpus"`
	FreeMemoryMB int    `json:"freeMemoryMB"`
	Online       bool   `json:"online"`
	// ConnectedAt is when the node last registered.
	ConnectedAt time.Time       `json:"connectedAt"`
	Config      json.RawMessage `json:"config,omitempty"`
	Machine     *proto.Machine  `json:"machine,omitempty"`
	Chassis     *ChassisState   `json:"chassis,omitempty"`
}

type ChassisState struct {
	IP            string    `json:"ip"`
	LastHandshake time.Time `json:"lastHandshake,omitempty"`
	Registered    bool      `json:"registered"`
}

type VM struct {
	proto.VMSpec
	NodeID   string        `json:"nodeId"`
	Provider string        `json:"provider"`
	State    proto.VMState `json:"state"`
	IP       string        `json:"ip,omitempty"`
	Error    string        `json:"error,omitempty"`
}

type CreateVMRequest struct {
	NodeID    string           `json:"nodeId"`
	Name      string           `json:"name"`
	CPUs      int              `json:"cpus"`
	MemoryMB  int              `json:"memoryMB"`
	DiskGB    int              `json:"diskGB"`
	DataDisks []proto.DataDisk `json:"dataDisks"`
	SSHKeyIDs []string         `json:"sshKeyIds"`
	// SSHPublicKeys is resolved from SSHKeyIDs by the API.
	SSHPublicKeys []string `json:"-"`
}

type nodeEntry struct {
	Node
	conn NodeConn
}

type Registry struct {
	mu    sync.Mutex
	nodes map[string]*nodeEntry
	vms   map[string]*VM
	net   *NetModel
	cfg   Config
}

func NewRegistry(net *NetModel, cfg Config) *Registry {
	return &Registry{nodes: map[string]*nodeEntry{}, vms: map[string]*VM{}, net: net, cfg: cfg}
}

// NodeConnected adopts the reported VMs and drops ones the node no longer has.
// The hub's NICs win over reported ones; a reported VM the hub does not know keeps its NICs unless they clash.
func (r *Registry) NodeConnected(reg proto.Register, c NodeConn) {
	underlay := r.nodeConnected(reg, c)
	if underlay == nil {
		return
	}
	if err := c.Send(proto.Message{Type: "underlay", Underlay: underlay}); err != nil {
		log.Printf("node %s: sending underlay: %v", reg.NodeID, err)
	}
}

func (r *Registry) nodeConnected(reg proto.Register, c NodeConn) *proto.Underlay {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[reg.NodeID] = &nodeEntry{
		Node: Node{ID: reg.NodeID, Hostname: reg.Hostname, Provider: reg.Provider, CPUs: reg.CPUs, MemoryMB: reg.MemoryMB, Online: true,
			ConnectedAt: time.Now().UTC(), Config: reg.Config, Machine: reg.Machine},
		conn: c,
	}
	reported := map[string]bool{}
	for _, rec := range reg.VMs {
		id := rec.Spec.ID
		reported[id] = true
		vm, known := r.vms[id]
		if !known || vm.NodeID != reg.NodeID {
			vm = &VM{VMSpec: rec.Spec, NodeID: reg.NodeID}
			r.vms[id] = vm
		}
		vm.Provider = reg.Provider
		vm.State, vm.IP, vm.Error = rec.Status.State, rec.Status.IP, rec.Status.Error
		if nics := r.net.NICs(id); len(nics) > 0 {
			vm.NICs = nics
		} else if err := r.net.AdoptNICs(id, rec.Spec.NICs); err != nil {
			vm.NICs = nil
			vm.State, vm.Error = proto.StateError, adoptError(rec.Spec.NICs, err)
		} else {
			vm.NICs = rec.Spec.NICs
		}
	}
	for id, vm := range r.vms {
		if vm.NodeID == reg.NodeID && !reported[id] {
			delete(r.vms, id)
			if err := r.net.Release(id); err != nil {
				log.Printf("releasing NICs of %s: %v", id, err)
			}
		}
	}
	if reg.ChassisKey == "" {
		return nil
	}
	ch, err := r.net.Enroll(reg.NodeID, reg.ChassisKey)
	if err != nil {
		log.Printf("node %s: enrolling chassis: %v", reg.NodeID, err)
		return nil
	}
	underlayPriv, _ := r.net.GatewayKeys()
	hubKey, err := wgkey.Public(underlayPriv)
	if err != nil {
		log.Printf("hub underlay key: %v", err)
		return nil
	}
	return &proto.Underlay{Address: ch.IP + "/24", HubKey: hubKey, Port: r.cfg.Underlay.Port, SBRemote: "tcp:10.198.0.1:6642"}
}

func adoptError(nics []proto.NIC, err error) string {
	ip := ""
	if len(nics) > 0 {
		ip = nics[0].IP
	}
	if errors.Is(err, ErrInvalid) {
		return fmt.Sprintf("invalid mgmt IP %q", ip)
	}
	if errors.Is(err, ErrConflict) {
		return fmt.Sprintf("mgmt IP %s already in use", ip)
	}
	return err.Error()
}

// NodeDisconnected ignores c if the node has already reconnected with a newer connection.
func (r *Registry) NodeDisconnected(nodeID string, c NodeConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[nodeID]
	if !ok || n.conn != c {
		return
	}
	n.Online, n.conn = false, nil
	for _, vm := range r.vms {
		if vm.NodeID == nodeID {
			vm.State, vm.IP = proto.StateUnknown, ""
		}
	}
}

func (r *Registry) CreateVM(req CreateVMRequest) (VM, error) {
	if err := validate(req); err != nil {
		return VM{}, err
	}
	r.mu.Lock()
	n, ok := r.nodes[req.NodeID]
	if !ok {
		r.mu.Unlock()
		return VM{}, fmt.Errorf("%w: unknown node %q", ErrInvalid, req.NodeID)
	}
	for _, vm := range r.vms {
		if vm.Name == req.Name {
			r.mu.Unlock()
			return VM{}, fmt.Errorf("%w: name %q already in use", ErrInvalid, req.Name)
		}
	}
	if !n.Online {
		r.mu.Unlock()
		return VM{}, fmt.Errorf("%w: node %q is offline", ErrConflict, n.ID)
	}
	freeCPU, freeMem := r.free(n)
	if req.CPUs > freeCPU || req.MemoryMB > freeMem {
		r.mu.Unlock()
		return VM{}, fmt.Errorf("%w: node %q has %d CPUs / %d MB free", ErrConflict, n.ID, freeCPU, freeMem)
	}
	id := newID()
	nics, err := r.net.AllocNICs(id)
	if err != nil {
		r.mu.Unlock()
		return VM{}, err
	}
	vm := &VM{
		VMSpec: proto.VMSpec{ID: id, Name: req.Name, CPUs: req.CPUs, MemoryMB: req.MemoryMB, DiskGB: req.DiskGB,
			DataDisks: req.DataDisks, SSHPublicKeys: req.SSHPublicKeys, NICs: nics},
		NodeID: n.ID, Provider: n.Provider, State: proto.StatePending,
	}
	r.vms[id] = vm
	out, conn, spec := *vm, n.conn, vm.VMSpec
	r.mu.Unlock()

	if err := conn.Send(proto.Message{Type: "create", Create: &spec}); err != nil {
		r.mu.Lock()
		delete(r.vms, id)
		r.net.Release(id)
		r.mu.Unlock()
		return VM{}, fmt.Errorf("%w: sending to node: %v", ErrConflict, err)
	}
	return out, nil
}

func (r *Registry) DeleteVM(id string) error {
	r.mu.Lock()
	vm, ok := r.vms[id]
	if !ok {
		r.mu.Unlock()
		return ErrNotFound
	}
	n := r.nodes[vm.NodeID]
	if n == nil || !n.Online {
		r.mu.Unlock()
		return fmt.Errorf("%w: node %q is offline", ErrConflict, vm.NodeID)
	}
	delete(r.vms, id)
	if err := r.net.Release(id); err != nil {
		log.Printf("releasing NICs of %s: %v", id, err)
	}
	conn := n.conn
	r.mu.Unlock()
	return conn.Send(proto.Message{Type: "delete", DeleteID: id})
}

func (r *Registry) UpdateStatus(st proto.VMStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if vm, ok := r.vms[st.ID]; ok {
		vm.State, vm.IP, vm.Error = st.State, st.IP, st.Error
	}
}

func (r *Registry) Nodes() []Node {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Node, 0, len(r.nodes))
	for _, n := range r.nodes {
		node := n.Node
		node.FreeCPUs, node.FreeMemoryMB = r.free(n)
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Registry) VMs() []VM {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]VM, 0, len(r.vms))
	for _, vm := range r.vms {
		out = append(out, *vm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) free(n *nodeEntry) (cpus, memMB int) {
	cpus, memMB = n.CPUs, n.MemoryMB
	for _, vm := range r.vms {
		if vm.NodeID == n.ID {
			cpus -= vm.CPUs
			memMB -= vm.MemoryMB
		}
	}
	return cpus, memMB
}

func validate(q CreateVMRequest) error {
	switch {
	case !nameRe.MatchString(q.Name):
		return fmt.Errorf("%w: name must match %s", ErrInvalid, nameRe)
	case q.CPUs < 1:
		return fmt.Errorf("%w: cpus must be >= 1", ErrInvalid)
	case q.MemoryMB < 512:
		return fmt.Errorf("%w: memoryMB must be >= 512", ErrInvalid)
	case q.DiskGB < 4:
		return fmt.Errorf("%w: diskGB must be >= 4", ErrInvalid)
	case len(q.SSHPublicKeys) == 0:
		return fmt.Errorf("%w: at least one SSH key is required", ErrInvalid)
	}
	names, paths := map[string]bool{}, map[string]bool{}
	for _, d := range q.DataDisks {
		switch {
		case !diskNameRe.MatchString(d.Name):
			return fmt.Errorf("%w: disk name must match %s", ErrInvalid, diskNameRe)
		case !mountPathRe.MatchString(d.MountPath) || path.Clean(d.MountPath) != d.MountPath:
			return fmt.Errorf("%w: disk %q: mountPath must be a clean absolute path other than /", ErrInvalid, d.Name)
		case reservedMount(d.MountPath):
			return fmt.Errorf("%w: disk %q: mountPath %s is reserved by the system", ErrInvalid, d.Name, d.MountPath)
		case d.SizeGB < 1:
			return fmt.Errorf("%w: disk %q must be >= 1 GB", ErrInvalid, d.Name)
		case names[d.Name]:
			return fmt.Errorf("%w: disk name %q is used twice", ErrInvalid, d.Name)
		case paths[d.MountPath]:
			return fmt.Errorf("%w: mountPath %q is used twice", ErrInvalid, d.MountPath)
		}
		names[d.Name], paths[d.MountPath] = true, true
	}
	return nil
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
