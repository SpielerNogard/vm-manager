package cloudhypervisor

import (
	"encoding/json"
	"slices"
	"testing"

	"vm-manager/internal/node"
	"vm-manager/internal/proto"
	"vm-manager/internal/provider/linuxvm"
)

func TestArgs(t *testing.T) {
	f := node.VMFile{MAC: "06:00:c0:a8:40:05", Spec: proto.VMSpec{ID: "vm1", CPUs: 2, MemoryMB: 2048,
		NICs:        []proto.NIC{{Network: "mgmt", MAC: "02:0a:c8:00:00:0a", Port: "vm1-mgmt"}},
		Attachments: []proto.DiskAttachment{{DiskID: "d1", URL: "nbd://h:1/d1.img"}}}}
	got, err := args(f, "/fw", "/v/ch.sock", []string{"/v/root.img", "/dev/nbd0", "/v/seed.iso"})
	want := []string{"--api-socket", "path=/v/ch.sock", "--firmware", "/fw", "--cpus", "boot=2", "--memory", "size=2048M",
		"--disk", "path=/v/root.img,image_type=raw", "path=/dev/nbd0,image_type=raw", "path=/v/seed.iso,image_type=raw,readonly=on",
		"--net", "tap=" + linuxvm.TapName("vm1", 0) + ",mac=06:00:c0:a8:40:05", "tap=" + linuxvm.TapName("vm1", 1) + ",mac=02:0a:c8:00:00:0a",
		"--serial", "tty", "--console", "off"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("args = %v, %v\nwant %v", got, err, want)
	}
	if _, err := args(f, "/fw", "/s", []string{"only-one"}); err == nil {
		t.Error("accepted a drive list that does not match the attach order")
	}
}

func TestParseSettingsDefaults(t *testing.T) {
	s, err := parseSettings(node.Config{Provider: "cloud-hypervisor"})
	if err != nil || s.ImageURL != linuxvm.DefaultImageURL || s.Firmware != DefaultFirmware {
		t.Errorf("defaults = %+v, %v", s, err)
	}
	raw := json.RawMessage(`{"imageURL":"http://x/i.img","firmware":"/opt/CLOUDHV.fd"}`)
	s, err = parseSettings(node.Config{Provider: "cloud-hypervisor", Raw: map[string]json.RawMessage{"cloud-hypervisor": raw}})
	if err != nil || s.ImageURL != "http://x/i.img" || s.Firmware != "/opt/CLOUDHV.fd" {
		t.Errorf("explicit = %+v, %v", s, err)
	}
}
