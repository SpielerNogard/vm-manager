package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hub.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadHubConfig(t *testing.T) {
	c, err := LoadConfig(writeFile(t, `{"token":"s3cret","sshPorts":{"min":1,"max":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.Token != "s3cret" || c.Underlay.Port != 51820 || c.Clients.Port != 51821 || c.GuestMTU != 1300 {
		t.Fatalf("config = %+v", c)
	}

	c, err = LoadConfig(writeFile(t, `{"listen":":9000","token":"x","underlay":{"port":1},"clients":{"endpoint":"vmm.lan","port":2},"guestMTU":1280}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9000" || c.Underlay.Port != 1 || c.Clients.Endpoint != "vmm.lan" || c.Clients.Port != 2 || c.GuestMTU != 1280 {
		t.Fatalf("config = %+v", c)
	}
}

func TestLoadHubConfigErrors(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"missing token": {`{}`, "token"},
		"bad json":      {`{`, "hub.json"},
	} {
		_, err := LoadConfig(writeFile(t, tc.body))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("missing file: no error")
	}
}
