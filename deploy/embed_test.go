package deploy

import (
	"os"
	"strings"
	"testing"
)

func TestStackFiles(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		b, err := Stack.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	compose := read("stack.compose.yaml")
	for _, want := range []string{
		"host.docker.internal:host-gateway",
		"HUB_URL: http://host.docker.internal:${HUB_PORT}",
		"${DATA_DIR}:/data:ro",
		"name: vm-manager-disks",
		"profiles: [storage]",
		"network_mode: \"service:gateway\"",
		"deploy/tokens.Dockerfile",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("stack.compose.yaml lacks %q", want)
		}
	}
	if strings.Contains(compose, "service:netns") {
		t.Error("the hub-managed stack still shares a network namespace with a hub container")
	}
	if !strings.Contains(read("gateway-start.sh"), `"${HUB_URL:-http://127.0.0.1:8080}"`) {
		t.Error("gateway-start.sh does not take the hub address from HUB_URL")
	}
	if !strings.Contains(read("gateway-start.sh"), "--dport 9002 -j ACCEPT") {
		t.Error("gateway-start.sh does not let VMs reach the token service")
	}
	for _, f := range []string{"gateway.Dockerfile", "storage.Dockerfile", "tokens.Dockerfile"} {
		if !strings.Contains(read(f), "ARG VMM_RELEASE_URL") {
			t.Errorf("%s cannot build from another release URL", f)
		}
	}
}

func TestStacksLoadKernelModules(t *testing.T) {
	stack, err := Stack.ReadFile("stack.compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	compose, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// OrbStack ships openvswitch but does not load it; without it the gateway restarts in a loop.
	for name, file := range map[string]string{"stack.compose.yaml": string(stack), "compose.yaml": string(compose)} {
		for _, want := range []string{"modprobe -a openvswitch wireguard || true", "/lib/modules:/lib/modules:ro", "condition: service_completed_successfully"} {
			if !strings.Contains(file, want) {
				t.Errorf("%s lacks %q", name, want)
			}
		}
	}
}
