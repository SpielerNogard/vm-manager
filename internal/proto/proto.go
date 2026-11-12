// Package proto defines the messages exchanged between hub and nodes.
package proto

import (
	"encoding/json"
	"io"
)

type VMState string

const (
	StatePending  VMState = "pending"
	StateStarting VMState = "starting"
	StateRunning  VMState = "running"
	StateStopped  VMState = "stopped"
	StateError    VMState = "error"
	StateUnknown  VMState = "unknown"
)

type VMSpec struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	CPUs          int              `json:"cpus"`
	MemoryMB      int              `json:"memoryMB"`
	DiskGB        int              `json:"diskGB"`
	SSHPublicKeys []string         `json:"sshPublicKeys"`
	NICs          []NIC            `json:"nics"`
	Attachments   []DiskAttachment `json:"attachments,omitempty"`
}

type DiskAttachment struct {
	DiskID    string `json:"diskId"`
	MountPath string `json:"mountPath"`
	SizeGB    int    `json:"sizeGB"`        // size the guest should see; a change restarts the VM
	URL       string `json:"url,omitempty"` // set by the hub in DesiredVM only
}

// NIC is one virtual network interface; Port is the OVN logical switch port name.
type NIC struct {
	Network string `json:"network"`
	MAC     string `json:"mac"`
	IP      string `json:"ip"`
	Port    string `json:"port"`
}

// Disk is a block device the node attached to the VM; Name is "root" or "seed".
type Disk struct {
	Name           string `json:"name"`
	File           string `json:"file"`
	Device         string `json:"device"`
	ReadOnly       bool   `json:"readOnly"`
	SizeBytes      int64  `json:"sizeBytes"`
	AllocatedBytes int64  `json:"allocatedBytes"`
	Error          string `json:"error,omitempty"`
}

type VMStatus struct {
	ID    string  `json:"id"`
	State VMState `json:"state"`
	IP    string  `json:"ip,omitempty"`
	Error string  `json:"error,omitempty"`
	Disks []Disk  `json:"disks,omitempty"`
	// Generation echoes the DesiredVM generation this status reflects.
	Generation uint64 `json:"generation,omitempty"`
}

type Power string

const (
	PowerRunning Power = "running"
	PowerStopped Power = "stopped"
)

type StopMode string

const (
	StopGraceful StopMode = "graceful"
	StopHard     StopMode = "hard"
)

const ErrVMDirMissing = "VM directory missing"

type DesiredVM struct {
	Spec        VMSpec   `json:"spec"`
	Power       Power    `json:"power"`
	StopMode    StopMode `json:"stopMode,omitempty"`
	Generation  uint64   `json:"generation"`
	Provisioned bool     `json:"provisioned"`
}

type Register struct {
	NodeID   string     `json:"nodeId"`
	Hostname string     `json:"hostname"`
	Provider string     `json:"provider"`
	CPUs     int        `json:"cpus"`
	MemoryMB int        `json:"memoryMB"`
	VMs      []VMStatus `json:"vms,omitempty"`
	// Config is the node's config file with the hub token removed; shown in the GUI only.
	Config     json.RawMessage `json:"config,omitempty"`
	Machine    *Machine        `json:"machine,omitempty"`
	ChassisKey string          `json:"chassisKey,omitempty"`
	DisksInUse []string        `json:"disksInUse,omitempty"`
	AppliedSeq uint64          `json:"appliedSeq,omitempty"` // seq of the last desired list the node fully reconciled
}

// Underlay tells a node how to join the hub's WireGuard underlay and reach the OVN southbound DB.
type Underlay struct {
	Address  string `json:"address"`
	HubKey   string `json:"hubKey"`
	SBRemote string `json:"sbRemote"`
	Port     int    `json:"port"`
}

type Machine struct {
	OS        string `json:"os"`
	OSVersion string `json:"osVersion,omitempty"`
	Arch      string `json:"arch"`
	CPUModel  string `json:"cpuModel,omitempty"`
	CPUs      int    `json:"cpus"`
	MemoryMB  int    `json:"memoryMB,omitempty"`
}

// Message types: "register" (node→hub), "desired"/"underlay" (hub→node), "status" and "disks" (node→hub).
type Message struct {
	Type     string    `json:"type"`
	Register *Register `json:"register,omitempty"`
	Status   *VMStatus `json:"status,omitempty"`
	Underlay *Underlay `json:"underlay,omitempty"`
	// Desired (type "desired") is the full VM list for the node; nil means no VMs.
	Desired    []DesiredVM `json:"desired,omitempty"`
	DisksInUse []string    `json:"disksInUse,omitempty"` // type "disks": every disk ID a local VM still uses
	Seq        uint64      `json:"seq,omitempty"`        // type "desired": increases with every list sent to the node
	AppliedSeq uint64      `json:"appliedSeq,omitempty"` // type "disks": seq of the last desired list the node fully reconciled
}

func NewEncoder(w io.Writer) *json.Encoder { return json.NewEncoder(w) }

func StorageFileName(diskID string) string { return diskID + ".img" }

type StorageFile struct {
	Name           string `json:"name"`
	SizeBytes      int64  `json:"sizeBytes"`
	AllocatedBytes int64  `json:"allocatedBytes"`
}

type StorageDisk struct {
	ID        string `json:"id"`
	SizeBytes int64  `json:"sizeBytes"`
	CopyFrom  string `json:"copyFrom,omitempty"`
}

// StorageMessage types: "register" and "report" (agent→hub), "desired" (hub→agent).
type StorageMessage struct {
	Type     string        `json:"type"`
	Endpoint string        `json:"endpoint,omitempty"` // register: host:port nodes use for NBD
	Files    []StorageFile `json:"files,omitempty"`
	Disks    []StorageDisk `json:"disks,omitempty"`
	Deleted  []string      `json:"deleted,omitempty"` // disk IDs whose files must be removed
}
