package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInitConfigCreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "etc", "node.json")
	o := InitOptions{Path: path, HubURL: "http://h:8080", Token: "secret-token", Name: "n1", Provider: "firecracker", MemoryMB: 1500}
	if err := InitConfig(o, "/var/lib/vm-manager/vms", "/var/lib/vm-manager/images"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", st.Mode().Perm())
	}
	m := readJSON(t, path)
	hub := m["hub"].(map[string]any)
	paths := m["paths"].(map[string]any)
	if m["nodeId"] != "n1" || m["provider"] != "firecracker" || hub["url"] != "ws://h:8080/api/nodes/connect" || hub["token"] != "secret-token" ||
		paths["vms"] != "/var/lib/vm-manager/vms" || paths["images"] != "/var/lib/vm-manager/images" ||
		m["capacity"].(map[string]any)["memoryMB"] != float64(1500) {
		t.Fatalf("config = %v", m)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("written config does not load: %v", err)
	}
}

func TestInitConfigUpdatesOnlyGivenFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.json")
	old := `{"nodeId":"old","hub":{"url":"ws://old:1/api/nodes/connect","token":"old","extra":1},"provider":"vz",
"paths":{"vms":"/data/vms","images":"/data/img"},"capacity":{"cpus":3,"memoryMB":999},"vz":{"imageURL":"http://img"},"custom":[1,2]}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	o := InitOptions{Path: path, HubURL: "https://hub.example:8443", Token: "new", Name: "n1", Provider: "vz", UnderlayMTU: 1200}
	if err := InitConfig(o, "/x/vms", "/x/images"); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, path)
	hub := m["hub"].(map[string]any)
	if m["nodeId"] != "n1" || hub["url"] != "wss://hub.example:8443/api/nodes/connect" || hub["token"] != "new" || hub["extra"] != float64(1) {
		t.Fatalf("hub = %v", m)
	}
	if p := m["paths"].(map[string]any); p["vms"] != "/data/vms" || p["images"] != "/data/img" {
		t.Fatalf("paths changed: %v", p)
	}
	if c := m["capacity"].(map[string]any); c["cpus"] != float64(3) || c["memoryMB"] != float64(999) {
		t.Fatalf("capacity changed: %v", c)
	}
	if m["vz"].(map[string]any)["imageURL"] != "http://img" || m["custom"] == nil || m["underlay"].(map[string]any)["mtu"] != float64(1200) {
		t.Fatalf("other keys lost: %v", m)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", st.Mode().Perm())
	}
}

func TestInitConfigRejectsBadHub(t *testing.T) {
	for _, hub := range []string{"ftp://x:1", "http://x", "x:8080", ""} {
		o := InitOptions{Path: filepath.Join(t.TempDir(), "n.json"), HubURL: hub, Token: "t", Name: "n", Provider: "vz"}
		if err := InitConfig(o, "a", "b"); err == nil {
			t.Errorf("%q accepted", hub)
		}
	}
}
