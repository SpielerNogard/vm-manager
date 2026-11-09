// Package hub holds the hub's in-memory state, its node transport and REST API.
package hub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"vm-manager/internal/proto"
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
	OpenStream() (net.Conn, error)
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
	OnPortOpen  func(port int) error
	OnPortClose func(port int)

	mu               sync.Mutex
	portMin, portMax int
	nodes            map[string]*nodeEntry
	vms              map[string]*VM
	ports            map[int]string // port → VM ID
}

func NewRegistry(portMin, portMax int) *Registry {
	return &Registry{
		portMin: portMin, portMax: portMax,
		nodes:       map[string]*nodeEntry{},
		vms:         map[string]*VM{},
		ports:       map[int]string{},
		OnPortOpen:  func(int) error { return nil },
		OnPortClose: func(int) {},
	}
}

// NodeConnected adopts the reported VMs and drops ones the node no longer has.
func (r *Registry) NodeConnected(reg proto.Register, c NodeConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[reg.NodeID] = &nodeEntry{
		Node: Node{ID: reg.NodeID, Hostname: reg.Hostname, Provider: reg.Provider, CPUs: reg.CPUs, MemoryMB: reg.MemoryMB, Online: true,
			ConnectedAt: time.Now().UTC(), Config: reg.Config, Machine: reg.Machine},
		conn: c,
	}
	reported := map[string]bool{}
	for _, rec := range reg.VMs {
		reported[rec.Spec.ID] = true
		vm, known := r.vms[rec.Spec.ID]
		if !known || vm.NodeID != reg.NodeID {
			if known {
				r.releasePort(vm.SSHPort)
			}
			spec := rec.Spec
			spec.SSHPort = r.adoptPort(spec.ID, spec.SSHPort)
			vm = &VM{VMSpec: spec, NodeID: reg.NodeID}
			r.vms[spec.ID] = vm
		}
		vm.Provider = reg.Provider
		vm.State, vm.IP, vm.Error = rec.Status.State, rec.Status.IP, rec.Status.Error
	}
	for id, vm := range r.vms {
		if vm.NodeID == reg.NodeID && !reported[id] {
			r.releasePort(vm.SSHPort)
			delete(r.vms, id)
		}
	}
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
	port := r.allocPort(id)
	if port == 0 {
		r.mu.Unlock()
		return VM{}, fmt.Errorf("%w: no free SSH port", ErrConflict)
	}
	vm := &VM{
		VMSpec: proto.VMSpec{ID: id, Name: req.Name, CPUs: req.CPUs, MemoryMB: req.MemoryMB, DiskGB: req.DiskGB,
			DataDisks: req.DataDisks, SSHPublicKeys: req.SSHPublicKeys, SSHPort: port},
		NodeID: n.ID, Provider: n.Provider, State: proto.StatePending,
	}
	r.vms[id] = vm
	out, conn, spec := *vm, n.conn, vm.VMSpec
	r.mu.Unlock()

	if err := conn.Send(proto.Message{Type: "create", Create: &spec}); err != nil {
		r.mu.Lock()
		r.releasePort(port)
		delete(r.vms, id)
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
	r.releasePort(vm.SSHPort)
	delete(r.vms, id)
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

// StreamForPort opens a data stream to the node hosting the VM behind port and writes its header.
func (r *Registry) StreamForPort(port int) (net.Conn, error) {
	r.mu.Lock()
	vm, ok := r.vms[r.ports[port]]
	if !ok {
		r.mu.Unlock()
		return nil, ErrNotFound
	}
	n := r.nodes[vm.NodeID]
	if n == nil || !n.Online {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: node %q is offline", ErrConflict, vm.NodeID)
	}
	conn, id := n.conn, vm.ID
	r.mu.Unlock()

	s, err := conn.OpenStream()
	if err != nil {
		return nil, err
	}
	if err := proto.WriteHeader(s, proto.StreamHeader{VMID: id}); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
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

// adoptPort keeps a VM's previous port when possible, otherwise allocates a new one (0 if exhausted).
func (r *Registry) adoptPort(vmID string, want int) int {
	if want >= r.portMin && want <= r.portMax && r.ports[want] == "" && r.OnPortOpen(want) == nil {
		r.ports[want] = vmID
		return want
	}
	return r.allocPort(vmID)
}

// allocPort skips ports the OS refuses to bind (e.g. taken by another process).
func (r *Registry) allocPort(vmID string) int {
	for p := r.portMin; p <= r.portMax; p++ {
		if r.ports[p] == "" && r.OnPortOpen(p) == nil {
			r.ports[p] = vmID
			return p
		}
	}
	return 0
}

func (r *Registry) releasePort(p int) {
	if _, ok := r.ports[p]; ok {
		delete(r.ports, p)
		r.OnPortClose(p)
	}
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
