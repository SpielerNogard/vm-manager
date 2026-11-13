package linuxvm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllocNATIP(t *testing.T) {
	vms := t.TempDir()
	alloc := func(id string) string {
		t.Helper()
		ip, err := AllocNATIP(vms, id)
		if err != nil {
			t.Fatal(err)
		}
		return ip
	}
	if a, b := alloc("a"), alloc("b"); a != "192.168.64.2" || b != "192.168.64.3" {
		t.Fatalf("got %s %s", a, b)
	}
	if again := alloc("a"); again != "192.168.64.2" {
		t.Fatalf("not stable: %s", again)
	}
	os.RemoveAll(filepath.Join(vms, "a"))
	if c := alloc("c"); c != "192.168.64.2" {
		t.Fatalf("freed address not reused: %s", c)
	}
	for i := 0; i < 251; i++ {
		alloc(fmt.Sprintf("fill%d", i))
	}
	if _, err := AllocNATIP(vms, "overflow"); err == nil || !strings.Contains(err.Error(), "no free NAT address") {
		t.Fatalf("err = %v", err)
	}
}
