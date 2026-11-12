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
	State          string          `json:"state"` // "creating" (no file at full size reported yet), "ready", "releasing"
	ReleasingOn    string          `json:"releasingOn,omitempty"`
	CopyFrom       string          `json:"copyFrom,omitempty"`
	Attachment     *AttachmentView `json:"attachment,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// diskViewLocked requires r.mu.
func (r *Registry) diskViewLocked(d StoredDisk) DiskView {
	v := DiskView{ID: d.ID, Name: d.Name, SizeGB: d.SizeGB, State: "creating", ReleasingOn: d.ReleasingOn, CopyFrom: d.CopyFrom, CreatedAt: d.CreatedAt}
	name := proto.StorageFileName(d.ID)
	for _, f := range r.storageFiles {
		if f.Name == name {
			v.AllocatedBytes = f.AllocatedBytes
			if f.SizeBytes >= int64(d.SizeGB)<<30 {
				v.State = "ready"
			}
		}
	}
	if d.ReleasingOn != "" {
		v.State = "releasing"
	}
	return v
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
	return r.addDisk(name, sizeGB, "")
}

func (r *Registry) addDisk(name string, sizeGB int, copyFrom string) (DiskView, error) {
	r.mu.Lock()
	if err := r.checkNewDiskLocked(name, sizeGB); err != nil {
		r.mu.Unlock()
		return DiskView{}, err
	}
	d := StoredDisk{ID: newID(), Name: name, SizeGB: sizeGB, CopyFrom: copyFrom, CreatedAt: time.Now().UTC()}
	if err := r.disks.Put(d); err != nil {
		r.mu.Unlock()
		return DiskView{}, err
	}
	v := r.diskViewLocked(d)
	r.mu.Unlock()
	r.pushStorage()
	return v, nil
}

// CopyDisk returns a warning when the source is attached, as the copy may then be inconsistent.
func (r *Registry) CopyDisk(srcID, name string) (DiskView, string, error) {
	src, ok := r.disks.Get(srcID)
	if !ok {
		return DiskView{}, "", fmt.Errorf("%w: disk %q", ErrNotFound, srcID)
	}
	v, err := r.addDisk(name, src.SizeGB, srcID)
	return v, "", err
}

// GrowDisk handles unattached disks only; restartAllowed matters once disks can be attached.
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
