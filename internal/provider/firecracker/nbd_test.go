package firecracker

import "testing"

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
