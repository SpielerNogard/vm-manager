package node

import (
	"context"
	"log"
	"slices"
	"sync"

	"vm-manager/internal/proto"
)

// reconcile converges the node's VMs to desired, the hub's full list for this node.
// seq is the list's Message.Seq, echoed to the hub once the list is applied.
func (a *Agent) reconcile(ctx context.Context, seq uint64, desired []proto.DesiredVM) {
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
	deferred := a.deferred
	a.deferred = nil
	a.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range gone {
		wg.Add(1)
		go func() { defer wg.Done(); a.destroy(e) }()
	}
	// Removals finish first so their resources are free before anything new is prepared.
	wg.Wait()
	a.resumeDeferred(ctx, deferred, desired)

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

	a.mu.Lock()
	a.appliedSeq = seq
	inUse, send := a.disksInUse(), a.send
	a.mu.Unlock()
	if send != nil {
		if err := send(proto.Message{Type: "disks", DisksInUse: inUse, AppliedSeq: seq}); err != nil {
			log.Printf("disks in use: %v", err)
		}
	}
}

// resumeDeferred boots the restored VMs with disks that the hub still wants and deletes the rest.
func (a *Agent) resumeDeferred(ctx context.Context, deferred []VMFile, desired []proto.DesiredVM) {
	gens := make(map[string]uint64, len(desired))
	for _, d := range desired {
		gens[d.Spec.ID] = d.Generation
	}
	for _, f := range deferred {
		gen, ok := gens[f.Spec.ID]
		switch {
		case !ok:
			if err := DeleteVMDir(a.VMsDir, f.Spec.ID); err != nil {
				log.Printf("vm %s: delete dir: %v", f.Spec.ID, err)
			}
		case gen > f.Generation:
			// The attachments may have changed while the node was down; updateDesired applies them before booting.
			booted := make(chan struct{})
			close(booted)
			a.mu.Lock()
			a.vms[f.Spec.ID] = &vmEntry{file: f, status: proto.VMStatus{ID: f.Spec.ID, State: proto.StateStopped}, cancel: func() {}, booted: booted}
			a.mu.Unlock()
		default:
			a.startVM(ctx, f, f.NeedsPrepare)
		}
	}
}

func (a *Agent) destroy(e *vmEntry) {
	id := e.file.Spec.ID
	e.cancel()
	<-e.booted
	if inst := a.instance(e); inst != nil {
		e.stopRequested.Store(true)
		if err := inst.Kill(); err != nil {
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
		e.file.Spec, e.file.Power, e.file.StopMode, e.file.Generation = d.Spec, d.Power, d.StopMode, d.Generation
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
	var reseedErr error
	halted := !slices.Equal(f.Spec.NICs, d.Spec.NICs) || !slices.Equal(f.Spec.Attachments, d.Spec.Attachments)
	if halted {
		e = a.halt(e)
		oldNICs, oldAtts := f.Spec.NICs, f.Spec.Attachments
		f.Spec.NICs, f.Spec.Attachments = d.Spec.NICs, d.Spec.Attachments
		if !f.NeedsPrepare {
			// Keeping the old values on failure leaves a difference, so the next generation retries the reseed.
			if reseedErr = a.Provider.Reseed(ctx, f, VMDir(a.VMsDir, f.Spec.ID)); reseedErr != nil {
				f.Spec.NICs, f.Spec.Attachments = oldNICs, oldAtts
			}
		}
	}
	if err := SaveVM(a.VMsDir, f); err != nil {
		log.Printf("vm %s: save: %v", f.Spec.ID, err)
		if halted {
			a.report(proto.VMStatus{ID: f.Spec.ID, State: proto.StateError, Error: "save: " + err.Error()})
		}
		return
	}
	a.mu.Lock()
	e.file = f
	inst, st := e.inst, e.status
	a.mu.Unlock()
	if reseedErr != nil {
		a.report(proto.VMStatus{ID: f.Spec.ID, State: proto.StateError, Error: "reseed: " + reseedErr.Error()})
		return
	}
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
	case f.Power == proto.PowerStopped:
		e.cancel() // ends a pending restart backoff
		a.report(st)
	default:
		a.report(st)
	}
}

// halt stops e's instance and swaps in an idle stopped entry, so e's boot goroutine neither restarts it nor reports over later updates.
func (a *Agent) halt(e *vmEntry) *vmEntry {
	e.stopRequested.Store(true)
	if inst := a.instance(e); inst != nil {
		select {
		case <-inst.Done():
		default:
			if err := inst.Stop(); err != nil {
				log.Printf("vm %s: stop: %v", e.file.Spec.ID, err)
				if err := inst.Kill(); err != nil {
					log.Printf("vm %s: kill: %v", e.file.Spec.ID, err)
				}
			}
			<-inst.Done()
		}
	}
	// Cancelling only after exit keeps a pending WaitIP from reporting the cancellation as an error.
	e.cancel()
	booted := make(chan struct{})
	close(booted)
	a.mu.Lock()
	defer a.mu.Unlock()
	idle := &vmEntry{file: e.file, status: proto.VMStatus{ID: e.file.Spec.ID, State: proto.StateStopped}, cancel: func() {}, booted: booted}
	a.vms[idle.file.Spec.ID] = idle
	return idle
}
