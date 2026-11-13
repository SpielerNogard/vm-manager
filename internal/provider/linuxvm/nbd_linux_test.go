package linuxvm

import (
	"os"
	"testing"
)

// TestAttachNBD needs root and VMM_TEST_NBD pointing at a served export, e.g. qemu-nbd -x test.
func TestAttachNBD(t *testing.T) {
	url := os.Getenv("VMM_TEST_NBD")
	if url == "" || os.Geteuid() != 0 {
		t.Skip("needs root and VMM_TEST_NBD")
	}
	dir := t.TempDir()
	dev, err := AttachNBD(dir, "d1", url)
	if err != nil {
		t.Fatal(err)
	}
	if !NBDConnected(dev) {
		t.Fatalf("%s not connected", dev)
	}
	again, err := AttachNBD(dir, "d1", url)
	if err != nil || again != dev {
		t.Fatalf("second attach = %s, %v; want %s reused", again, err, dev)
	}
	if err := DetachNBD(dir, "d1"); err != nil {
		t.Fatal(err)
	}
	if NBDConnected(dev) {
		t.Fatalf("%s still connected after detach", dev)
	}
}
