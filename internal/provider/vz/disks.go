package vz

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Code-Hex/vz/v3"

	"vm-manager/internal/node"
	"vm-manager/internal/proto"
)

type attachedDisk struct {
	name, file string
	readOnly   bool
	nbd        bool
}

// attachOrder is the single source of the storage device order; the guest names them vda, vdb, ... by position.
func attachOrder(f node.VMFile) []attachedDisk {
	disks := []attachedDisk{{name: "root", file: "root.img"}}
	for _, a := range f.Spec.Attachments {
		disks = append(disks, attachedDisk{name: a.DiskID, file: a.URL, nbd: true})
	}
	return append(disks, attachedDisk{name: "seed", file: node.SeedISO, readOnly: true})
}

func (p *Provider) Disks(f node.VMFile, dir string) ([]proto.Disk, error) {
	var out []proto.Disk
	for i, d := range attachOrder(f) {
		device := "vd" + string(rune('a'+i))
		if d.nbd {
			e, _ := p.nbdErrs.Load(nbdKey(f.Spec.ID, d.name))
			msg, _ := e.(string)
			out = append(out, proto.Disk{Name: d.name, File: d.file, Device: device, Error: msg})
			continue
		}
		st, err := os.Stat(filepath.Join(dir, d.file))
		if err != nil {
			return nil, err
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, fmt.Errorf("stat %s: unexpected type %T", d.file, st.Sys())
		}
		out = append(out, proto.Disk{
			Name: d.name, File: d.file, Device: device, ReadOnly: d.readOnly,
			SizeBytes: st.Size(), AllocatedBytes: sys.Blocks * 512,
		})
	}
	return out, nil
}

func nbdKey(vmID, diskID string) string { return vmID + "/" + diskID }

// trackNBD mirrors the attachment's error state into nbdErrs until done closes.
func (p *Provider) trackNBD(vmID, diskID string, att *vz.NetworkBlockDeviceStorageDeviceAttachment, done <-chan struct{}) {
	key := nbdKey(vmID, diskID)
	defer p.nbdErrs.Delete(key)
	errs, connected := att.DidEncounterError(), att.Connected()
	for {
		select {
		case err := <-errs:
			if err != nil {
				p.nbdErrs.Store(key, err.Error())
			}
		case <-connected:
			p.nbdErrs.Delete(key)
		case <-done:
			return
		}
	}
}
