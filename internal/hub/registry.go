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
	IP            string     `json:"ip"`
	LastHandshake *time.Time `json:"lastHandshake,omitempty"`
	Registered    bool       `json:"registered"`
}

type VM struct {
	proto.VMSpec
	NodeID   string        `json:"nodeId"`
	Provider string        `json:"provider"`
	State    proto.VMState `json:"state"`
	IP       string        `json:"ip,omitempty"`
	Error    string        `json:"error,omitempty"`
	Disks    []proto.Disk  `json:"disks,omitempty"`
	Power    proto.Power   `json:"power"`

	stopMode    proto.StopMode
	generation  uint64
	applied     uint64 // generation the node last reported
	provisioned bool
}

func (vm *VM) stored() StoredVM {
	return StoredVM{Spec: vm.VMSpec, NodeID: vm.NodeID, Provider: vm.Provider, Power: vm.Power, StopMode: vm.stopMode,
		Generation: vm.generation, Provisioned: vm.provisioned}
}

func (vm *VM) desired() proto.DesiredVM {
	return proto.DesiredVM{Spec: vm.VMSpec, Power: vm.Power, StopMode: vm.stopMode, Generation: vm.generation, Provisioned: vm.provisioned}
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
	store *VMStore
	cfg   Config
	// sendMu (per node, kept across reconnects) orders desired lists so a node never receives an older one after a newer one.
	sendMu map[string]*sync.Mutex
}

func NewRegistry(net *NetModel, store *VMStore, cfg Config) *Registry {
	r := &Registry{nodes: map[string]*nodeEntry{}, vms: map[string]*VM{}, net: net, store: store, cfg: cfg, sendMu: map[string]*sync.Mutex{}}
	for _, n := range store.Nodes() {
		r.nodes[n.ID] = &nodeEntry{Node: Node{ID: n.ID, Hostname: n.Hostname, Provider: n.Provider, CPUs: n.CPUs, MemoryMB: n.MemoryMB, Machine: n.Machine}}
	}
	for _, s := range store.VMs() {
		vm := &VM{VMSpec: s.Spec, NodeID: s.NodeID, Provider: s.Provider, State: proto.StateUnknown, Power: s.Power,
			stopMode: s.StopMode, generation: s.Generation, provisioned: s.Provisioned}
		// Only a provisioned VM is known to have been applied; anything else stays pending until reported.
		if s.Provisioned {
			vm.applied = s.Generation
		}
		r.vms[s.Spec.ID] = vm
	}
	return r
}

// NodeConnected records the node, takes over statuses of VMs the hub assigned to it and sends it its desired VMs.
func (r *Registry) NodeConnected(reg proto.Register, c NodeConn) {
	underlay := r.nodeConnected(reg, c)
	r.push(reg.NodeID)
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
	if err := r.store.PutNode(StoredNode{ID: reg.NodeID, Hostname: reg.Hostname, Provider: reg.Provider, CPUs: reg.CPUs,
		MemoryMB: reg.MemoryMB, Machine: reg.Machine}); err != nil {
		log.Printf("node %s: saving: %v", reg.NodeID, err)
	}
	for _, st := range reg.VMs {
		if vm, ok := r.vms[st.ID]; ok && vm.NodeID == reg.NodeID {
			r.applyStatus(vm, st)
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
	freeCPU, freeMem := r.free(n)
	if req.CPUs > freeCPU || req.MemoryMB > freeMem {
		r.mu.Unlock()
		return VM{}, fmt.Errorf("%w: node %q has %d CPUs / %d MB free", ErrConflict, n.ID, freeCPU, freeMem)
	}
	id := newID()
	nics, err := r.net.AllocNICs(id, req.Name)
	if err != nil {
		r.mu.Unlock()
		return VM{}, err
	}
	vm := &VM{
		VMSpec: proto.VMSpec{ID: id, Name: req.Name, CPUs: req.CPUs, MemoryMB: req.MemoryMB, DiskGB: req.DiskGB,
			DataDisks: req.DataDisks, SSHPublicKeys: req.SSHPublicKeys, NICs: nics},
		NodeID: n.ID, Provider: n.Provider, State: proto.StatePending, Power: proto.PowerRunning, generation: 1,
	}
	if err := r.store.PutVM(vm.stored()); err != nil {
		r.release(id)
		r.mu.Unlock()
		return VM{}, err
	}
	r.vms[id] = vm
	out := *vm
	r.mu.Unlock()
	r.push(n.ID)
	return out, nil
}

// DeleteVM forgets the VM at once; an offline node deletes it as unknown when it reconnects.
func (r *Registry) DeleteVM(id string) error {
	r.mu.Lock()
	vm, ok := r.vms[id]
	if !ok {
		r.mu.Unlock()
		return ErrNotFound
	}
	if err := r.store.DeleteVM(id); err != nil {
		r.mu.Unlock()
		return err
	}
	delete(r.vms, id)
	r.release(id)
	r.mu.Unlock()
	r.push(vm.NodeID)
	return nil
}

func (r *Registry) release(id string) {
	if err := r.net.Release(id); err != nil {
		log.Printf("releasing NICs of %s: %v", id, err)
	}
}

// bump applies f to the VM's desired state as a new generation and sends it to the node.
func (r *Registry) bump(id string, f func(*VM)) error {
	r.mu.Lock()
	vm, ok := r.vms[id]
	if !ok {
		r.mu.Unlock()
		return ErrNotFound
	}
	prev := *vm
	f(vm)
	vm.generation++
	if err := r.store.PutVM(vm.stored()); err != nil {
		*vm = prev
		r.mu.Unlock()
		return err
	}
	nodeID := vm.NodeID
	r.mu.Unlock()
	r.push(nodeID)
	return nil
}

// desiredFor lists the node's desired VMs; r.mu must be held.
func (r *Registry) desiredFor(nodeID string) []proto.DesiredVM {
	var out []proto.DesiredVM
	for _, vm := range r.vms {
		if vm.NodeID == nodeID {
			out = append(out, vm.desired())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.ID < out[j].Spec.ID })
	return out
}

// push sends the node its current desired VMs if it is online.
func (r *Registry) push(nodeID string) {
	r.mu.Lock()
	sendMu := r.sendMu[nodeID]
	if sendMu == nil {
		sendMu = &sync.Mutex{}
		r.sendMu[nodeID] = sendMu
	}
	r.mu.Unlock()
	sendMu.Lock()
	defer sendMu.Unlock()
	r.mu.Lock()
	n := r.nodes[nodeID]
	if n == nil || n.conn == nil {
		r.mu.Unlock()
		return
	}
	conn, desired := n.conn, r.desiredFor(nodeID)
	r.mu.Unlock()
	if err := conn.Send(proto.Message{Type: "desired", Desired: desired}); err != nil {
		log.Printf("node %s: sending desired state: %v", nodeID, err)
	}
}

func (r *Registry) UpdateStatus(st proto.VMStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if vm, ok := r.vms[st.ID]; ok {
		r.applyStatus(vm, st)
	}
}

// applyStatus records a reported status; r.mu must be held.
func (r *Registry) applyStatus(vm *VM, st proto.VMStatus) {
	vm.State, vm.IP, vm.Error, vm.Disks, vm.applied = st.State, st.IP, st.Error, st.Disks, st.Generation
	if vm.provisioned || !slices.Contains([]proto.VMState{proto.StateStarting, proto.StateRunning, proto.StateStopped}, st.State) {
		return
	}
	vm.provisioned = true
	if err := r.store.PutVM(vm.stored()); err != nil {
		vm.provisioned = false
		log.Printf("vm %s: saving: %v", vm.ID, err)
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
		v := *vm
		if v.applied < v.generation {
			v.State = proto.StatePending
		}
		out = append(out, v)
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
