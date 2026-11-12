package hub

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"slices"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"vm-manager/internal/proto"
)

// storageMessageLimit bounds one link message; desired lists and reports grow with the number of disks.
const storageMessageLimit = 4 << 20

type StorageConn interface {
	Send(proto.StorageMessage) error
}

type StorageView struct {
	Online   bool                `json:"online"`
	Endpoint string              `json:"endpoint"`
	Orphans  []proto.StorageFile `json:"orphans"`
}

type wsStorage struct {
	ctx context.Context
	c   *websocket.Conn
}

func (s *wsStorage) Send(m proto.StorageMessage) error { return wsjson.Write(s.ctx, s.c, m) }

// StorageHandler accepts the storage agent's WebSocket; its first message must be "register".
func StorageHandler(r *Registry, token func() string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+token())) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(storageMessageLimit)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var m proto.StorageMessage
		if err := wsjson.Read(ctx, c, &m); err != nil || m.Type != "register" {
			log.Printf("storage %s: expected register message", req.RemoteAddr)
			return
		}
		conn := &wsStorage{ctx: ctx, c: c}
		if err := r.StorageConnected(m, conn); err != nil {
			log.Printf("storage %s refused: %v", req.RemoteAddr, err)
			return
		}
		log.Printf("storage connected, endpoint %s", m.Endpoint)
		defer func() {
			r.StorageDisconnected(conn)
			log.Printf("storage disconnected")
		}()
		for {
			var m proto.StorageMessage
			if err := wsjson.Read(ctx, c, &m); err != nil {
				return
			}
			if m.Type == "report" {
				r.StorageReport(m.Files)
			}
		}
	})
}

// StorageConnected makes c the storage connection (newest wins), saves its endpoint and sends it the desired disks.
// A new endpoint changes every attachment URL, so VMs with disks get a new generation.
func (r *Registry) StorageConnected(m proto.StorageMessage, c StorageConn) error {
	if !proto.ValidEndpoint(m.Endpoint) {
		return fmt.Errorf("%w: register endpoint %q is not host:port", ErrInvalid, m.Endpoint)
	}
	r.mu.Lock()
	r.storage, r.storageReported = c, false
	nodes := map[string]bool{}
	if r.disks.Endpoint() != m.Endpoint {
		if err := r.disks.SetEndpoint(m.Endpoint); err != nil {
			log.Printf("storage: saving endpoint: %v", err)
		} else {
			for _, vm := range r.vms {
				if len(vm.Attachments) == 0 {
					continue
				}
				if _, err := r.bumpLocked(vm, func(*VM) {}); err != nil {
					log.Printf("vm %s: saving: %v", vm.ID, err)
					continue
				}
				nodes[vm.NodeID] = true
			}
		}
	}
	r.mu.Unlock()
	r.pushStorage()
	for n := range nodes {
		r.push(n)
	}
	return nil
}

// StorageDisconnected ignores c if the agent has already reconnected with a newer connection.
func (r *Registry) StorageDisconnected(c StorageConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.storage == c {
		r.storage, r.storageReported = nil, false
	}
}

// StorageReport records the agent's files, ends finished copies and drops tombstones whose file is gone.
// VMs using a disk seen for the first time get a new generation, as only now their attachment gets a URL.
func (r *Registry) StorageReport(files []proto.StorageFile) {
	r.mu.Lock()
	repush, seen := r.storageReportLocked(files)
	nodes := r.growAttachedLocked(files)
	for _, id := range seen {
		vm, _ := r.attachmentLocked(id)
		if vm == nil {
			continue
		}
		if _, err := r.bumpLocked(vm, func(*VM) {}); err != nil {
			log.Printf("vm %s: saving: %v", vm.ID, err)
			continue
		}
		nodes[vm.NodeID] = true
	}
	r.mu.Unlock()
	if repush {
		r.pushStorage()
	}
	for n := range nodes {
		r.push(n)
	}
}

// growAttachedLocked gives the new size to VMs whose disk file has grown and returns their nodes; r.mu must be held.
func (r *Registry) growAttachedLocked(files []proto.StorageFile) map[string]bool {
	sizes := map[string]int64{}
	for _, f := range files {
		sizes[f.Name] = f.SizeBytes
	}
	nodes := map[string]bool{}
	for _, vm := range r.vms {
		atts := slices.Clone(vm.Attachments)
		grown := false
		for i, a := range atts {
			d, ok := r.disks.Get(a.DiskID)
			if ok && a.SizeGB < d.SizeGB && sizes[proto.StorageFileName(d.ID)] >= int64(d.SizeGB)<<30 {
				atts[i].SizeGB, grown = d.SizeGB, true
			}
		}
		if !grown {
			continue
		}
		if _, err := r.bumpLocked(vm, func(vm *VM) { vm.Attachments = atts }); err != nil {
			log.Printf("vm %s: saving: %v", vm.ID, err)
			continue
		}
		nodes[vm.NodeID] = true
	}
	return nodes
}

// storageReportLocked reports whether a copy's source has appeared, which the agent skipped earlier, and the disks seen for the first time.
func (r *Registry) storageReportLocked(files []proto.StorageFile) (repush bool, seen []string) {
	r.storageFiles, r.storageReported = files, true
	sizes := map[string]int64{}
	for _, f := range files {
		sizes[f.Name] = f.SizeBytes
	}
	present := func(id string) bool { _, ok := sizes[proto.StorageFileName(id)]; return ok }
	for _, d := range r.disks.Disks() {
		changed, first := false, false
		switch {
		case d.CopyFrom == "":
		case present(d.ID):
			d.CopyFrom, changed = "", true
		case present(d.CopyFrom):
			repush = true
		}
		if !d.Seen && sizes[proto.StorageFileName(d.ID)] >= int64(d.SizeGB)<<30 {
			d.Seen, changed, first = true, true, true
		}
		if !changed {
			continue
		}
		if err := r.disks.Put(d); err != nil {
			log.Printf("disk %s: saving reported state: %v", d.ID, err)
		} else if first {
			seen = append(seen, d.ID)
		}
	}
	for _, id := range r.disks.Tombstones() {
		if !present(id) {
			if err := r.disks.ClearTombstone(id); err != nil {
				log.Printf("disk %s: clearing tombstone: %v", id, err)
			}
		}
	}
	return repush, seen
}

// pushStorage sends the agent every disk and tombstone if it is online.
func (r *Registry) pushStorage() {
	r.storageSendMu.Lock()
	defer r.storageSendMu.Unlock()
	r.mu.Lock()
	conn := r.storage
	m := proto.StorageMessage{Type: "desired", Deleted: r.disks.Tombstones()}
	for _, d := range r.disks.Disks() {
		m.Disks = append(m.Disks, proto.StorageDisk{ID: d.ID, SizeBytes: int64(d.SizeGB) << 30, CopyFrom: d.CopyFrom, Create: !d.Seen})
	}
	r.mu.Unlock()
	if conn == nil {
		return
	}
	if err := conn.Send(m); err != nil {
		log.Printf("storage: sending desired disks: %v", err)
	}
}

func (r *Registry) Storage() StorageView {
	r.mu.Lock()
	defer r.mu.Unlock()
	known := map[string]bool{}
	for _, d := range r.disks.Disks() {
		known[proto.StorageFileName(d.ID)] = true
	}
	for _, id := range r.disks.Tombstones() {
		known[proto.StorageFileName(id)] = true
	}
	orphans := []proto.StorageFile{}
	for _, f := range r.storageFiles {
		if !known[f.Name] {
			orphans = append(orphans, f)
		}
	}
	return StorageView{Online: r.storage != nil, Endpoint: r.disks.Endpoint(), Orphans: orphans}
}
