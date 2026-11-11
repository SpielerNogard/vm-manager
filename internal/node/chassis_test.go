package node

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateChassisKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chassis.key")
	priv, pub, err := LoadOrCreateChassisKey(path)
	if err != nil || priv == "" || pub == "" {
		t.Fatalf("create: %q %q %v", priv, pub, err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stat: %v %v", st, err)
	}
	priv2, pub2, err := LoadOrCreateChassisKey(path)
	if err != nil || priv2 != priv || pub2 != pub {
		t.Fatalf("reload: %q %q %v", priv2, pub2, err)
	}
}
