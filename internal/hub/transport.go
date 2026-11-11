package hub

import (
	"context"
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
	mu  sync.Mutex
	enc *json.Encoder
}

func (n *yamuxNode) Send(m proto.Message) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.enc.Encode(m)
}

// NodeHandler accepts node connections: WebSocket → yamux, first node-opened stream is the control stream.
func NodeHandler(r *Registry, token func() string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer "+token() {
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
		node := &yamuxNode{enc: proto.NewEncoder(ctrl)}
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
			if m.Type == "status" && m.Status != nil {
				r.UpdateStatus(id, *m.Status)
			}
		}
	})
}

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	return cfg
}
