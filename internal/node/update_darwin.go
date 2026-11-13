package node

import (
	"os/exec"
	"syscall"
)

// installerCommand runs the installer in a session of its own: on bootout launchd kills only the job's process group.
func installerCommand(script string, args []string) *exec.Cmd {
	cmd := exec.Command("sh", append([]string{script}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}
