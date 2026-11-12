// Package hub holds the hub's in-memory state, its node transport and REST API.
package hub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"regexp"
	"slices"
	"sort"
	"strconv"
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
	Close() error
}

const (
	NodePending = "pending"
	NodeOnline  = "online"
	NodeOffline = "offline"
)

type Node struct {
	ID           string `json:"id"`
	Hostname     string `json:"hostname"`
	Provider     string `json:"provider"`
	CPUs         int    `json:"cpus"`
	MemoryMB     int    `json:"memoryMB"`
	FreeCPUs     int    `json:"freeCpus"`
	FreeMemoryMB int    `json:"freeMemoryMB"`
	// State is NodePending (created, never registered), NodeOnline or NodeOffline.
	State string `json:"state"`
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
	NodeID     string   `json:"nodeId"`
	Name       string   `json:"name"`
	CPUs       int      `json:"cpus"`
	MemoryMB   int      `json:"memoryMB"`
	DiskGB     int      `json:"diskGB"`
	SSHKeyIDs  []string `json:"sshKeyIds"`
	NetworkIDs []string `json:"networkIds"`
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
	cfg   *ConfigStore
	// sendMu (per node, kept across reconnects and node deletion so ordering survives re-registration) orders desired lists so a node never receives an older one after a newer one.
	sendMu map[string]*sync.Mutex
}

func NewRegistry(net *NetModel, store *VMStore, cfg *ConfigStore) *Registry {
	r := &Registry{nodes: map[string]*nodeEntry{}, vms: map[string]*VM{}, net: net, store: store, cfg: cfg, sendMu: map[string]*sync.Mutex{}}
	for _, n := range store.Nodes() {
		state := NodeOffline
		if n.Pending {
			state = NodePending
		}
		r.nodes[n.ID] = &nodeEntry{Node: Node{ID: n.ID, Hostname: n.Hostname, Provider: n.Provider, CPUs: n.CPUs, MemoryMB: n.MemoryMB, Machine: n.Machine, State: state}}
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
	// NICs without a VM (deleted pre-upgrade, or a crash between AllocNICs and PutVM) would keep OVN ports and duplicate DNS names.
	for id := range net.AllNICs() {
		if _, ok := r.vms[id]; ok {
			continue
		}
		if err := net.Release(id); err != nil {
			log.Printf("releasing orphan NICs of %s: %v", id, err)
		}
	}
	for _, c := range net.Chassis() {
		if _, ok := r.nodes[c.NodeID]; ok {
			continue
		}
		if err := net.ReleaseChassis(c.NodeID); err != nil {
			log.Printf("releasing orphan chassis of %s: %v", c.NodeID, err)
		}
	}
	return r
}

// NodeConnected records the node, takes over statuses of VMs the hub assigned to it and sends it its desired VMs.
// It refuses a node reporting VMs of another node, as its empty desired list would delete them.
func (r *Registry) NodeConnected(reg proto.Register, c NodeConn) error {
	underlay, err := r.nodeConnected(reg, c)
	if err != nil {
		return err
	}
	r.push(reg.NodeID)
	if underlay == nil {
		return nil
	}
	if err := c.Send(proto.Message{Type: "underlay", Underlay: underlay}); err != nil {
		log.Printf("node %s: sending underlay: %v", reg.NodeID, err)
	}
	return nil
}

func (r *Registry) nodeConnected(reg proto.Register, c NodeConn) (*proto.Underlay, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var foreign []string
	for _, st := range reg.VMs {
		if vm, ok := r.vms[st.ID]; ok && vm.NodeID != reg.NodeID {
			foreign = append(foreign, fmt.Sprintf("%s (owned by node %s)", st.ID, vm.NodeID))
		}
	}
	if len(foreign) > 0 {
		return nil, fmt.Errorf("reports VMs of other nodes: %s; was its nodeId or hostname changed?", strings.Join(foreign, ", "))
	}
	if err := r.store.PutNode(StoredNode{ID: reg.NodeID, Hostname: reg.Hostname, Provider: reg.Provider, CPUs: reg.CPUs,
		MemoryMB: reg.MemoryMB, Machine: reg.Machine}); err != nil {
		// A pending node must not go online while the store still says pending; the node retries on reconnect.
		if prev := r.nodes[reg.NodeID]; prev != nil && prev.State == NodePending {
			return nil, fmt.Errorf("saving node: %w", err)
		}
		log.Printf("node %s: saving: %v", reg.NodeID, err)
	}
	r.nodes[reg.NodeID] = &nodeEntry{
		Node: Node{ID: reg.NodeID, Hostname: reg.Hostname, Provider: reg.Provider, CPUs: reg.CPUs, MemoryMB: reg.MemoryMB, State: NodeOnline,
			ConnectedAt: time.Now().UTC(), Config: reg.Config, Machine: reg.Machine},
		conn: c,
	}
	for _, st := range reg.VMs {
		if vm, ok := r.vms[st.ID]; ok && vm.NodeID == reg.NodeID {
			r.applyStatus(vm, st)
		}
	}
	if reg.ChassisKey == "" {
		return nil, nil
	}
	ch, err := r.net.Enroll(reg.NodeID, reg.ChassisKey)
	if err != nil {
		log.Printf("node %s: enrolling chassis: %v", reg.NodeID, err)
		return nil, nil
	}
	underlayPriv, _ := r.net.GatewayKeys()
	hubKey, err := wgkey.Public(underlayPriv)
	if err != nil {
		log.Printf("hub underlay key: %v", err)
		return nil, nil
	}
	return &proto.Underlay{Address: ch.IP + "/24", HubKey: hubKey, Port: r.cfg.Get().Underlay.Port, SBRemote: "tcp:10.198.0.1:6642"}, nil
}

// NodeDisconnected ignores c if the node has already reconnected with a newer connection.
func (r *Registry) NodeDisconnected(nodeID string, c NodeConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[nodeID]
	if !ok || n.conn != c {
		return
	}
	n.State, n.conn = NodeOffline, nil
	for _, vm := range r.vms {
		if vm.NodeID == nodeID {
			vm.State, vm.IP = proto.StateUnknown, ""
		}
	}
}

func (r *Registry) CreateNode(id string) (Node, error) {
	if !nameRe.MatchString(id) {
		return Node{}, fmt.Errorf("%w: id must match %s", ErrInvalid, nameRe)
	}
	if id == "connect" {
		return Node{}, fmt.Errorf("%w: name \"connect\" is reserved", ErrInvalid)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.nodes[id]; ok {
		return Node{}, fmt.Errorf("%w: node %q already exists", ErrConflict, id)
	}
	if err := r.store.PutNode(StoredNode{ID: id, Pending: true}); err != nil {
		return Node{}, err
	}
	n := &nodeEntry{Node: Node{ID: id, State: NodePending}}
	r.nodes[id] = n
	return n.Node, nil
}

// DeleteNode forgets the node and disconnects it if online; a still running node reconnects and registers anew.
func (r *Registry) DeleteNode(id string) error {
	r.mu.Lock()
	n, ok := r.nodes[id]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: node %q", ErrNotFound, id)
	}
	for _, vm := range r.vms {
		if vm.NodeID == id {
			r.mu.Unlock()
			return fmt.Errorf("%w: node %q still has VMs", ErrConflict, id)
		}
	}
	if err := r.store.DeleteNode(id); err != nil {
		r.mu.Unlock()
		return err
	}
	releaseErr := r.net.ReleaseChassis(id)
	delete(r.nodes, id)
	conn := n.conn
	r.mu.Unlock()
	if conn != nil {
		if err := conn.Close(); err != nil {
			log.Printf("node %s: closing connection: %v", id, err)
		}
	}
	return releaseErr
}

// NodeConfig renders a node.json for the node; host is how the node reaches the hub and defaults to clients.endpoint.
func (r *Registry) NodeConfig(id, host string) ([]byte, error) {
	r.mu.Lock()
	_, ok := r.nodes[id]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: node %q", ErrNotFound, id)
	}
	c := r.cfg.Get()
	if host == "" {
		host = c.Clients.Endpoint
	}
	if host == "" {
		return nil, fmt.Errorf("%w: setup incomplete: set the client endpoint in settings", ErrConflict)
	}
	if validEndpoint(host) != nil {
		return nil, fmt.Errorf("%w: host must be a host name or IP without scheme or port", ErrInvalid)
	}
	type hubConf struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	type pathsConf struct {
		VMs    string `json:"vms"`
		Images string `json:"images"`
	}
	return json.MarshalIndent(struct {
		NodeID   string    `json:"nodeId"`
		Hub      hubConf   `json:"hub"`
		Provider string    `json:"provider"`
		Paths    pathsConf `json:"paths"`
	}{
		NodeID:   id,
		Hub:      hubConf{URL: "ws://" + net.JoinHostPort(host, strconv.Itoa(c.Hub.Port)) + "/api/nodes/connect", Token: c.Token},
		Provider: "vz",
		Paths:    pathsConf{VMs: "~/.vm-manager/vms", Images: "~/.vm-manager/images"},
	}, "", "  ")
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
	if n.State == NodePending {
		r.mu.Unlock()
		return VM{}, fmt.Errorf("%w: node %q has not connected yet", ErrConflict, n.ID)
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
	nics, err := r.net.AllocNICs(id, req.Name, req.NetworkIDs)
	if err != nil {
		r.mu.Unlock()
		return VM{}, err
	}
	vm := &VM{
		VMSpec: proto.VMSpec{ID: id, Name: req.Name, CPUs: req.CPUs, MemoryMB: req.MemoryMB, DiskGB: req.DiskGB,
			SSHPublicKeys: req.SSHPublicKeys, NICs: nics},
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
func (r *Registry) bump(id string, f func(*VM)) (VM, error) {
	r.mu.Lock()
	vm, ok := r.vms[id]
	if !ok {
		r.mu.Unlock()
		return VM{}, ErrNotFound
	}
	out, err := r.bumpLocked(vm, f)
	r.mu.Unlock()
	if err != nil {
		return VM{}, err
	}
	r.push(out.NodeID)
	return out, nil
}

// bumpLocked applies f as a new generation and persists it without pushing; r.mu must be held.
func (r *Registry) bumpLocked(vm *VM, f func(*VM)) (VM, error) {
	prev := *vm
	f(vm)
	vm.generation++
	if err := r.store.PutVM(vm.stored()); err != nil {
		*vm = prev
		return VM{}, err
	}
	return *vm, nil
}

// SetVMNetworks replaces the VM's non-mgmt networks; a running VM must be restarted for that, so it needs restartAllowed.
func (r *Registry) SetVMNetworks(id string, networkIDs []string, restartAllowed bool) (VM, error) {
	r.mu.Lock()
	vm, ok := r.vms[id]
	if !ok {
		r.mu.Unlock()
		return VM{}, ErrNotFound
	}
	var prevIDs []string
	for _, n := range vm.NICs {
		if n.Network != mgmtNetwork {
			prevIDs = append(prevIDs, n.Network)
		}
	}
	if slices.Equal(slices.Sorted(slices.Values(prevIDs)), slices.Sorted(slices.Values(networkIDs))) {
		out := *vm
		r.mu.Unlock()
		return out, nil
	}
	if vm.Power == proto.PowerRunning && !restartAllowed {
		r.mu.Unlock()
		return VM{}, fmt.Errorf("%w: restart required: pass restartAllowed=true", ErrConflict)
	}
	nics, err := r.net.SetNetworkNICs(id, networkIDs)
	if err != nil {
		r.mu.Unlock()
		return VM{}, err
	}
	out, err := r.bumpLocked(vm, func(vm *VM) { vm.NICs = nics })
	if err != nil {
		if _, rerr := r.net.SetNetworkNICs(id, prevIDs); rerr != nil {
			log.Printf("vm %s: restoring networks: %v", id, rerr)
		}
		r.mu.Unlock()
		return VM{}, err
	}
	r.mu.Unlock()
	r.push(out.NodeID)
	return out, nil
}

func (r *Registry) SetPower(id string, power proto.Power, mode proto.StopMode) (VM, error) {
	return r.bump(id, func(vm *VM) {
		vm.Power, vm.stopMode = power, mode
		// Error survives a disconnect (state unknown), so this also covers starting while the node is offline.
		if power == proto.PowerRunning && vm.Error == proto.ErrVMDirMissing {
			vm.provisioned = false
		}
	})
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

// UpdateStatus ignores statuses of VMs that do not belong to nodeID.
func (r *Registry) UpdateStatus(nodeID string, st proto.VMStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if vm, ok := r.vms[st.ID]; ok && vm.NodeID == nodeID {
		r.applyStatus(vm, st)
	}
}

// applyStatus records a reported status; r.mu must be held.
func (r *Registry) applyStatus(vm *VM, st proto.VMStatus) {
	vm.State, vm.IP, vm.Error, vm.Disks, vm.applied = st.State, st.IP, st.Error, st.Disks, max(vm.applied, st.Generation)
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
	return nil
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
