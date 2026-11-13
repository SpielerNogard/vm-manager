package node

import "os/exec"

// installerCommand runs the installer as a transient unit, so the restart of vm-manager-node it causes does not kill it.
func installerCommand(script string, args []string) *exec.Cmd {
	return exec.Command("systemd-run", append([]string{"--quiet", "--wait", "--pipe", "--collect", "--unit=vm-manager-node-update", "sh", script}, args...)...)
}
