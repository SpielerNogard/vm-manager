package firecracker

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
	"vm-manager/internal/node"
	"vm-manager/internal/proto"
)

// TestAttachNBD needs root and VMM_TEST_NBD pointing at a served export, e.g. qemu-nbd -x test.
func TestAttachNBD(t *testing.T) {
	url := os.Getenv("VMM_TEST_NBD")
	if url == "" || os.Geteuid() != 0 {
		t.Skip("needs root and VMM_TEST_NBD")
	}
	dir := t.TempDir()
	dev, err := attachNBD(dir, "d1", url)
	if err != nil {
		t.Fatal(err)
	}
	if !nbdConnected(dev) {
		t.Fatalf("%s not connected", dev)
	}
	again, err := attachNBD(dir, "d1", url)
	if err != nil || again != dev {
		t.Fatalf("second attach = %s, %v; want %s reused", again, err, dev)
	}
	if err := detachNBD(dir, "d1"); err != nil {
		t.Fatal(err)
	}
	if nbdConnected(dev) {
		t.Fatalf("%s still connected after detach", dev)
	}
}

func TestStartWithNBDDisk(t *testing.T) {
	url := os.Getenv("VMM_TEST_NBD")
	if url == "" {
		t.Skip("needs VMM_TEST_NBD")
	}
	p := testProvider(t)
	f := testVM("fcnbd")
	f.Spec.Attachments = []proto.DiskAttachment{{DiskID: "d1", MountPath: "/data", URL: url}}
	inst := startVM(t, p, f)
	dir := node.VMDir(p.VMsDir, f.Spec.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if _, err := inst.WaitIP(ctx); err != nil {
		inst.Kill()
		t.Fatal(err)
	}
	disks, err := p.Disks(f, dir)
	if err != nil || len(disks) != 3 || disks[1].Name != "d1" || disks[1].Device != "vdb" || disks[1].Error != "" {
		inst.Kill()
		t.Fatalf("disks = %+v, %v", disks, err)
	}
	b, _ := os.ReadFile(nbdRecord(dir, "d1"))
	dev := strings.TrimSpace(string(b))
	inst.Stop()
	<-inst.Done()
	if nbdConnected(dev) {
		t.Fatalf("%s still connected after the VM stopped", dev)
	}
}
