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
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	CPUs          int        `json:"cpus"`
	MemoryMB      int        `json:"memoryMB"`
	DiskGB        int        `json:"diskGB"`
	DataDisks     []DataDisk `json:"dataDisks,omitempty"`
	SSHPublicKeys []string   `json:"sshPublicKeys"`
	NICs          []NIC      `json:"nics"`
}

// NIC is one virtual network interface; Port is the OVN logical switch port name.
type NIC struct {
	Network string `json:"network"`
	MAC     string `json:"mac"`
	IP      string `json:"ip"`
	Port    string `json:"port"`
}

// DataDisk is formatted as ext4 labelled Name on first boot and mounted at MountPath.
type DataDisk struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	SizeGB    int    `json:"sizeGB"`
}

type VMStatus struct {
	ID    string  `json:"id"`
	State VMState `json:"state"`
	IP    string  `json:"ip,omitempty"`
	Error string  `json:"error,omitempty"`
}

type VMRecord struct {
	Spec   VMSpec   `json:"spec"`
	Status VMStatus `json:"status"`
}

type Register struct {
	NodeID   string     `json:"nodeId"`
	Hostname string     `json:"hostname"`
	Provider string     `json:"provider"`
	CPUs     int        `json:"cpus"`
	MemoryMB int        `json:"memoryMB"`
	VMs      []VMRecord `json:"vms,omitempty"`
	// Config is the node's config file with the hub token removed; shown in the GUI only.
	Config     json.RawMessage `json:"config,omitempty"`
	Machine    *Machine        `json:"machine,omitempty"`
	ChassisKey string          `json:"chassisKey,omitempty"`
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

// Message types: "register" (node→hub), "create"/"delete"/"underlay" (hub→node), "status" (node→hub).
type Message struct {
	Type     string    `json:"type"`
	Register *Register `json:"register,omitempty"`
	Create   *VMSpec   `json:"create,omitempty"`
	DeleteID string    `json:"deleteId,omitempty"`
	Status   *VMStatus `json:"status,omitempty"`
	Underlay *Underlay `json:"underlay,omitempty"`
}

func NewEncoder(w io.Writer) *json.Encoder { return json.NewEncoder(w) }

// Pipe copies both directions and closes both ends once either side finishes.
func Pipe(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	cp := func(dst io.Writer, src io.Reader) { io.Copy(dst, src); done <- struct{}{} }
	go cp(a, b)
	go cp(b, a)
	<-done
	a.Close()
	b.Close()
}
