package firecracker

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func jailTestDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	dir := filepath.Join(t.TempDir(), "vm")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func statT(t *testing.T, path string) *syscall.Stat_t {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Sys().(*syscall.Stat_t)
}

func TestBuildJail(t *testing.T) {
	dir := jailTestDir(t)
	rw := writeFile(t, filepath.Join(dir, "root.img"))
	ro := writeFile(t, filepath.Join(dir, "seed.iso"))
	files := []jailFile{{path: rw}, {path: "/dev/null"}, {path: ro, readOnly: true}}
	for range 2 { // a restart finds the previous jail
		got, err := buildJail(dir, 70005, files)
		if err != nil || !slices.Equal(got, []string{"/root.img", "/null", "/seed.iso"}) {
			t.Fatalf("%q, %v", got, err)
		}
	}
	root := jailRoot(dir)
	if st := statT(t, filepath.Join(root, "root.img")); st.Ino != statT(t, rw).Ino || st.Uid != 70005 || st.Mode&0o777 != 0o600 {
		t.Errorf("root.img: ino %d uid %d mode %o", st.Ino, st.Uid, st.Mode&0o777)
	}
	// The VMM may read but not rewrite what the host hands it read-only.
	if st := statT(t, filepath.Join(root, "seed.iso")); st.Ino != statT(t, ro).Ino || st.Uid != 0 || st.Gid != 70005 || st.Mode&0o777 != 0o640 {
		t.Errorf("seed.iso: ino %d uid %d gid %d mode %o", st.Ino, st.Uid, st.Gid, st.Mode&0o777)
	}
	if st := statT(t, filepath.Join(root, "null")); st.Rdev != statT(t, "/dev/null").Rdev || st.Uid != 70005 {
		t.Errorf("device node: rdev %d uid %d", st.Rdev, st.Uid)
	}
	// Other VMs' UIDs must not reach this VM's disks.
	if st := statT(t, dir); st.Mode&0o777 != 0o700 {
		t.Errorf("VM dir mode %o", st.Mode&0o777)
	}
}

func TestBuildJailRejectsSymlink(t *testing.T) {
	dir := jailTestDir(t)
	link := filepath.Join(dir, "root.img")
	if err := os.Symlink(writeFile(t, filepath.Join(t.TempDir(), "elsewhere.img")), link); err != nil {
		t.Fatal(err)
	}
	if _, err := buildJail(dir, 70005, []jailFile{{path: link}}); err == nil || !strings.Contains(err.Error(), "root.img") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildJailRejectsNodev(t *testing.T) {
	dir := jailTestDir(t)
	if err := syscall.Mount("tmpfs", dir, "tmpfs", syscall.MS_NODEV, ""); err != nil {
		t.Fatal(err)
	}
	defer syscall.Unmount(dir, 0)
	_, err := buildJail(dir, 70005, []jailFile{{path: writeFile(t, filepath.Join(dir, "root.img"))}})
	if err == nil || !strings.Contains(err.Error(), "nodev") {
		t.Fatalf("err = %v", err)
	}
}
