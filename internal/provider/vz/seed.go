package vz

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"vm-manager/internal/node"
)

const seedISO = "seed.iso"

func writeGuestSeed(f node.VMFile) func(src string) error {
	return func(src string) error {
		userData, metaData, networkConfig := node.CloudInit(f)
		for name, content := range map[string]string{"user-data": userData, "meta-data": metaData, "network-config": networkConfig} {
			if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
}

// makeSeedISO builds a cloud-init NoCloud seed (volume label "cidata") with hdiutil from the files write puts into its directory.
func makeSeedISO(ctx context.Context, dir string, write func(src string) error) error {
	src := filepath.Join(dir, "seed")
	os.RemoveAll(src)
	if err := os.MkdirAll(src, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(src)
	if err := write(src); err != nil {
		return err
	}
	// Building beside seed.iso and renaming keeps the old seed bootable if hdiutil fails; hdiutil won't overwrite its -o target.
	tmp := filepath.Join(dir, "seed-new.iso")
	os.Remove(tmp)
	defer os.Remove(tmp)
	cmd := exec.CommandContext(ctx, "hdiutil", "makehybrid", "-iso", "-joliet", "-default-volume-name", "cidata", "-o", tmp, src)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("hdiutil: %v: %s", err, b)
	}
	return os.Rename(tmp, filepath.Join(dir, seedISO))
}
