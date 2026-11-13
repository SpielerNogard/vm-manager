package cloudhypervisor

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"vm-manager/internal/node"
)

func TestNewRejectsMissingFirmware(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	raw := json.RawMessage(`{"firmware":"` + missing + `"}`)
	_, err := New(node.Config{Provider: "cloud-hypervisor", Raw: map[string]json.RawMessage{"cloud-hypervisor": raw}})
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("err = %v", err)
	}
}
