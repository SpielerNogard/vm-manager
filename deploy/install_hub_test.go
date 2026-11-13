//go:build unix

package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// runHubInstaller runs install-hub.sh with system commands replaced by stubs that log their calls.
func runHubInstaller(t *testing.T, osName, arch string, without []string, env []string, args ...string) (out, calls string, code int) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	stubs := map[string]string{
		"uname":  `case "$1" in -s) echo "$STUB_OS" ;; -m) echo "$STUB_ARCH" ;; esac`,
		"docker": `[ "$1 $2" = "compose version" ] && [ -n "${STUB_NO_COMPOSE:-}" ] && exit 1; exit 0`,
		"curl":   `exit 22`,
	}
	for _, name := range []string{"sudo", "systemctl", "launchctl", "install", "modprobe"} {
		stubs[name] = ""
	}
	for _, name := range without {
		delete(stubs, name)
	}
	for name, body := range stubs {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> \"$STUB_LOG\"\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", append([]string{"install-hub.sh"}, args...)...)
	// Without a docker stub the system's own docker must not be found either; the script only needs builtins up to that check.
	path := dir + ":/usr/bin:/bin"
	if slices.Contains(without, "docker") {
		path = dir
	}
	cmd.Env = append(append(os.Environ(), "PATH="+path, "STUB_LOG="+log, "STUB_OS="+osName, "STUB_ARCH="+arch, "HOME="+dir), env...)
	b, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	c, _ := os.ReadFile(log)
	return string(b), string(c), code
}

func TestHubInstallerRejects(t *testing.T) {
	cases := []struct {
		name, os, arch string
		without, env   []string
		args           []string
		code           int
		want           string
	}{
		{"no docker", "Linux", "x86_64", []string{"docker"}, nil, nil, 1, "Docker is required"},
		{"no compose", "Linux", "x86_64", nil, []string{"STUB_NO_COMPOSE=1"}, nil, 1, "Docker Compose"},
		{"intel mac", "Darwin", "x86_64", nil, nil, nil, 1, "Apple Silicon"},
		{"riscv", "Linux", "riscv64", nil, nil, nil, 1, "amd64 or arm64"},
		{"unknown option", "Linux", "x86_64", nil, nil, []string{"--frobnicate"}, 2, "usage: install-hub.sh"},
		{"download fails", "Linux", "x86_64", nil, nil, nil, 1, "downloading"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, calls, code := runHubInstaller(t, tc.os, tc.arch, tc.without, tc.env, tc.args...)
			if code != tc.code || !strings.Contains(out, tc.want) {
				t.Errorf("exit %d (want %d), output %q lacks %q", code, tc.code, out, tc.want)
			}
			for _, c := range []string{"install ", "systemctl", "launchctl", "modprobe"} {
				if strings.Contains(calls, c) {
					t.Errorf("ran %q before rejecting:\n%s", c, calls)
				}
			}
		})
	}
}

func TestHubInstallerScript(t *testing.T) {
	s, err := os.ReadFile("install-hub.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(s)
	for _, want := range []string{
		"-stack",
		"vm-manager-hub-$PLATFORM",
		"sha256",
		"$RELEASES/latest/download",
		"After=network-online.target docker.service",
		"dev.vm-manager.hub",
		"--max-time",
		// root's ~/.ssh is usually empty: the hub's default keys are those of the user who ran the installer.
		"SUDO_USER",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("install-hub.sh lacks %q", want)
		}
	}
}
