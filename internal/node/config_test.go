package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fullConfig = `{
  "nodeId": "mac-mini-1",
  "hub": {"url": "ws://hub:8080/api/nodes/connect", "token": "t"},
  "provider": "vz",
  "paths": {"vms": "~/vmm/vms", "images": "/data/images"},
  "capacity": {"cpus": 6, "memoryMB": 12288},
  "vz": {"imageURL": "https://example.com/img", "leasesPath": "/tmp/leases"}
}`

func writeConfig(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "node.json")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfig(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, fullConfig))
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if c.NodeID != "mac-mini-1" || c.Hub.URL != "ws://hub:8080/api/nodes/connect" || c.Hub.Token != "t" || c.Provider != "vz" ||
		c.Paths.VMs != filepath.Join(home, "vmm/vms") || c.Paths.Images != "/data/images" || c.Capacity.CPUs != 6 || c.Capacity.MemoryMB != 12288 {
		t.Fatalf("config = %+v", c)
	}
	var vz struct{ ImageURL, LeasesPath string }
	if err := c.ProviderSection(&vz); err != nil || vz.ImageURL != "https://example.com/img" || vz.LeasesPath != "/tmp/leases" {
		t.Fatalf("vz section = %+v err %v", vz, err)
	}
}

func TestLoadConfigMissingRequired(t *testing.T) {
	for _, field := range []string{"hub.url", "hub.token", "provider", "paths.vms", "paths.images"} {
		body := fullConfig
		switch field {
		case "hub.url":
			body = strings.Replace(body, `"url": "ws://hub:8080/api/nodes/connect", `, "", 1)
		case "hub.token":
			body = strings.Replace(body, `, "token": "t"`, "", 1)
		case "provider":
			body = strings.Replace(body, `"provider": "vz",`, "", 1)
		case "paths.vms":
			body = strings.Replace(body, `"vms": "~/vmm/vms", `, "", 1)
		case "paths.images":
			body = strings.Replace(body, `, "images": "/data/images"`, "", 1)
		}
		_, err := LoadConfig(writeConfig(t, body))
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("%s: err = %v", field, err)
		}
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, `{"hub":{"url":"ws://h","token":"t"},"provider":"vz","paths":{"vms":"/a","images":"/b"}}`))
	if err != nil {
		t.Fatal(err)
	}
	host, _ := os.Hostname()
	if c.NodeID != host || c.Capacity.CPUs <= 0 || c.Capacity.MemoryMB <= 0 {
		t.Fatalf("defaults = %+v", c)
	}
	var vz struct{ ImageURL string }
	if err := c.ProviderSection(&vz); err != nil || vz.ImageURL != "" {
		t.Fatalf("missing section: %+v %v", vz, err)
	}
}

func TestConfigPublicDropsToken(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, fullConfig))
	if err != nil {
		t.Fatal(err)
	}
	pub, err := c.Public()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]any
	json.Unmarshal(pub, &got)
	if _, ok := got["hub"]["token"]; ok || got["hub"]["url"] != "ws://hub:8080/api/nodes/connect" || got["vz"]["imageURL"] != "https://example.com/img" {
		t.Fatalf("public = %s", pub)
	}
	if strings.Contains(string(pub), `"t"`) {
		t.Fatalf("token leaked: %s", pub)
	}
}

func TestLoadConfigUnderlay(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, fullConfig))
	if err != nil || c.Underlay.MTU != 1420 || c.Underlay.Endpoint != "" {
		t.Fatalf("defaults: %+v %v", c.Underlay, err)
	}
	c, err = LoadConfig(writeConfig(t, strings.Replace(fullConfig, `"vz"`, `"vz", "underlay": {"endpoint": "1.2.3.4:6000", "mtu": 1400}`, 1)))
	if err != nil || c.Underlay.MTU != 1400 || c.Underlay.Endpoint != "1.2.3.4:6000" {
		t.Fatalf("set: %+v %v", c.Underlay, err)
	}
}
