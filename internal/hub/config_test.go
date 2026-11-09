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
	c, err := LoadConfig(writeFile(t, `{"token":"s3cret"}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.Token != "s3cret" || c.SSHPorts.Min != 22000 || c.SSHPorts.Max != 22999 {
		t.Fatalf("config = %+v", c)
	}

	c, err = LoadConfig(writeFile(t, `{"listen":":9000","token":"x","sshPorts":{"min":23000,"max":23010}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9000" || c.SSHPorts.Min != 23000 || c.SSHPorts.Max != 23010 {
		t.Fatalf("config = %+v", c)
	}
}

func TestLoadHubConfigErrors(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"missing token": {`{}`, "token"},
		"min > max":     {`{"token":"x","sshPorts":{"min":23000,"max":22000}}`, "sshPorts"},
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
