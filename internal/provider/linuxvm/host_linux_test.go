package linuxvm

import (
	"net"
	"os"
	"strings"
	"testing"

	"vm-manager/internal/node"
	"vm-manager/internal/proto"
)

func TestCheckHostNamesMissingBinary(t *testing.T) {
	err := CheckHost("sh", "vmm-no-such-binary")
	if err == nil || !strings.Contains(err.Error(), "vmm-no-such-binary") {
		t.Fatalf("err = %v", err)
	}
}

func TestSetupTapsOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	if _, err := net.InterfaceByName(NATBridge); err != nil {
		t.Skip("needs bridge " + NATBridge)
	}
	f := node.VMFile{Spec: proto.VMSpec{ID: "tapowner"}}
	defer RemoveTaps(f)
	if err := SetupTaps(f, 100005); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("/sys/class/net/" + TapName("tapowner", 0) + "/owner")
	if err != nil || strings.TrimSpace(string(b)) != "100005" {
		t.Fatalf("owner = %q, %v", b, err)
	}
}
