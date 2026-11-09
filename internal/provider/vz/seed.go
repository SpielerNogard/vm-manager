package vz

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"vm-manager/internal/node"
	"vm-manager/internal/proto"
)

const seedISO = "seed.iso"

// makeSeedISO builds a cloud-init NoCloud seed (volume label "cidata") with hdiutil.
func makeSeedISO(ctx context.Context, dir string, spec proto.VMSpec) error {
	src := filepath.Join(dir, "seed")
	if err := os.MkdirAll(src, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(src)
	userData, metaData := node.CloudInit(spec)
	if err := os.WriteFile(filepath.Join(src, "user-data"), []byte(userData), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(src, "meta-data"), []byte(metaData), 0o644); err != nil {
		return err
	}
	out := filepath.Join(dir, seedISO)
	os.Remove(out)
	cmd := exec.CommandContext(ctx, "hdiutil", "makehybrid", "-iso", "-joliet", "-default-volume-name", "cidata", "-o", out, src)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("hdiutil: %v: %s", err, b)
	}
	return nil
}
