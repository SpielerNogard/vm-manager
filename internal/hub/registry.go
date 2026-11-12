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
	"path"
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
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Provider string `json:"provider"`
	// Error explains a node that registered with another hypervisor than it was created for.
	Error        string `json:"error,omitempty"`
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
	NodeID     string          `json:"nodeId"`
	Name       string          `json:"name"`
	CPUs       int             `json:"cpus"`
	MemoryMB   int             `json:"memoryMB"`
	DiskGB     int             `json:"diskGB"`
	SSHKeyIDs  []string        `json:"sshKeyIds"`
	NetworkIDs []string        `json:"networkIds"`
	Disks      []AttachRequest `json:"disks"`
	// SSHPublicKeys is resolved from SSHKeyIDs by the API.
	SSHPublicKeys []string `json:"-"`
}

type AttachRequest struct {
	DiskID    string `json:"diskId"`
	MountPath string `json:"mountPath"`
}

type nodeEntry struct {
	Node
	conn NodeConn
	seq  uint64 // of the last desired list sent
}

func (n *nodeEntry) stored() StoredNode {
	return StoredNode{ID: n.ID, Hostname: n.Hostname, Provider: n.Provider, CPUs: n.CPUs, MemoryMB: n.MemoryMB, Machine: n.Machine, DesiredSeq: n.seq,
		Pending: n.State == NodePending}
}

type Registry struct {
	mu    sync.Mutex
	nodes map[string]*nodeEntry
	vms   map[string]*VM
	net   *NetModel
	store *VMStore
	disks *DiskStore
	cfg   *ConfigStore
	// sendMu (per node, kept across reconnects and node deletion so ordering survives re-registration) orders desired lists so a node never receives an older one after a newer one.
	sendMu map[string]*sync.Mutex

	storage      StorageConn
	storageFiles []proto.StorageFile
	// storageReported is whether storageFiles came from the current connection, so an absent file means missing.
	storageReported bool
	storageSendMu   sync.Mutex
}

func NewRegistry(net *NetModel, store *VMStore, disks *DiskStore, cfg *ConfigStore) *Registry {
	r := &Registry{nodes: map[string]*nodeEntry{}, vms: map[string]*VM{}, net: net, store: store, disks: disks, cfg: cfg,
		sendMu: map[string]*sync.Mutex{}}
	for _, n := range store.Nodes() {
		state := NodeOffline
		if n.Pending {
			state = NodePending
		}
		r.nodes[n.ID] = &nodeEntry{Node: Node{ID: n.ID, Hostname: n.Hostname, Provider: n.Provider, CPUs: n.CPUs, MemoryMB: n.MemoryMB, Machine: n.Machine, State: state},
			seq: n.DesiredSeq}
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
	prev := r.nodes[reg.NodeID]
	n := &nodeEntry{
		Node: Node{ID: reg.NodeID, Hostname: reg.Hostname, Provider: reg.Provider, CPUs: reg.CPUs, MemoryMB: reg.MemoryMB, State: NodeOnline,
			ConnectedAt: time.Now().UTC(), Config: reg.Config, Machine: reg.Machine},
		conn: c,
	}
	if prev != nil {
		n.seq = prev.seq
		if prev.State == NodePending && prev.Provider != "" && prev.Provider != reg.Provider {
			n.Error = fmt.Sprintf("created for %s, registered with %s", prev.Provider, reg.Provider)
		}
	}
	if err := r.store.PutNode(n.stored()); err != nil {
		// A pending node must not go online while the store still says pending; the node retries on reconnect.
		if prev != nil && prev.State == NodePending {
			return nil, fmt.Errorf("saving node: %w", err)
		}
		log.Printf("node %s: saving: %v", reg.NodeID, err)
	}
	r.nodes[reg.NodeID] = n
	for _, st := range reg.VMs {
		if vm, ok := r.vms[st.ID]; ok && vm.NodeID == reg.NodeID {
			r.applyStatus(vm, st)
		}
	}
	r.disksInUseLocked(reg.NodeID, reg.DisksInUse, reg.AppliedSeq)
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

var providers = []string{"vz", "firecracker"}

func (r *Registry) CreateNode(id, provider string) (Node, error) {
	if !slices.Contains(providers, provider) {
		return Node{}, fmt.Errorf("%w: provider must be one of %s", ErrInvalid, strings.Join(providers, ", "))
	}
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
	if err := r.store.PutNode(StoredNode{ID: id, Provider: provider, Pending: true}); err != nil {
		return Node{}, err
	}
	n := &nodeEntry{Node: Node{ID: id, Provider: provider, State: NodePending}}
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

// nodeHub resolves the node's provider and the hub address it should use; host defaults to the client endpoint.
func (r *Registry) nodeHub(id, host string) (provider, hubAddr string, c Config, err error) {
	r.mu.Lock()
	n, ok := r.nodes[id]
	if ok {
		provider = n.Provider
	}
	r.mu.Unlock()
	if !ok {
		return "", "", c, fmt.Errorf("%w: node %q", ErrNotFound, id)
	}
	c = r.cfg.Get()
	if host == "" {
		host = c.Clients.Endpoint
	}
	if host == "" {
		return "", "", c, fmt.Errorf("%w: setup incomplete: set the client endpoint in settings", ErrConflict)
	}
	if validEndpoint(host) != nil {
		return "", "", c, fmt.Errorf("%w: host must be a host name or IP without scheme or port", ErrInvalid)
	}
	return provider, net.JoinHostPort(host, strconv.Itoa(c.Hub.Port)), c, nil
}

// InstallCommand is the one-line installer for the node, as shown in the GUI.
func (r *Registry) InstallCommand(id, host string) (string, error) {
	provider, hub, c, err := r.nodeHub(id, host)
	if err != nil {
		return "", err
	}
	if provider == "" {
		return "", fmt.Errorf("%w: node %q has no hypervisor; recreate it", ErrConflict, id)
	}
	return fmt.Sprintf("curl -fsSL http://%s/node/install.sh | sh -s -- --hub http://%s --token %s --name %s --provider %s",
		hub, hub, shellWord(c.Token), id, provider), nil
}

// shellWord quotes s for a POSIX shell unless it is plainly safe; tokens set in Settings may contain any non-space character.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// NodeConfig renders a node.json for the node; host is how the node reaches the hub and defaults to clients.endpoint.
func (r *Registry) NodeConfig(id, host string) ([]byte, error) {
	provider, hub, c, err := r.nodeHub(id, host)
	if err != nil {
		return nil, err
	}
	paths := "~/.vm-manager"
	if provider == "firecracker" {
		paths = "/var/lib/vm-manager"
	} else if provider == "" {
		provider = "vz"
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
		Hub:      hubConf{URL: "ws://" + hub + "/api/nodes/connect", Token: c.Token},
		Provider: provider,
		Paths:    pathsConf{VMs: paths + "/vms", Images: paths + "/images"},
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
	atts, err := r.attachmentsLocked(nil, req.Disks)
	if err != nil {
		r.mu.Unlock()
		return VM{}, err
	}
	id := newID()
	nics, err := r.net.AllocNICs(id, req.Name, req.NetworkIDs)
	if err != nil {
		r.mu.Unlock()
		return VM{}, err
	}
	vm := &VM{
		VMSpec: proto.VMSpec{ID: id, Name: req.Name, CPUs: req.CPUs, MemoryMB: req.MemoryMB, DiskGB: req.DiskGB,
			SSHPublicKeys: req.SSHPublicKeys, NICs: nics, Attachments: atts},
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
// Its disks stay releasing until the node confirms it no longer uses them.
func (r *Registry) DeleteVM(id string) error {
	r.mu.Lock()
	vm, ok := r.vms[id]
	if !ok {
		r.mu.Unlock()
		return ErrNotFound
	}
	diskIDs := attachedIDs(vm.Attachments)
	if err := r.disks.SetReleasing(diskIDs, vm.NodeID, r.nextSeqLocked(vm.NodeID)); err != nil {
		r.mu.Unlock()
		return err
	}
	if err := r.store.DeleteVM(id); err != nil {
		if rerr := r.disks.SetReleasing(diskIDs, "", 0); rerr != nil {
			log.Printf("vm %s: restoring disks: %v", id, rerr)
		}
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

// SetVMDisks replaces the VM's attachments; like networks, a running VM needs restartAllowed. Removed disks become releasing.
func (r *Registry) SetVMDisks(id string, disks []AttachRequest, restartAllowed bool) (VM, error) {
	if err := validateDisks(disks); err != nil {
		return VM{}, err
	}
	r.mu.Lock()
	vm, ok := r.vms[id]
	if !ok {
		r.mu.Unlock()
		return VM{}, ErrNotFound
	}
	atts, err := r.attachmentsLocked(vm, disks)
	if err != nil {
		r.mu.Unlock()
		return VM{}, err
	}
	if slices.Equal(atts, vm.Attachments) {
		out := *vm
		r.mu.Unlock()
		return out, nil
	}
	if vm.Power == proto.PowerRunning && !restartAllowed {
		r.mu.Unlock()
		return VM{}, fmt.Errorf("%w: restart required: pass restartAllowed=true", ErrConflict)
	}
	kept := attachedIDs(atts)
	var removed []string
	for _, a := range vm.Attachments {
		if !slices.Contains(kept, a.DiskID) {
			removed = append(removed, a.DiskID)
		}
	}
	if err := r.disks.SetReleasing(removed, vm.NodeID, r.nextSeqLocked(vm.NodeID)); err != nil {
		r.mu.Unlock()
		return VM{}, err
	}
	out, err := r.bumpLocked(vm, func(vm *VM) { vm.Attachments = atts })
	if err != nil {
		if rerr := r.disks.SetReleasing(removed, "", 0); rerr != nil {
			log.Printf("vm %s: restoring disks: %v", id, rerr)
		}
		r.mu.Unlock()
		return VM{}, err
	}
	r.mu.Unlock()
	r.push(out.NodeID)
	return out, nil
}

// attachmentsLocked resolves disks for vm (nil for a new VM); r.mu must be held.
func (r *Registry) attachmentsLocked(vm *VM, disks []AttachRequest) ([]proto.DiskAttachment, error) {
	var out []proto.DiskAttachment
	for _, q := range disks {
		d, ok := r.disks.Get(q.DiskID)
		if !ok {
			return nil, fmt.Errorf("%w: unknown disk %q", ErrInvalid, q.DiskID)
		}
		if d.ReleasingOn != "" {
			return nil, fmt.Errorf("%w: disk %q is still being released by node %q", ErrConflict, d.Name, d.ReleasingOn)
		}
		a := proto.DiskAttachment{DiskID: d.ID, MountPath: q.MountPath, SizeGB: d.SizeGB}
		// The guest must not see more than the file holds; the grow path raises it once the file is reported grown.
		if f, ok := r.storageFileLocked(d.ID); ok && f.SizeBytes < int64(d.SizeGB)<<30 {
			a.SizeGB = max(1, int(f.SizeBytes>>30))
		}
		owner, i := r.attachmentLocked(d.ID)
		if owner == nil && r.missingLocked(d) {
			return nil, fmt.Errorf("%w: disk %q is missing on the storage server", ErrConflict, d.Name)
		}
		if owner != nil {
			if owner != vm {
				return nil, fmt.Errorf("%w: disk %q is attached to VM %q", ErrConflict, d.Name, owner.Name)
			}
			// A grow in progress must keep waiting for the file before the guest sees the new size.
			a.SizeGB = owner.Attachments[i].SizeGB
		}
		out = append(out, a)
	}
	return out, nil
}

// attachmentLocked finds the VM a disk is attached to and the attachment's index; r.mu must be held.
func (r *Registry) attachmentLocked(diskID string) (*VM, int) {
	for _, vm := range r.vms {
		if i := slices.IndexFunc(vm.Attachments, func(a proto.DiskAttachment) bool { return a.DiskID == diskID }); i >= 0 {
			return vm, i
		}
	}
	return nil, -1
}

func attachedIDs(atts []proto.DiskAttachment) []string {
	var ids []string
	for _, a := range atts {
		ids = append(ids, a.DiskID)
	}
	return ids
}

// nextSeqLocked is the seq of the next desired list sent to the node; r.mu must be held.
func (r *Registry) nextSeqLocked(nodeID string) uint64 {
	if n := r.nodes[nodeID]; n != nil {
		return n.seq + 1
	}
	return 1
}

// DisksInUse ends the release of every disk releasing on nodeID that the node no longer uses.
// appliedSeq guards against a report from before the node saw the detach.
func (r *Registry) DisksInUse(nodeID string, ids []string, appliedSeq uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.disksInUseLocked(nodeID, ids, appliedSeq)
}

func (r *Registry) disksInUseLocked(nodeID string, ids []string, appliedSeq uint64) {
	var released []string
	for _, d := range r.disks.Disks() {
		if d.ReleasingOn == nodeID && appliedSeq >= d.ReleasingSeq && !slices.Contains(ids, d.ID) {
			released = append(released, d.ID)
		}
	}
	if err := r.disks.SetReleasing(released, "", 0); err != nil {
		log.Printf("node %s: releasing disks: %v", nodeID, err)
	}
}

// ForceRelease ends a release the node cannot confirm because it is offline; whatever it still writes is lost.
func (r *Registry) ForceRelease(diskID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.disks.Get(diskID)
	if !ok {
		return fmt.Errorf("%w: disk %q", ErrNotFound, diskID)
	}
	if d.ReleasingOn == "" {
		return fmt.Errorf("%w: disk %q is not being released", ErrConflict, d.Name)
	}
	if n := r.nodes[d.ReleasingOn]; n != nil && n.State == NodeOnline {
		return fmt.Errorf("%w: node %q is online and will confirm the release itself", ErrConflict, d.ReleasingOn)
	}
	return r.disks.SetReleasing([]string{diskID}, "", 0)
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
	ep := r.disks.Endpoint()
	for _, vm := range r.vms {
		if vm.NodeID != nodeID {
			continue
		}
		d := vm.desired()
		d.Spec.Attachments = slices.Clone(d.Spec.Attachments)
		for i, a := range d.Spec.Attachments {
			// Without an endpoint or a file the storage has reported, the node gets no URL and reports storage not ready.
			if sd, ok := r.disks.Get(a.DiskID); ep != "" && ok && sd.Seen {
				d.Spec.Attachments[i].URL = "nbd://" + ep + "/" + proto.StorageFileName(a.DiskID)
			}
		}
		out = append(out, d)
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
	n.seq++
	if err := r.store.PutNode(n.stored()); err != nil {
		n.seq--
		r.mu.Unlock()
		log.Printf("node %s: saving desired seq: %v", nodeID, err)
		return
	}
	conn, m := n.conn, proto.Message{Type: "desired", Desired: r.desiredFor(nodeID), Seq: n.seq}
	r.mu.Unlock()
	if err := conn.Send(m); err != nil {
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
	return validateDisks(q.Disks)
}

func validateDisks(disks []AttachRequest) error {
	ids, paths := map[string]bool{}, map[string]bool{}
	for _, d := range disks {
		switch {
		case !mountPathRe.MatchString(d.MountPath) || path.Clean(d.MountPath) != d.MountPath:
			return fmt.Errorf("%w: disk %q: mountPath must be a clean absolute path other than /", ErrInvalid, d.DiskID)
		case reservedMount(d.MountPath):
			return fmt.Errorf("%w: disk %q: mountPath %s is reserved by the system", ErrInvalid, d.DiskID, d.MountPath)
		case ids[d.DiskID]:
			return fmt.Errorf("%w: disk %q is listed twice", ErrInvalid, d.DiskID)
		case paths[d.MountPath]:
			return fmt.Errorf("%w: mountPath %q is used twice", ErrInvalid, d.MountPath)
		}
		ids[d.DiskID], paths[d.MountPath] = true, true
	}
	return nil
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
