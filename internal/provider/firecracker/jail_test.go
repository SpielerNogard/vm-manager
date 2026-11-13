package firecracker

import (
	"slices"
	"testing"
)

func TestJailUID(t *testing.T) {
	for ip, want := range map[string]int{"192.168.64.2": 70002, "192.168.64.254": 70254} {
		if got, err := jailUID(ip); err != nil || got != want {
			t.Errorf("jailUID(%q) = %d, %v", ip, got, err)
		}
	}
	for _, bad := range []string{"", "junk", "192.168.64"} {
		if _, err := jailUID(bad); err == nil {
			t.Errorf("jailUID(%q) accepted", bad)
		}
	}
}

func TestJailerArgs(t *testing.T) {
	want := []string{"-c", `"$@" & wait $!`, "sh", "jailer", "--id", "vm", "--exec-file", "/usr/local/bin/firecracker",
		"--uid", "100005", "--gid", "100005", "--chroot-base-dir", "/v/vm1/jail",
		"--", "--api-sock", "/fc.sock", "--config-file", "/fc.json"}
	if got := jailerArgs("/usr/local/bin/firecracker", "/v/vm1", 100005); !slices.Equal(got, want) {
		t.Errorf("got %q", got)
	}
}
