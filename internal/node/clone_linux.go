package node

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile uses a reflink (btrfs, xfs), falling back to a full copy on other filesystems.
func cloneFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err == nil {
		return out.Close()
	}
	out.Close()
	return copyFile(src, dst)
}
