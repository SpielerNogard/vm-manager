package vz

import (
	"github.com/Code-Hex/vz/v3"

	"vm-manager/internal/node"
	"vm-manager/internal/proto"
)

func (p *Provider) Disks(f node.VMFile, dir string) ([]proto.Disk, error) {
	return node.ListDisks(f, dir, func(diskID string) string {
		e, _ := p.nbdErrs.Load(nbdKey(f.Spec.ID, diskID))
		msg, _ := e.(string)
		return msg
	})
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
