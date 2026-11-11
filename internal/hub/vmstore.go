package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"vm-manager/internal/proto"
)

type StoredVM struct {
	Spec        proto.VMSpec   `json:"spec"`
	NodeID      string         `json:"nodeId"`
	Provider    string         `json:"provider"`
	Power       proto.Power    `json:"power"`
	StopMode    proto.StopMode `json:"stopMode,omitempty"`
	Generation  uint64         `json:"generation"`
	Provisioned bool           `json:"provisioned"`
}

type StoredNode struct {
	ID       string         `json:"id"`
	Hostname string         `json:"hostname"`
	Provider string         `json:"provider"`
	CPUs     int            `json:"cpus"`
	MemoryMB int            `json:"memoryMB"`
	Machine  *proto.Machine `json:"machine,omitempty"`
}

type vmState struct {
	VMs   []StoredVM   `json:"vms"`
	Nodes []StoredNode `json:"nodes"`
}

// VMStore holds the hub's desired VMs and known nodes and persists them to a JSON file.
type VMStore struct {
	mu    sync.Mutex
	path  string
	vms   map[string]StoredVM
	nodes map[string]StoredNode
}

func LoadVMStore(path string) (*VMStore, error) {
	s := &VMStore{path: path, vms: map[string]StoredVM{}, nodes: map[string]StoredNode{}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	var st vmState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, vm := range st.VMs {
		s.vms[vm.Spec.ID] = vm
	}
	for _, n := range st.Nodes {
		s.nodes[n.ID] = n
	}
	return s, nil
}

func (s *VMStore) sortedVMs() []StoredVM {
	out := make([]StoredVM, 0, len(s.vms))
	for _, vm := range s.vms {
		out = append(out, vm)
	}
	slices.SortFunc(out, func(a, b StoredVM) int { return strings.Compare(a.Spec.ID, b.Spec.ID) })
	return out
}

func (s *VMStore) sortedNodes() []StoredNode {
	out := make([]StoredNode, 0, len(s.nodes))
	for _, n := range s.nodes {
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b StoredNode) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// mutate applies f and rolls the maps back if saving fails.
func (s *VMStore) mutate(f func()) error {
	prevVMs, prevNodes := maps.Clone(s.vms), maps.Clone(s.nodes)
	f()
	if err := saveJSON(s.path, vmState{VMs: s.sortedVMs(), Nodes: s.sortedNodes()}); err != nil {
		s.vms, s.nodes = prevVMs, prevNodes
		return err
	}
	return nil
}

func (s *VMStore) PutVM(vm StoredVM) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(func() { s.vms[vm.Spec.ID] = vm })
}

func (s *VMStore) DeleteVM(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(func() { delete(s.vms, id) })
}

func (s *VMStore) PutNode(n StoredNode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(func() { s.nodes[n.ID] = n })
}

func (s *VMStore) VMs() []StoredVM {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedVMs()
}

func (s *VMStore) Nodes() []StoredNode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedNodes()
}

// saveJSON writes via a temp file and rename so a crash never leaves a half-written file.
func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
