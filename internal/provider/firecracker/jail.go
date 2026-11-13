package firecracker

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
)

// jailID is fixed: the jail lives in the VM dir, which already separates the VMs.
const jailID = "vm"

// jailUIDBase is above login and systemd UIDs and below the default subuid ranges of containers (100000+).
const jailUIDBase = 70000

// jailUID derives the VM's UID/GID from its NAT address, which is already unique per VM on the node.
func jailUID(natIP string) (int, error) {
	ip := net.ParseIP(natIP).To4()
	if ip == nil {
		return 0, fmt.Errorf("NAT address %q is not IPv4", natIP)
	}
	return jailUIDBase + int(ip[3]), nil
}

func jailBase(dir string) string { return filepath.Join(dir, "jail") }

// jailRoot is where the jailer puts the chroot: <base>/<exec file name>/<id>/root.
func jailRoot(dir string) string {
	return filepath.Join(jailBase(dir), "firecracker", jailID, "root")
}

// jailerArgs are /bin/sh arguments: the shell stays PID 1 of the VM's PID namespace and runs the jailer as its child.
func jailerArgs(fcPath, dir string, uid int) []string {
	id := strconv.Itoa(uid)
	return []string{"-c", `"$@" & wait $!`, "sh", "jailer", "--id", jailID, "--exec-file", fcPath,
		"--uid", id, "--gid", id, "--chroot-base-dir", jailBase(dir),
		"--", "--api-sock", "/fc.sock", "--config-file", "/fc.json"}
}
