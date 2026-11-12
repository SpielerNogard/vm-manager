package hub

import (
	"fmt"
	"slices"
	"time"

	"vm-manager/internal/proto"
)

type AttachmentView struct {
	VMID      string `json:"vmId"`
	VMName    string `json:"vmName"`
	NodeID    string `json:"nodeId"`
	MountPath string `json:"mountPath"`
}

type DiskView struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	SizeGB         int             `json:"sizeGB"`
	AllocatedBytes int64           `json:"allocatedBytes"`
	State          string          `json:"state"` // "creating" (no file at full size reported yet), "ready", "releasing", "missing"
	ReleasingOn    string          `json:"releasingOn,omitempty"`
	CopyFrom       string          `json:"copyFrom,omitempty"`
	Attachment     *AttachmentView `json:"attachment,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// diskViewLocked requires r.mu.
func (r *Registry) diskViewLocked(d StoredDisk) DiskView {
	v := DiskView{ID: d.ID, Name: d.Name, SizeGB: d.SizeGB, State: "creating", ReleasingOn: d.ReleasingOn, CopyFrom: d.CopyFrom, CreatedAt: d.CreatedAt}
	if f, ok := r.storageFileLocked(d.ID); ok {
		v.AllocatedBytes = f.AllocatedBytes
		if f.SizeBytes >= int64(d.SizeGB)<<30 {
			v.State = "ready"
		}
	}
	if d.ReleasingOn != "" {
		v.State = "releasing"
	}
	if r.missingLocked(d) {
		v.State = "missing"
	}
	if vm, i := r.attachmentLocked(d.ID); vm != nil {
		v.Attachment = &AttachmentView{VMID: vm.ID, VMName: vm.Name, NodeID: vm.NodeID, MountPath: vm.Attachments[i].MountPath}
	}
	return v
}

// missingLocked reports a disk whose file the connected storage agent no longer has; r.mu must be held.
func (r *Registry) missingLocked(d StoredDisk) bool {
	_, ok := r.storageFileLocked(d.ID)
	return d.Seen && r.storageReported && !ok
}

// storageFileLocked returns the agent's last report of the disk's file; r.mu must be held.
func (r *Registry) storageFileLocked(diskID string) (proto.StorageFile, bool) {
	name := proto.StorageFileName(diskID)
	i := slices.IndexFunc(r.storageFiles, func(f proto.StorageFile) bool { return f.Name == name })
	if i < 0 {
		return proto.StorageFile{}, false
	}
	return r.storageFiles[i], true
}

func (r *Registry) Disks() []DiskView {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []DiskView{}
	for _, d := range r.disks.Disks() {
		out = append(out, r.diskViewLocked(d))
	}
	return out
}

func (r *Registry) checkNewDiskLocked(name string, sizeGB int) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%w: disk name must match %s", ErrInvalid, nameRe)
	}
	if sizeGB < 1 {
		return fmt.Errorf("%w: sizeGB must be at least 1", ErrInvalid)
	}
	if slices.ContainsFunc(r.disks.Disks(), func(d StoredDisk) bool { return d.Name == name }) {
		return fmt.Errorf("%w: disk %q already exists", ErrInvalid, name)
	}
	return nil
}

func (r *Registry) CreateDisk(name string, sizeGB int) (DiskView, error) {
	v, _, err := r.addDisk(name, sizeGB, "")
	return v, err
}

// CopyDisk returns a warning when the source is attached or releasing, as the copy may then be inconsistent.
func (r *Registry) CopyDisk(srcID, name string) (DiskView, string, error) {
	return r.addDisk(name, 0, srcID)
}

// addDisk takes the size from the source disk if copyFrom is set.
func (r *Registry) addDisk(name string, sizeGB int, copyFrom string) (DiskView, string, error) {
	r.mu.Lock()
	var warning string
	if copyFrom != "" {
		src, ok := r.disks.Get(copyFrom)
		if !ok {
			r.mu.Unlock()
			return DiskView{}, "", fmt.Errorf("%w: disk %q", ErrNotFound, copyFrom)
		}
		if r.missingLocked(src) {
			r.mu.Unlock()
			return DiskView{}, "", fmt.Errorf("%w: disk %q is missing on the storage server and cannot be copied", ErrConflict, src.Name)
		}
		sizeGB = src.SizeGB
		if vm, _ := r.attachmentLocked(copyFrom); vm != nil || src.ReleasingOn != "" {
			warning = fmt.Sprintf("disk %q may still be written by a VM; the copy is only crash-consistent", src.Name)
		}
	}
	if err := r.checkNewDiskLocked(name, sizeGB); err != nil {
		r.mu.Unlock()
		return DiskView{}, "", err
	}
	d := StoredDisk{ID: newID(), Name: name, SizeGB: sizeGB, CopyFrom: copyFrom, CreatedAt: time.Now().UTC()}
	if err := r.disks.Put(d); err != nil {
		r.mu.Unlock()
		return DiskView{}, "", err
	}
	v := r.diskViewLocked(d)
	r.mu.Unlock()
	r.pushStorage()
	return v, warning, nil
}

// GrowDisk only changes the disk; an attached VM sees the new size once the storage agent reports the grown file.
func (r *Registry) GrowDisk(id string, sizeGB int, restartAllowed bool) (DiskView, error) {
	r.mu.Lock()
	d, ok := r.disks.Get(id)
	if !ok {
		r.mu.Unlock()
		return DiskView{}, fmt.Errorf("%w: disk %q", ErrNotFound, id)
	}
	if sizeGB <= d.SizeGB {
		r.mu.Unlock()
		return DiskView{}, fmt.Errorf("%w: sizeGB must be larger than the current %d", ErrInvalid, d.SizeGB)
	}
	if d.ReleasingOn != "" {
		r.mu.Unlock()
		return DiskView{}, fmt.Errorf("%w: disk %q is still being released by node %q", ErrConflict, id, d.ReleasingOn)
	}
	if vm, _ := r.attachmentLocked(id); vm != nil && vm.Power == proto.PowerRunning && !restartAllowed {
		r.mu.Unlock()
		return DiskView{}, fmt.Errorf("%w: disk is attached to running VM %q: restart required: pass restartAllowed=true", ErrConflict, vm.Name)
	}
	d.SizeGB = sizeGB
	if err := r.disks.Put(d); err != nil {
		r.mu.Unlock()
		return DiskView{}, err
	}
	v := r.diskViewLocked(d)
	r.mu.Unlock()
	r.pushStorage()
	return v, nil
}

func (r *Registry) DeleteDisk(id string) error {
	r.mu.Lock()
	d, ok := r.disks.Get(id)
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: disk %q", ErrNotFound, id)
	}
	if d.ReleasingOn != "" {
		r.mu.Unlock()
		return fmt.Errorf("%w: disk %q is still being released by node %q", ErrConflict, id, d.ReleasingOn)
	}
	if vm, _ := r.attachmentLocked(id); vm != nil {
		r.mu.Unlock()
		return fmt.Errorf("%w: disk %q is attached to VM %q", ErrConflict, id, vm.Name)
	}
	for _, o := range r.disks.Disks() {
		if o.CopyFrom == id {
			r.mu.Unlock()
			return fmt.Errorf("%w: disk %q is the source of a pending copy (%q)", ErrConflict, id, o.Name)
		}
	}
	if err := r.disks.Delete(id); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()
	r.pushStorage()
	return nil
}
