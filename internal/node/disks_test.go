package node

import (
	"testing"

	"vm-manager/internal/proto"
)

func TestAttachOrder(t *testing.T) {
	f := VMFile{Spec: proto.VMSpec{Attachments: []proto.DiskAttachment{{DiskID: "d1", URL: "nbd://h:1/d1.img"}}}}
	got := AttachOrder(f)
	want := []AttachedDisk{
		{Name: "root", File: "root.img"},
		{Name: "d1", File: "nbd://h:1/d1.img", NBD: true},
		{Name: "seed", File: SeedISO, ReadOnly: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("disk %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if DeviceName(0) != "vda" || DeviceName(3) != "vdd" {
		t.Errorf("DeviceName = %s %s", DeviceName(0), DeviceName(3))
	}
}
