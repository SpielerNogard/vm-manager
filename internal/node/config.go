package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Config struct {
	NodeID string `json:"nodeId"`
	Hub    struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	} `json:"hub"`
	Provider string `json:"provider"`
	Paths    struct {
		VMs    string `json:"vms"`
		Images string `json:"images"`
	} `json:"paths"`
	Capacity struct {
		CPUs     int `json:"cpus"`
		MemoryMB int `json:"memoryMB"`
	} `json:"capacity"`
	Raw map[string]json.RawMessage `json:"-"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if err := json.Unmarshal(b, &c.Raw); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	for field, v := range map[string]string{
		"hub.url": c.Hub.URL, "hub.token": c.Hub.Token, "provider": c.Provider,
		"paths.vms": c.Paths.VMs, "paths.images": c.Paths.Images,
	} {
		if v == "" {
			return c, fmt.Errorf("config: %s is required", field)
		}
	}
	home, _ := os.UserHomeDir()
	c.Paths.VMs, c.Paths.Images = expandHome(c.Paths.VMs, home), expandHome(c.Paths.Images, home)
	if c.NodeID == "" {
		c.NodeID, _ = os.Hostname()
	}
	if c.Capacity.CPUs == 0 {
		c.Capacity.CPUs = runtime.NumCPU()
	}
	if c.Capacity.MemoryMB == 0 {
		c.Capacity.MemoryMB = totalMemoryMB() - 4096
	}
	if c.Capacity.MemoryMB <= 0 {
		return c, fmt.Errorf("config: capacity.memoryMB is required on this platform")
	}
	return c, nil
}

// ProviderSection decodes the config block named after the provider; a missing block leaves v untouched.
func (c Config) ProviderSection(v any) error {
	raw, ok := c.Raw[c.Provider]
	if !ok {
		return nil
	}
	return json.Unmarshal(raw, v)
}

func expandHome(p, home string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[1:])
	}
	return p
}
