package hub

import (
	"errors"
	"testing"

	"vm-manager/internal/proto"
)

func newAttachRegistry(t *testing.T) (*Registry, *fakeNode, *fakeStorage) {
	t.Helper()
	r, n := newTestRegistry(t)
	fs := &fakeStorage{}
	if err := r.StorageConnected(proto.StorageMessage{Type: "register", Endpoint: "st:10809"}, fs); err != nil {
		t.Fatal(err)
	}
	return r, n, fs
}

func mustDisk(t *testing.T, r *Registry, name string, sizeGB int) DiskView {
	t.Helper()
	d, err := r.CreateDisk(name, sizeGB)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func createWithDisks(t *testing.T, r *Registry, name string, disks ...AttachRequest) VM {
	t.Helper()
	q := validReq()
	q.Name, q.CPUs, q.MemoryMB, q.Disks = name, 1, 1024, disks
	vm, err := r.CreateVM(q)
	if err != nil {
		t.Fatal(err)
	}
	return vm
}

func mustDelete(t *testing.T, r *Registry, id string) {
	t.Helper()
	if err := r.DeleteVM(id); err != nil {
		t.Fatal(err)
	}
}

func diskOf(t *testing.T, r *Registry, id string) DiskView {
	t.Helper()
	for _, d := range r.Disks() {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("disk %s not listed", id)
	return DiskView{}
}

func markReady(r *Registry, ids ...string) {
	var files []proto.StorageFile
	for _, id := range ids {
		st, _ := r.disks.Get(id)
		files = append(files, proto.StorageFile{Name: proto.StorageFileName(id), SizeBytes: int64(st.SizeGB) << 30})
	}
	r.StorageReport(files)
}

func TestCreateVMWithDisks(t *testing.T) {
	r, n, _ := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 3)
	markReady(r, d.ID)
	vm := createWithDisks(t, r, "dev", AttachRequest{DiskID: d.ID, MountPath: "/data"})
	m := n.last()
	if len(m.Desired) != 1 {
		t.Fatalf("sent = %+v", m)
	}
	want := proto.DiskAttachment{DiskID: d.ID, MountPath: "/data", SizeGB: 3, URL: "nbd://st:10809/" + d.ID + ".img"}
	if a := m.Desired[0].Spec.Attachments; len(a) != 1 || a[0] != want {
		t.Fatalf("attachments = %+v", a)
	}
	if a := r.VMs()[0].Attachments; len(a) != 1 || a[0].URL != "" {
		t.Fatalf("stored attachments = %+v", a)
	}
	att := diskOf(t, r, d.ID).Attachment
	if att == nil || *att != (AttachmentView{VMID: vm.ID, VMName: "dev", NodeID: "n1", MountPath: "/data"}) {
		t.Fatalf("attachment view = %+v", att)
	}
}

func TestAttachConflicts(t *testing.T) {
	r, _, _ := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 1)
	other := mustDisk(t, r, "other", 1)
	createWithDisks(t, r, "a", AttachRequest{DiskID: d.ID, MountPath: "/data"})
	q := validReq()
	q.Name, q.CPUs = "b", 1
	q.Disks = []AttachRequest{{DiskID: d.ID, MountPath: "/data"}}
	if _, err := r.CreateVM(q); !errors.Is(err, ErrConflict) {
		t.Fatalf("attached elsewhere: err = %v", err)
	}
	q.Disks = []AttachRequest{{DiskID: other.ID, MountPath: "/a"}, {DiskID: other.ID, MountPath: "/b"}}
	if _, err := r.CreateVM(q); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same disk twice: err = %v", err)
	}
	st, _ := r.disks.Get(other.ID)
	st.ReleasingOn = "n1"
	r.disks.Put(st)
	q.Disks = []AttachRequest{{DiskID: other.ID, MountPath: "/a"}}
	if _, err := r.CreateVM(q); !errors.Is(err, ErrConflict) {
		t.Fatalf("releasing: err = %v", err)
	}
	if len(r.VMs()) != 1 {
		t.Fatalf("vms = %+v", r.VMs())
	}
}

func TestAttachMountPathRules(t *testing.T) {
	cases := map[string][]AttachRequest{
		"unknown disk":  {{DiskID: "nope", MountPath: "/data"}},
		"root":          {{MountPath: "/"}},
		"relative":      {{MountPath: "data"}},
		"unclean":       {{MountPath: "/a/../srv"}},
		"space":         {{MountPath: "/my data"}},
		"reserved tree": {{MountPath: "/etc"}},
		"below tree":    {{MountPath: "/usr/local"}},
		"ssh below":     {{MountPath: "/home/ubuntu/.ssh/x"}},
		"exact path":    {{MountPath: "/var"}},
		"home":          {{MountPath: "/home/ubuntu"}},
		"dup mount":     {{MountPath: "/a"}, {MountPath: "/a"}},
	}
	for name, disks := range cases {
		t.Run(name, func(t *testing.T) {
			r, _, _ := newAttachRegistry(t)
			for i := range disks {
				if disks[i].DiskID == "" {
					disks[i].DiskID = mustDisk(t, r, string(rune('a'+i)), 1).ID
				}
			}
			q := validReq()
			q.Disks = disks
			if _, err := r.CreateVM(q); !errors.Is(err, ErrInvalid) {
				t.Fatalf("create: err = %v", err)
			}
			vm, err := r.CreateVM(validReq())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.SetVMDisks(vm.ID, disks, true); !errors.Is(err, ErrInvalid) {
				t.Fatalf("set: err = %v", err)
			}
		})
	}
	t.Run("allowed", func(t *testing.T) {
		r, _, _ := newAttachRegistry(t)
		var disks []AttachRequest
		for i, p := range []string{"/data", "/var/cache/build", "/home/ubuntu/work"} {
			disks = append(disks, AttachRequest{DiskID: mustDisk(t, r, string(rune('a'+i)), 1).ID, MountPath: p})
		}
		createWithDisks(t, r, "dev", disks...)
	})
}

func TestDeleteVMReleasesOnConfirm(t *testing.T) {
	r, n, _ := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 1)
	markReady(r, d.ID)
	vm := createWithDisks(t, r, "dev", AttachRequest{DiskID: d.ID, MountPath: "/data"})
	mustDelete(t, r, vm.ID)
	if v := diskOf(t, r, d.ID); v.State != "releasing" || v.ReleasingOn != "n1" || v.Attachment != nil {
		t.Fatalf("view = %+v", v)
	}
	seq := n.last().Seq
	r.DisksInUse("n2", nil, seq)
	if v := diskOf(t, r, d.ID); v.State != "releasing" {
		t.Fatalf("other node released: %+v", v)
	}
	r.DisksInUse("n1", []string{d.ID}, seq)
	if v := diskOf(t, r, d.ID); v.State != "releasing" {
		t.Fatalf("released while in use: %+v", v)
	}
	r.DisksInUse("n1", nil, seq)
	if v := diskOf(t, r, d.ID); v.State != "ready" || v.ReleasingOn != "" {
		t.Fatalf("view = %+v", v)
	}
}

func TestRegisterConfirmsRelease(t *testing.T) {
	r, n, _ := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 1)
	vm := createWithDisks(t, r, "dev", AttachRequest{DiskID: d.ID, MountPath: "/data"})
	r.NodeDisconnected("n1", n)
	mustDelete(t, r, vm.ID)
	// The node saw the attach but not the delete yet.
	reg := proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192, AppliedSeq: n.last().Seq}
	n2 := &fakeNode{}
	if err := r.NodeConnected(reg, n2); err != nil {
		t.Fatal(err)
	}
	if v := diskOf(t, r, d.ID); v.ReleasingOn != "n1" {
		t.Fatalf("released before the delete reached the node: %+v", v)
	}
	reg.AppliedSeq, reg.DisksInUse = n2.last().Seq, []string{d.ID}
	if err := r.NodeConnected(reg, &fakeNode{}); err != nil {
		t.Fatal(err)
	}
	if v := diskOf(t, r, d.ID); v.ReleasingOn != "n1" {
		t.Fatalf("released while in use: %+v", v)
	}
	reg.DisksInUse = nil
	if err := r.NodeConnected(reg, &fakeNode{}); err != nil {
		t.Fatal(err)
	}
	if v := diskOf(t, r, d.ID); v.ReleasingOn != "" {
		t.Fatalf("view = %+v", v)
	}
}

func TestReleasingSurvivesHubRestart(t *testing.T) {
	r, _, _ := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 1)
	vm := createWithDisks(t, r, "dev", AttachRequest{DiskID: d.ID, MountPath: "/data"})
	mustDelete(t, r, vm.ID)
	disks, err := LoadDiskStore(r.disks.path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := LoadVMStore(r.store.path)
	if err != nil {
		t.Fatal(err)
	}
	r2 := NewRegistry(newTestNet(t), store, disks, newTestConfig(t, Config{}))
	if v := diskOf(t, r2, d.ID); v.State != "releasing" || v.ReleasingOn != "n1" {
		t.Fatalf("view = %+v", v)
	}
}

func TestSetVMDisksNeedsRestartAllowed(t *testing.T) {
	r, n, _ := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 1)
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	attach := []AttachRequest{{DiskID: d.ID, MountPath: "/data"}}
	if _, err := r.SetVMDisks(vm.ID, attach, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("running: err = %v", err)
	}
	if _, err := r.SetPower(vm.ID, proto.PowerStopped, proto.StopHard); err != nil {
		t.Fatal(err)
	}
	got, err := r.SetVMDisks(vm.ID, attach, false)
	if err != nil || got.generation != 3 || len(got.Attachments) != 1 {
		t.Fatalf("vm = %+v gen %d err %v", got, got.generation, err)
	}
	if a := n.last().Desired[0].Spec.Attachments; len(a) != 1 || a[0].DiskID != d.ID {
		t.Fatalf("sent = %+v", a)
	}
	if same, _ := r.SetVMDisks(vm.ID, attach, false); same.generation != 3 {
		t.Fatalf("same list bumped: gen %d", same.generation)
	}
	got, err = r.SetVMDisks(vm.ID, nil, false)
	if err != nil || len(got.Attachments) != 0 {
		t.Fatalf("detach: %+v %v", got, err)
	}
	if v := diskOf(t, r, d.ID); v.ReleasingOn != "n1" {
		t.Fatalf("view = %+v", v)
	}
	if _, err := r.SetVMDisks("nope", nil, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestForceRelease(t *testing.T) {
	r, n, _ := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 1)
	if err := r.ForceRelease(d.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("not releasing: err = %v", err)
	}
	vm := createWithDisks(t, r, "dev", AttachRequest{DiskID: d.ID, MountPath: "/data"})
	mustDelete(t, r, vm.ID)
	if err := r.ForceRelease(d.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("online: err = %v", err)
	}
	r.NodeDisconnected("n1", n)
	if err := r.ForceRelease(d.ID); err != nil {
		t.Fatal(err)
	}
	if v := diskOf(t, r, d.ID); v.ReleasingOn != "" {
		t.Fatalf("view = %+v", v)
	}
	if err := r.ForceRelease("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestGrowAttachedBumpsAfterReport(t *testing.T) {
	r, n, _ := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 1)
	markReady(r, d.ID)
	vm := createWithDisks(t, r, "dev", AttachRequest{DiskID: d.ID, MountPath: "/data"})
	if _, err := r.GrowDisk(d.ID, 2, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("running without restartAllowed: err = %v", err)
	}
	sent := n.count()
	if _, err := r.GrowDisk(d.ID, 2, true); err != nil {
		t.Fatal(err)
	}
	r.StorageReport([]proto.StorageFile{{Name: proto.StorageFileName(d.ID), SizeBytes: 1 << 30}})
	if got := r.VMs()[0]; n.count() != sent || got.generation != 1 || got.Attachments[0].SizeGB != 1 {
		t.Fatalf("bumped before the file grew: gen %d %+v", got.generation, got.Attachments)
	}
	markReady(r, d.ID)
	got := r.VMs()[0]
	if got.generation != 2 || got.Attachments[0].SizeGB != 2 || n.count() != sent+1 || n.last().Desired[0].Spec.Attachments[0].SizeGB != 2 {
		t.Fatalf("after report: gen %d %+v", got.generation, got.Attachments)
	}
	markReady(r, d.ID)
	if r.VMs()[0].generation != 2 {
		t.Fatal("bumped again")
	}
	if _, err := r.SetPower(vm.ID, proto.PowerStopped, proto.StopHard); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GrowDisk(d.ID, 3, false); err != nil {
		t.Fatalf("stopped: %v", err)
	}
}

func TestEndpointChangeBumpsVMs(t *testing.T) {
	r, n, fs := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 1)
	markReady(r, d.ID)
	createWithDisks(t, r, "with", AttachRequest{DiskID: d.ID, MountPath: "/data"})
	createWithDisks(t, r, "without")
	if err := r.StorageConnected(proto.StorageMessage{Type: "register", Endpoint: "st:10809"}, fs); err != nil {
		t.Fatal(err)
	}
	gens := func() map[string]uint64 {
		out := map[string]uint64{}
		for _, v := range r.VMs() {
			out[v.Name] = v.generation
		}
		return out
	}
	if g := gens(); g["with"] != 1 || g["without"] != 1 {
		t.Fatalf("same endpoint bumped: %+v", g)
	}
	if err := r.StorageConnected(proto.StorageMessage{Type: "register", Endpoint: "st2:10809"}, fs); err != nil {
		t.Fatal(err)
	}
	if g := gens(); g["with"] != 2 || g["without"] != 1 {
		t.Fatalf("gens = %+v", g)
	}
	var urls []string
	for _, dv := range n.last().Desired {
		for _, a := range dv.Spec.Attachments {
			urls = append(urls, a.URL)
		}
	}
	if len(urls) != 1 || urls[0] != "nbd://st2:10809/"+d.ID+".img" {
		t.Fatalf("urls = %v", urls)
	}
	if err := r.StorageConnected(proto.StorageMessage{Type: "register"}, fs); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty endpoint: err = %v", err)
	}
	if r.Storage().Endpoint != "st2:10809" {
		t.Fatal("empty endpoint saved")
	}
}

func TestDeleteDiskRefusedWhileAttached(t *testing.T) {
	r, _, _ := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 1)
	createWithDisks(t, r, "dev", AttachRequest{DiskID: d.ID, MountPath: "/data"})
	if err := r.DeleteDisk(d.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestCopyDiskWarnsWhenAttachedOrReleasing(t *testing.T) {
	r, _, _ := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 1)
	vm := createWithDisks(t, r, "dev", AttachRequest{DiskID: d.ID, MountPath: "/data"})
	if _, warn, err := r.CopyDisk(d.ID, "c1"); err != nil || warn == "" {
		t.Fatalf("attached: %q %v", warn, err)
	}
	mustDelete(t, r, vm.ID)
	if _, warn, err := r.CopyDisk(d.ID, "c2"); err != nil || warn == "" {
		t.Fatalf("releasing: %q %v", warn, err)
	}
}

func TestStaleDisksMessageKeepsReleasing(t *testing.T) {
	r, n, _ := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 1)
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetPower(vm.ID, proto.PowerStopped, proto.StopHard); err != nil {
		t.Fatal(err)
	}
	beforeAttach := n.last().Seq
	if _, err := r.SetVMDisks(vm.ID, []AttachRequest{{DiskID: d.ID, MountPath: "/data"}}, false); err != nil {
		t.Fatal(err)
	}
	attachSeq := n.last().Seq
	if _, err := r.SetVMDisks(vm.ID, nil, false); err != nil {
		t.Fatal(err)
	}
	detachSeq := n.last().Seq
	if !(beforeAttach < attachSeq && attachSeq < detachSeq) {
		t.Fatalf("seqs %d %d %d", beforeAttach, attachSeq, detachSeq)
	}
	// sent before the node applied the attach, processed after the detach
	r.DisksInUse("n1", nil, beforeAttach)
	r.DisksInUse("n1", nil, attachSeq)
	if v := diskOf(t, r, d.ID); v.ReleasingOn != "n1" {
		t.Fatalf("stale message released: %+v", v)
	}
	r.DisksInUse("n1", nil, detachSeq)
	if v := diskOf(t, r, d.ID); v.ReleasingOn != "" {
		t.Fatalf("view = %+v", v)
	}
}

func TestDesiredSeqSurvivesHubRestart(t *testing.T) {
	r, n, _ := newAttachRegistry(t)
	createWithDisks(t, r, "dev")
	last := n.last().Seq
	if last == 0 {
		t.Fatal("desired without seq")
	}
	store, err := LoadVMStore(r.store.path)
	if err != nil {
		t.Fatal(err)
	}
	r2 := NewRegistry(newTestNet(t), store, newTestDisks(t), newTestConfig(t, Config{}))
	n2 := &fakeNode{}
	if err := r2.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192}, n2); err != nil {
		t.Fatal(err)
	}
	if got := n2.last().Seq; got <= last {
		t.Fatalf("seq after restart = %d, before %d", got, last)
	}
}

func TestAttachSizeFollowsReportedFile(t *testing.T) {
	r, n, _ := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 1)
	markReady(r, d.ID)
	if _, err := r.GrowDisk(d.ID, 3, false); err != nil {
		t.Fatal(err)
	}
	vm := createWithDisks(t, r, "dev", AttachRequest{DiskID: d.ID, MountPath: "/data"})
	if a := r.VMs()[0].Attachments[0]; a.SizeGB != 1 {
		t.Fatalf("attached size = %d before the file grew", a.SizeGB)
	}
	markReady(r, d.ID)
	got := r.VMs()[0]
	if got.ID != vm.ID || got.Attachments[0].SizeGB != 3 || got.generation != 2 || n.last().Desired[0].Spec.Attachments[0].SizeGB != 3 {
		t.Fatalf("after report: gen %d %+v", got.generation, got.Attachments)
	}
}

func TestDiskSeenStopsCreate(t *testing.T) {
	r, _, fs := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 2)
	if m := fs.last(); len(m.Disks) != 1 || !m.Disks[0].Create {
		t.Fatalf("new disk: desired = %+v", m)
	}
	r.StorageReport([]proto.StorageFile{{Name: proto.StorageFileName(d.ID), SizeBytes: 1 << 30}})
	if st, _ := r.disks.Get(d.ID); st.Seen {
		t.Fatal("seen below full size")
	}
	markReady(r, d.ID)
	disks, err := LoadDiskStore(r.disks.path)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := disks.Get(d.ID); !st.Seen {
		t.Fatal("seen not persisted")
	}
	r.pushStorage()
	if m := fs.last(); len(m.Disks) != 1 || m.Disks[0].Create {
		t.Fatalf("seen disk: desired = %+v", m)
	}
}

func TestSeenDiskMissing(t *testing.T) {
	r, _, fs := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 1)
	r.StorageReport(nil)
	if v := diskOf(t, r, d.ID); v.State != "creating" {
		t.Fatalf("never seen: state = %q", v.State)
	}
	markReady(r, d.ID)
	r.StorageReport(nil)
	if v := diskOf(t, r, d.ID); v.State != "missing" {
		t.Fatalf("state = %q", v.State)
	}
	q := validReq()
	q.Name, q.CPUs, q.MemoryMB, q.Disks = "dev", 1, 1024, []AttachRequest{{DiskID: d.ID, MountPath: "/data"}}
	if _, err := r.CreateVM(q); !errors.Is(err, ErrConflict) {
		t.Fatalf("attach missing: err = %v", err)
	}
	r.StorageDisconnected(fs)
	if err := r.StorageConnected(proto.StorageMessage{Type: "register", Endpoint: "st:10809"}, fs); err != nil {
		t.Fatal(err)
	}
	if v := diskOf(t, r, d.ID); v.State == "missing" {
		t.Fatal("missing before the reconnected storage reported")
	}
}

func TestAttachURLWaitsForSeen(t *testing.T) {
	r, n, _ := newAttachRegistry(t)
	d := mustDisk(t, r, "data", 1)
	createWithDisks(t, r, "dev", AttachRequest{DiskID: d.ID, MountPath: "/data"})
	if a := n.last().Desired[0].Spec.Attachments[0]; a.URL != "" {
		t.Fatalf("url before seen = %q", a.URL)
	}
	sent := n.count()
	markReady(r, d.ID)
	want := "nbd://st:10809/" + d.ID + ".img"
	if got := r.VMs()[0]; got.generation != 2 || n.count() != sent+1 || n.last().Desired[0].Spec.Attachments[0].URL != want {
		t.Fatalf("after seen: gen %d, desired %+v", got.generation, n.last().Desired)
	}
	markReady(r, d.ID)
	if r.VMs()[0].generation != 2 {
		t.Fatal("bumped again")
	}
	disks, err := LoadDiskStore(r.disks.path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := LoadVMStore(r.store.path)
	if err != nil {
		t.Fatal(err)
	}
	r2 := NewRegistry(newTestNet(t), store, disks, Config{})
	n2 := &fakeNode{}
	if err := r2.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192}, n2); err != nil {
		t.Fatal(err)
	}
	if a := n2.last().Desired[0].Spec.Attachments[0]; a.URL != want {
		t.Fatalf("url after hub restart = %q", a.URL)
	}
}

func TestStorageRegisterNeedsHostPort(t *testing.T) {
	r, _, fs := newAttachRegistry(t)
	for _, ep := range []string{"storage", "storage:1:2", "nbd://storage:10809"} {
		if err := r.StorageConnected(proto.StorageMessage{Type: "register", Endpoint: ep}, fs); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: err = %v", ep, err)
		}
	}
	if r.Storage().Endpoint != "st:10809" {
		t.Fatal("invalid endpoint saved")
	}
}
