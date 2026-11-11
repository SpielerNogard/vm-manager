package node

import (
	"context"
	"log"
	"sync"

	"vm-manager/internal/proto"
)

// reconcile converges the node's VMs to desired, the hub's full list for this node.
func (a *Agent) reconcile(ctx context.Context, desired []proto.DesiredVM) {
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()

	want := make(map[string]bool, len(desired))
	for _, d := range desired {
		want[d.Spec.ID] = true
	}
	a.mu.Lock()
	var gone []*vmEntry
	for id, e := range a.vms {
		if !want[id] {
			gone = append(gone, e)
			delete(a.vms, id)
		}
	}
	a.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range gone {
		wg.Add(1)
		go func() { defer wg.Done(); a.destroy(e) }()
	}
	// Removals finish first so their resources are free before anything new is prepared.
	wg.Wait()

	for _, d := range desired {
		a.mu.Lock()
		e, ok := a.vms[d.Spec.ID]
		var gen uint64
		if ok {
			gen = e.file.Generation
		}
		a.mu.Unlock()
		switch {
		case !ok || e.placeholder && !d.Provisioned && d.Generation > gen:
			a.createDesired(ctx, d)
		case d.Generation > gen:
			a.updateDesired(ctx, e, d)
		}
	}
}

func (a *Agent) destroy(e *vmEntry) {
	id := e.file.Spec.ID
	e.cancel()
	<-e.booted
	if e.inst != nil {
		e.stopRequested.Store(true)
		if err := e.inst.Kill(); err != nil {
			log.Printf("vm %s: kill: %v", id, err)
		}
	}
	if err := DeleteVMDir(a.VMsDir, id); err != nil {
		log.Printf("vm %s: delete dir: %v", id, err)
	}
}

func (a *Agent) createDesired(ctx context.Context, d proto.DesiredVM) {
	if d.Provisioned {
		a.placeholder(d, proto.ErrVMDirMissing)
		return
	}
	f := VMFile{Spec: d.Spec, MAC: randomMAC(), Power: d.Power, StopMode: d.StopMode, Generation: d.Generation, NeedsPrepare: true}
	if err := SaveVM(a.VMsDir, f); err != nil {
		a.placeholder(d, err.Error())
		return
	}
	a.startVM(ctx, f, true)
}

// placeholder records an unbootable VM so its error reaches the hub.
func (a *Agent) placeholder(d proto.DesiredVM, msg string) {
	booted := make(chan struct{})
	close(booted)
	e := &vmEntry{
		file:   VMFile{Spec: d.Spec, Power: d.Power, StopMode: d.StopMode, Generation: d.Generation},
		cancel: func() {}, booted: booted, placeholder: true,
	}
	a.mu.Lock()
	a.vms[d.Spec.ID] = e
	a.mu.Unlock()
	a.report(proto.VMStatus{ID: d.Spec.ID, State: proto.StateError, Error: msg})
}

func (a *Agent) updateDesired(ctx context.Context, e *vmEntry, d proto.DesiredVM) {
	if e.placeholder {
		a.mu.Lock()
		e.file.Power, e.file.StopMode, e.file.Generation = d.Power, d.StopMode, d.Generation
		st := e.status
		a.mu.Unlock()
		a.report(st)
		return
	}
	<-e.booted
	a.mu.Lock()
	f := e.file
	a.mu.Unlock()
	f.Power, f.StopMode, f.Generation = d.Power, d.StopMode, d.Generation
	if err := SaveVM(a.VMsDir, f); err != nil {
		log.Printf("vm %s: save: %v", f.Spec.ID, err)
		return
	}
	a.mu.Lock()
	e.file = f
	inst, st := e.inst, e.status
	a.mu.Unlock()
	live := inst != nil
	if live {
		select {
		case <-inst.Done():
			live = false
		default:
		}
	}
	switch {
	case f.Power != proto.PowerStopped && !live:
		e.cancel()
		a.startVM(ctx, f, f.NeedsPrepare)
	case f.Power == proto.PowerStopped && live:
		e.stopRequested.Store(true)
		stop := inst.Stop
		if f.StopMode == proto.StopHard {
			stop = inst.Kill
		}
		if err := stop(); err != nil {
			log.Printf("vm %s: stop: %v", f.Spec.ID, err)
		}
		a.report(proto.VMStatus{ID: f.Spec.ID, State: proto.StateStopped})
	default:
		a.report(st)
	}
}
