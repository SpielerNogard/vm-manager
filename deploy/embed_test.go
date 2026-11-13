package deploy

import (
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
	for _, f := range []string{"gateway.Dockerfile", "storage.Dockerfile"} {
		if !strings.Contains(read(f), "ARG VMM_RELEASE_URL") {
			t.Errorf("%s cannot build from another release URL", f)
		}
	}
}
