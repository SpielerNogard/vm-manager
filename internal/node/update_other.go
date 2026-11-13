//go:build !darwin && !linux

package node

import "os/exec"

func installerCommand(script string, args []string) *exec.Cmd {
	return exec.Command("sh", append([]string{script}, args...)...)
}
