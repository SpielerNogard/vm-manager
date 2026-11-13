package hub

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"vm-manager/internal/proto"
)

type yamuxNode struct {
	mu   sync.Mutex
	enc  *json.Encoder
	sess *yamux.Session
}

func (n *yamuxNode) Close() error { return n.sess.Close() }

func (n *yamuxNode) Send(m proto.Message) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.enc.Encode(m)
}

// NodeHandler accepts node connections: WebSocket → yamux, first node-opened stream is the control stream.
func NodeHandler(r *Registry, token func() string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+token())) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sess, err := yamux.Server(websocket.NetConn(ctx, c, websocket.MessageBinary), yamuxConfig())
		if err != nil {
			return
		}
		defer sess.Close()
		ctrl, err := sess.Accept()
		if err != nil {
			return
		}
		dec := json.NewDecoder(ctrl)
		var m proto.Message
		if err := dec.Decode(&m); err != nil || m.Type != "register" || m.Register == nil {
			log.Printf("node %s: expected register message", req.RemoteAddr)
			return
		}
		node := &yamuxNode{enc: proto.NewEncoder(ctrl), sess: sess}
		id := m.Register.NodeID
		if err := r.NodeConnected(*m.Register, node); err != nil {
			log.Printf("node %s refused: %v", id, err)
			return
		}
		log.Printf("node %s connected (%d VMs)", id, len(m.Register.VMs))
		defer func() {
			r.NodeDisconnected(id, node)
			log.Printf("node %s disconnected", id)
		}()
		for {
			var m proto.Message
			if err := dec.Decode(&m); err != nil {
				return
			}
			switch {
			case m.Type == "status" && m.Status != nil:
				r.UpdateStatus(id, *m.Status)
			case m.Type == "disks":
				r.DisksInUse(id, m.DisksInUse, m.AppliedSeq)
			case m.Type == "updateFailed":
				r.NodeUpdateFailed(id, node, m.Error)
			}
		}
	})
}

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	return cfg
}
