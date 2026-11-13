package linuxvm

import (
	"slices"
	"strings"
	"testing"
)

func TestParseNBDURL(t *testing.T) {
	host, port, export, err := parseNBDURL("nbd://10.0.0.5:10809/disk-1.img")
	if err != nil || host != "10.0.0.5" || port != "10809" || export != "disk-1.img" {
		t.Fatalf("got %q %q %q %v", host, port, export, err)
	}
	for _, bad := range []string{"nbd://10.0.0.5/disk.img", "nbd://10.0.0.5:10809/", "http://h:1/x"} {
		if _, _, _, err := parseNBDURL(bad); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

func TestQemuNBDArgs(t *testing.T) {
	got := qemuNBDArgs("/dev/nbd3", "10.0.0.5", "10809", "disk-1.img")
	want := []string{"--connect=/dev/nbd3", "--image-opts",
		"driver=raw,file.driver=nbd,file.server.type=inet,file.server.host=10.0.0.5,file.server.port=10809,file.export=disk-1.img,file.reconnect-delay=604800"}
	if !slices.Equal(got, want) {
		t.Errorf("args = %q\nwant   %q", got, want)
	}
	// image-opts separates options with commas; one inside a value must be doubled.
	if got := qemuNBDArgs("/dev/nbd0", "h", "1", "a,b.img")[2]; !strings.Contains(got, "file.export=a,,b.img,") {
		t.Errorf("comma in the export name is not escaped: %s", got)
	}
}
