package vz

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"vm-manager/internal/node"
	"vm-manager/internal/proto"
)

type attachedDisk struct {
	name, file string
	readOnly   bool
}

// attachOrder is the single source of the storage device order; the guest names them vda, vdb, ... by position.
func attachOrder(f node.VMFile) []attachedDisk {
	disks := []attachedDisk{{name: "root", file: "root.img"}}
	for i, d := range f.Spec.DataDisks {
		disks = append(disks, attachedDisk{name: d.Name, file: node.DataDiskName(i)})
	}
	return append(disks, attachedDisk{name: "seed", file: seedISO, readOnly: true})
}

func (p *Provider) Disks(f node.VMFile, dir string) ([]proto.Disk, error) {
	var out []proto.Disk
	for i, d := range attachOrder(f) {
		st, err := os.Stat(filepath.Join(dir, d.file))
		if err != nil {
			return nil, err
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, fmt.Errorf("stat %s: unexpected type %T", d.file, st.Sys())
		}
		out = append(out, proto.Disk{
			Name: d.name, File: d.file, Device: "vd" + string(rune('a'+i)), ReadOnly: d.readOnly,
			SizeBytes: st.Size(), AllocatedBytes: sys.Blocks * 512,
		})
	}
	return out, nil
}
