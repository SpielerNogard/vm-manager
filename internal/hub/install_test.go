//go:build unix

package hub

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const stubToken = "tok-SECRET-0123456789"

// runInstaller runs install.sh with system commands replaced by stubs that log their calls.
func runInstaller(t *testing.T, osName, arch, versionJSON string, args ...string) (out, calls string, code int) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	stubs := map[string]string{
		"uname": `case "$1" in -s) echo "$STUB_OS" ;; -m) echo "$STUB_ARCH" ;; esac`,
		"curl": `for a; do last=$a; done
case "$last" in */api/version) [ -n "$STUB_VERSION" ] || exit 7; printf '%s' "$STUB_VERSION" ;; *) exit 22 ;; esac`,
	}
	for _, name := range []string{"sudo", "apt-get", "systemctl", "launchctl", "modprobe", "nft", "networkctl", "install"} {
		stubs[name] = ""
	}
	for name, body := range stubs {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> \"$STUB_LOG\"\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", append([]string{"install.sh"}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin", "STUB_LOG="+log, "STUB_OS="+osName, "STUB_ARCH="+arch, "STUB_VERSION="+versionJSON, "HOME="+dir)
	b, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	c, _ := os.ReadFile(log)
	return string(b), string(c), code
}

func TestInstallScriptRejects(t *testing.T) {
	full := []string{"--hub", "http://hub.lan:8080", "--token", stubToken, "--name", "n1"}
	cases := []struct {
		name, os, arch, version string
		args                    []string
		code                    int
		want                    string
	}{
		{"missing token", "Linux", "x86_64", `{"version":"v1.0.0"}`, []string{"--hub", "http://h:1", "--name", "n", "--provider", "firecracker"}, 2, "--token is required"},
		{"hub without port", "Linux", "x86_64", `{"version":"v1.0.0"}`, []string{"--hub", "http://hub.lan", "--token", stubToken, "--name", "n1", "--provider", "firecracker"}, 1, "http://HOST:PORT"},
		{"vz on linux", "Linux", "x86_64", `{"version":"v1.0.0"}`, append(full, "--provider", "vz"), 1, "not supported on Linux"},
		{"arm64 linux", "Linux", "aarch64", `{"version":"v1.0.0"}`, append(full, "--provider", "firecracker"), 1, "amd64"},
		{"firecracker on mac", "Darwin", "arm64", `{"version":"v1.0.0"}`, append(full, "--provider", "firecracker"), 1, "not supported on Darwin"},
		{"arm64 linux ch", "Linux", "aarch64", `{"version":"v1.0.0"}`, append(full, "--provider", "cloud-hypervisor"), 1, "amd64"},
		{"ch on mac", "Darwin", "arm64", `{"version":"v1.0.0"}`, append(full, "--provider", "cloud-hypervisor"), 1, "not supported on Darwin"},
		{"unsupported lists ch", "Linux", "x86_64", `{"version":"v1.0.0"}`, append(full, "--provider", "vz"), 1, "cloud-hypervisor"},
		{"dev hub", "Linux", "x86_64", `{"version":"dev"}`, append(full, "--provider", "firecracker"), 1, "--version"},
		{"unreachable hub", "Linux", "x86_64", "", append(full, "--provider", "firecracker"), 1, "cannot reach the hub at http://hub.lan:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, calls, code := runInstaller(t, tc.os, tc.arch, tc.version, tc.args...)
			if code != tc.code || !strings.Contains(out, tc.want) {
				t.Errorf("exit %d (want %d), output %q lacks %q", code, tc.code, out, tc.want)
			}
			for _, c := range []string{"apt-get", "systemctl", "launchctl", "install "} {
				if strings.Contains(calls, c) {
					t.Errorf("ran %q before rejecting:\n%s", c, calls)
				}
			}
			if strings.Contains(out, stubToken) {
				t.Errorf("token printed:\n%s", out)
			}
		})
	}
}

func TestInstallScriptHostSafety(t *testing.T) {
	s, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(s)
	// sudo logs its command line to the system log, so the token must reach `node init` on stdin.
	if strings.Contains(script, `-token "$TOKEN"`) || !strings.Contains(script, "-token-stdin") {
		t.Error("token is passed on a (sudo) command line")
	}
	// An empty bridge never gets a carrier; without this, wait-online delays boot by 2 minutes.
	if !strings.Contains(script, "RequiredForOnline=no") {
		t.Error("vmm0 is required for network-online")
	}
	// Docker sets the FORWARD policy to DROP; VM traffic must be allowed in DOCKER-USER.
	if !strings.Contains(script, "DOCKER-USER") {
		t.Error("no DOCKER-USER rule for vmm0")
	}
	if !strings.Contains(script, "--connect-timeout") {
		t.Error("hub requests have no connect timeout")
	}
	// --since has 1 s resolution; the previous instance's log must not count as this start's connect.
	if !strings.Contains(script, "--after-cursor") {
		t.Error("node start is not tracked by journal cursor")
	}
}
