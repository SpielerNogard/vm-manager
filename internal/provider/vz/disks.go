//go:build darwin

package vz

import (
	"log"
	"time"

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

// trackNBD follows one NBD attachment until the VM is gone.
func (p *Provider) trackNBD(vmID string, n nbdAttachment, inst *instance) {
	key := nbdKey(vmID, n.diskID)
	defer p.nbdErrs.Delete(key)
	t := time.NewTicker(nbdProbeInterval)
	defer t.Stop()
	watchNBD(n.att.DidEncounterError(), n.att.Connected(), inst.done, t.C, func() bool { return nbdServerUp(n.addr) },
		func(msg string) {
			if msg == "" {
				p.nbdErrs.Delete(key)
				return
			}
			p.nbdErrs.Store(key, msg)
		},
		func() {
			log.Printf("vm %s: disk %s stayed failed after the storage server came back; restarting the VM", vmID, n.diskID)
			inst.Kill()
		})
}
