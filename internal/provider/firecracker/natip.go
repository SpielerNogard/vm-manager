package firecracker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	natFile    = "nat-ip"
	natGateway = "192.168.64.1"
)

// allocNATIP keeps the VM's eth0 address in its VM dir, so deleting the VM frees it; callers serialize calls.
func allocNATIP(vmsDir, vmID string) (string, error) {
	own := filepath.Join(vmsDir, vmID, natFile)
	if b, err := os.ReadFile(own); err == nil {
		return strings.TrimSpace(string(b)), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	used := map[string]bool{}
	others, err := filepath.Glob(filepath.Join(vmsDir, "*", natFile))
	if err != nil {
		return "", err
	}
	for _, p := range others {
		if b, err := os.ReadFile(p); err == nil {
			used[strings.TrimSpace(string(b))] = true
		}
	}
	for i := 2; i <= 254; i++ {
		ip := fmt.Sprintf("192.168.64.%d", i)
		if used[ip] {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(own), 0o755); err != nil {
			return "", err
		}
		return ip, os.WriteFile(own, []byte(ip+"\n"), 0o644)
	}
	return "", errors.New("no free NAT address in 192.168.64.0/24")
}
