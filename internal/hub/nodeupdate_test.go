package hub

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"vm-manager/internal/proto"
)

func updatableNode(t *testing.T) (*Registry, *fakeNode) {
	t.Helper()
	setHubVersion(t, "v0.5.0")
	r, _ := newTestRegistry(t)
	n := &fakeNode{}
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", Version: "v0.4.0", Updatable: true}, n)
	return r, n
}

func updatesSent(n *fakeNode) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, m := range n.sent {
		if m.Type == "update" {
			c++
		}
	}
	return c
}

func TestUpdateNode(t *testing.T) {
	r, n := updatableNode(t)
	if err := r.UpdateNode("n1"); err != nil {
		t.Fatal(err)
	}
	if c := updatesSent(n); c != 1 {
		t.Errorf("update messages sent = %d, want 1", c)
	}
	if node, _ := nodeByID(r, "n1"); !node.Updating {
		t.Errorf("node = %+v, want updating", node)
	}
}

func TestUpdateNodeRefused(t *testing.T) {
	cases := []struct {
		name    string
		hub     string
		prepare func(r *Registry, n *fakeNode)
		is      error
		want    string
	}{
		{"unknown node", "v0.5.0", func(r *Registry, n *fakeNode) {}, ErrNotFound, "n9"},
		{"offline", "v0.5.0", func(r *Registry, n *fakeNode) { r.NodeDisconnected("n1", n) }, ErrConflict, "offline"},
		{"no version", "v0.5.0", func(r *Registry, n *fakeNode) {
			r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", Updatable: true}, n)
		}, ErrConflict, "reports no version"},
		{"same version", "v0.5.0", func(r *Registry, n *fakeNode) {
			r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", Version: "v0.5.0", Updatable: true}, n)
		}, ErrConflict, "not older than the hub"},
		{"started by hand", "v0.5.0", func(r *Registry, n *fakeNode) {
			r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", Version: "v0.4.0"}, n)
		}, ErrConflict, "not installed by the installer"},
		{"dev hub", "dev", func(r *Registry, n *fakeNode) {}, ErrConflict, "development build"},
		{"already running", "v0.5.0", func(r *Registry, n *fakeNode) { r.UpdateNode("n1") }, ErrConflict, "already running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, n := updatableNode(t)
			setHubVersion(t, tc.hub)
			tc.prepare(r, n)
			before := updatesSent(n)
			id := "n1"
			if tc.is == ErrNotFound {
				id = "n9"
			}
			err := r.UpdateNode(id)
			if !errors.Is(err, tc.is) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %v containing %q", err, tc.is, tc.want)
			}
			if updatesSent(n) != before {
				t.Error("an update was sent although it was refused")
			}
		})
	}
}

func TestNodeUpdateFailed(t *testing.T) {
	r, _ := updatableNode(t)
	if err := r.UpdateNode("n1"); err != nil {
		t.Fatal(err)
	}
	r.NodeUpdateFailed("n1", "boom")
	if node, _ := nodeByID(r, "n1"); node.Updating || node.UpdateError != "boom" {
		t.Errorf("after a failure: %+v", node)
	}
	if err := r.UpdateNode("n1"); err != nil {
		t.Fatalf("retry refused: %v", err)
	}
	if node, _ := nodeByID(r, "n1"); !node.Updating || node.UpdateError != "" {
		t.Errorf("after a retry: %+v", node)
	}
}

func TestRegisterClearsUpdate(t *testing.T) {
	r, n := updatableNode(t)
	reg := proto.Register{NodeID: "n1", Provider: "vz", Version: "v0.5.0", Updatable: true}
	r.UpdateNode("n1")
	r.NodeConnected(reg, n)
	if node, _ := nodeByID(r, "n1"); node.Updating || node.UpdateError != "" {
		t.Errorf("after re-registering during an update: %+v", node)
	}
	reg.Version = "v0.4.0"
	r.NodeConnected(reg, n)
	r.UpdateNode("n1")
	r.NodeUpdateFailed("n1", "boom")
	r.NodeConnected(reg, n)
	if node, _ := nodeByID(r, "n1"); node.Updating || node.UpdateError != "" {
		t.Errorf("after re-registering with an old error: %+v", node)
	}
}

func TestTransportUpdateFailed(t *testing.T) {
	setHubVersion(t, "v0.5.0")
	r, srv := startHub(t)
	n, err := dialNode(t, srv.URL, testToken, proto.Register{NodeID: "n1", Provider: "vz", Version: "v0.4.0", Updatable: true})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "node online", func() bool { node, ok := nodeByID(r, "n1"); return ok && node.State == NodeOnline })
	if err := n.enc.Encode(proto.Message{Type: "updateFailed", Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "update error", func() bool { node, _ := nodeByID(r, "n1"); return node.UpdateError == "boom" })
}

func TestUpdateNodeAPI(t *testing.T) {
	setHubVersion(t, "v0.5.0")
	srv, r, _ := apiServerWith(t, testAPIConfig())
	n := &fakeNode{}
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", Version: "v0.4.0", Updatable: true}, n)
	post := func(id string) (int, string) {
		t.Helper()
		resp, err := http.Post(srv.URL+"/api/nodes/"+id+"/update", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := make([]byte, 512)
		k, _ := resp.Body.Read(b)
		return resp.StatusCode, string(b[:k])
	}
	if code, body := post("n1"); code != http.StatusNoContent {
		t.Errorf("update: status %d %s", code, body)
	}
	if code, _ := post("n9"); code != http.StatusNotFound {
		t.Errorf("unknown node: status %d", code)
	}
	r.NodeDisconnected("n1", n)
	if code, body := post("n1"); code != http.StatusConflict || !strings.Contains(body, "offline") {
		t.Errorf("offline node: status %d %s", code, body)
	}
}
