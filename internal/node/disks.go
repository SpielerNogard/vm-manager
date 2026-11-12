//go:build unix

package node

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"vm-manager/internal/proto"
)

type AttachedDisk struct {
	Name, File    string
	ReadOnly, NBD bool
}

// AttachOrder is the single source of the block device order; guests name them vda, vdb, ... by position.
func AttachOrder(f VMFile) []AttachedDisk {
	disks := []AttachedDisk{{Name: "root", File: "root.img"}}
	for _, a := range f.Spec.Attachments {
		disks = append(disks, AttachedDisk{Name: a.DiskID, File: a.URL, NBD: true})
	}
	return append(disks, AttachedDisk{Name: "seed", File: SeedISO, ReadOnly: true})
}

func DeviceName(i int) string { return "vd" + string(rune('a'+i)) }

// ListDisks reports the attached disks; nbdErr returns the current error of an NBD attachment, if any.
func ListDisks(f VMFile, dir string, nbdErr func(diskID string) string) ([]proto.Disk, error) {
	var out []proto.Disk
	for i, d := range AttachOrder(f) {
		if d.NBD {
			out = append(out, proto.Disk{Name: d.Name, File: d.File, Device: DeviceName(i), Error: nbdErr(d.Name)})
			continue
		}
		st, err := os.Stat(filepath.Join(dir, d.File))
		if err != nil {
			return nil, err
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, fmt.Errorf("stat %s: unexpected type %T", d.File, st.Sys())
		}
		out = append(out, proto.Disk{
			Name: d.Name, File: d.File, Device: DeviceName(i), ReadOnly: d.ReadOnly,
			SizeBytes: st.Size(), AllocatedBytes: int64(sys.Blocks) * 512,
		})
	}
	return out, nil
}
