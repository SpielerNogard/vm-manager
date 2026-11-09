package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"vm-manager/internal/proto"
)

type yamuxNode struct {
	sess *yamux.Session
	mu   sync.Mutex
	enc  *json.Encoder
}

func (n *yamuxNode) Send(m proto.Message) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.enc.Encode(m)
}

func (n *yamuxNode) OpenStream() (net.Conn, error) { return n.sess.Open() }

// NodeHandler accepts node connections: WebSocket → yamux, first node-opened stream is the control stream.
func NodeHandler(r *Registry, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer "+token {
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
		node := &yamuxNode{sess: sess, enc: proto.NewEncoder(ctrl)}
		id := m.Register.NodeID
		r.NodeConnected(*m.Register, node)
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
				r.UpdateStatus(*m.Status)
			}
		}
	})
}

// ServeSSHPort forwards every TCP connection on port to the VM currently mapped to it.
func ServeSSHPort(r *Registry, port int) (io.Closer, error) {
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				s, err := r.StreamForPort(port)
				if err != nil {
					c.Close()
					return
				}
				proto.Pipe(c, s)
			}()
		}
	}()
	return l, nil
}

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	return cfg
}
