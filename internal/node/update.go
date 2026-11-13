package node

import "path/filepath"

// InstallerManaged reports whether exe is where install.sh puts the node, so re-running the installer updates this node.
func InstallerManaged(exe, goos, home string) bool {
	want := "/usr/local/bin/vm-manager-node"
	if goos == "darwin" {
		want = filepath.Join(home, ".vm-manager", "bin", "vm-manager-node")
	}
	return filepath.Clean(exe) == want
}
