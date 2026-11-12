package hub

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"

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
		r.StorageConnected(m, conn)
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
func (r *Registry) StorageConnected(m proto.StorageMessage, c StorageConn) {
	r.mu.Lock()
	r.storage = c
	if r.disks.Endpoint() != m.Endpoint {
		if err := r.disks.SetEndpoint(m.Endpoint); err != nil {
			log.Printf("storage: saving endpoint: %v", err)
		}
	}
	r.mu.Unlock()
	r.pushStorage()
}

// StorageDisconnected ignores c if the agent has already reconnected with a newer connection.
func (r *Registry) StorageDisconnected(c StorageConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.storage == c {
		r.storage = nil
	}
}

// StorageReport records the agent's files, ends finished copies and drops tombstones whose file is gone.
func (r *Registry) StorageReport(files []proto.StorageFile) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.storageFiles = files
	present := map[string]bool{}
	for _, f := range files {
		present[f.Name] = true
	}
	for _, d := range r.disks.Disks() {
		if d.CopyFrom != "" && present[proto.StorageFileName(d.ID)] {
			d.CopyFrom = ""
			if err := r.disks.Put(d); err != nil {
				log.Printf("disk %s: clearing copy source: %v", d.ID, err)
			}
		}
	}
	for _, id := range r.disks.Tombstones() {
		if !present[proto.StorageFileName(id)] {
			if err := r.disks.ClearTombstone(id); err != nil {
				log.Printf("disk %s: clearing tombstone: %v", id, err)
			}
		}
	}
}

// pushStorage sends the agent every disk and tombstone if it is online.
func (r *Registry) pushStorage() {
	r.storageSendMu.Lock()
	defer r.storageSendMu.Unlock()
	r.mu.Lock()
	conn := r.storage
	m := proto.StorageMessage{Type: "desired", Deleted: r.disks.Tombstones()}
	for _, d := range r.disks.Disks() {
		m.Disks = append(m.Disks, proto.StorageDisk{ID: d.ID, SizeBytes: int64(d.SizeGB) << 30, CopyFrom: d.CopyFrom})
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
