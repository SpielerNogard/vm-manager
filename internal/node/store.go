// Package node runs on each VM host: it persists VMs, talks to the hub and drives a Provider.
package node

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"vm-manager/internal/proto"
)

type VMFile struct {
	Spec proto.VMSpec `json:"spec"`
	MAC  string       `json:"mac"`
}

func VMDir(vmsDir, id string) string { return filepath.Join(vmsDir, id) }

func SaveVM(vmsDir string, f VMFile) error {
	dir := VMDir(vmsDir, f.Spec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "vm.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "vm.json"))
}

func LoadVMs(vmsDir string) ([]VMFile, error) {
	entries, err := os.ReadDir(vmsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []VMFile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(vmsDir, e.Name(), "vm.json"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var f VMFile
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.ID < out[j].Spec.ID })
	return out, nil
}

func DeleteVMDir(vmsDir, id string) error { return os.RemoveAll(VMDir(vmsDir, id)) }
