package node

import "testing"

func TestInstallerManaged(t *testing.T) {
	for _, tc := range []struct {
		exe, goos, home string
		want            bool
	}{
		{"/usr/local/bin/vm-manager-node", "linux", "/root", true},
		{"/Users/a/.vm-manager/bin/vm-manager-node", "darwin", "/Users/a", true},
		{"/home/a/src/bin/vm-manager-node", "linux", "/home/a", false},
		{"/Users/a/.vm-manager/bin/node", "darwin", "/Users/a", false},
	} {
		if got := InstallerManaged(tc.exe, tc.goos, tc.home); got != tc.want {
			t.Errorf("InstallerManaged(%q, %q, %q) = %v, want %v", tc.exe, tc.goos, tc.home, got, tc.want)
		}
	}
}

func TestRegisterCarriesVersion(t *testing.T) {
	a, _ := newReconcileAgent(t)
	a.Version, a.Updatable = "v0.5.0", true
	if reg := a.registerMsg(); reg.Version != "v0.5.0" || !reg.Updatable {
		t.Errorf("register = version %q updatable %v", reg.Version, reg.Updatable)
	}
}
