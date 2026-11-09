// Package proto defines the messages exchanged between hub and nodes.
package proto

import (
	"bufio"
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
	ID           string `json:"id"`
	Name         string `json:"name"`
	CPUs         int    `json:"cpus"`
	MemoryMB     int    `json:"memoryMB"`
	DiskGB       int    `json:"diskGB"`
	DataDisksGB  []int  `json:"dataDisksGB,omitempty"`
	SSHPublicKey string `json:"sshPublicKey"`
	SSHPort      int    `json:"sshPort"`
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
}

// Message types: "register" (node→hub), "create"/"delete" (hub→node), "status" (node→hub).
type Message struct {
	Type     string    `json:"type"`
	Register *Register `json:"register,omitempty"`
	Create   *VMSpec   `json:"create,omitempty"`
	DeleteID string    `json:"deleteId,omitempty"`
	Status   *VMStatus `json:"status,omitempty"`
}

// StreamHeader is the first line of every hub→node data stream.
type StreamHeader struct {
	VMID string `json:"vmId"`
}

func NewEncoder(w io.Writer) *json.Encoder { return json.NewEncoder(w) }

func WriteHeader(w io.Writer, h StreamHeader) error { return json.NewEncoder(w).Encode(h) }

// ReadHeader reads exactly one line so the rest of r stays untouched for the payload.
func ReadHeader(r *bufio.Reader) (StreamHeader, error) {
	var h StreamHeader
	line, err := r.ReadBytes('\n')
	if err != nil {
		return h, err
	}
	err = json.Unmarshal(line, &h)
	return h, err
}
