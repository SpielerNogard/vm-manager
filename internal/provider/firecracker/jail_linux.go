package firecracker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

type jailFile struct {
	path     string
	readOnly bool
}

// buildJail recreates the VM's jail with files inside (hard links, or device nodes for NBD disks) and returns their paths in the jail.
func buildJail(dir string, uid int, files []jailFile) ([]string, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err != nil {
		return nil, err
	}
	if fs.Flags&unix.ST_NODEV != 0 {
		return nil, fmt.Errorf("%s is on a filesystem mounted nodev, the jail needs device nodes", dir)
	}
	// The jailed VMM only ever sees its chroot, so nobody but root needs to enter the VM dir.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(jailBase(dir)); err != nil {
		return nil, err
	}
	root := jailRoot(dir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	var inJail []string
	for _, f := range files {
		name := filepath.Base(f.path)
		if err := jailOne(f, filepath.Join(root, name), uid); err != nil {
			return nil, fmt.Errorf("jail %s: %w", name, err)
		}
		inJail = append(inJail, "/"+name)
	}
	return inJail, nil
}

func jailOne(f jailFile, dst string, uid int) error {
	var st syscall.Stat_t
	if err := syscall.Lstat(f.path, &st); err != nil {
		return err
	}
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFBLK, syscall.S_IFCHR:
		if err := syscall.Mknod(dst, st.Mode, int(st.Rdev)); err != nil {
			return err
		}
		return os.Chown(dst, uid, uid)
	case syscall.S_IFREG:
	default:
		return fmt.Errorf("%s is neither a regular file nor a device", f.path)
	}
	if err := os.Link(f.path, dst); err != nil {
		return err
	}
	// The link shares its inode with the VM dir's file, so read-only files stay root's.
	owner, mode := uid, os.FileMode(0o600)
	if f.readOnly {
		owner, mode = 0, 0o640
	}
	if err := os.Chown(dst, owner, uid); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// jailerCmd's shell is the init of a new PID namespace: when it dies, the kernel kills the jailed Firecracker, whose own Pdeathsig the jailer's UID change clears.
func jailerCmd(fcPath, dir string, uid int) *exec.Cmd {
	cmd := exec.Command("/bin/sh", jailerArgs(fcPath, dir, uid)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWPID}
	return cmd
}
