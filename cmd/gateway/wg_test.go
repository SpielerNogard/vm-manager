package main

import (
	"os"
	"strings"
	"testing"
)

func TestSyncWGConfig(t *testing.T) {
	var cfg string
	run := func(args ...string) (string, error) {
		if len(args) != 4 || args[1] != "syncconf" || args[2] != "wg-underlay" {
			t.Fatalf("args %v", args)
		}
		b, err := os.ReadFile(args[3])
		cfg = string(b)
		return "", err
	}
	err := syncWG(run, "wg-underlay", "PRIV", 51820, []peer{{PublicKey: "PUB1", AllowedIP: "10.198.0.11/32"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[Interface]", "PrivateKey = PRIV", "ListenPort = 51820", "[Peer]", "PublicKey = PUB1", "AllowedIPs = 10.198.0.11/32"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config missing %q:\n%s", want, cfg)
		}
	}
}
