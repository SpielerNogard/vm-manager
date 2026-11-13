package firecracker

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"vm-manager/internal/node"
	"vm-manager/internal/proto"
	"vm-manager/internal/provider/linuxvm"
)

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
	b, _ := os.ReadFile(linuxvm.NBDRecord(dir, "d1"))
	dev := strings.TrimSpace(string(b))
	inst.Stop()
	<-inst.Done()
	if linuxvm.NBDConnected(dev) {
		t.Fatalf("%s still connected after the VM stopped", dev)
	}
}
