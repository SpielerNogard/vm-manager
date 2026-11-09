package node

import "golang.org/x/sys/unix"

// cloneFile uses an APFS copy-on-write clone, falling back to a full copy on other filesystems.
func cloneFile(src, dst string) error {
	if err := unix.Clonefile(src, dst, 0); err == nil {
		return nil
	}
	return copyFile(src, dst)
}
