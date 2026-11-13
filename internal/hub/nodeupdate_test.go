package hub

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

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
	r, n := updatableNode(t)
	if err := r.UpdateNode("n1"); err != nil {
		t.Fatal(err)
	}
	r.NodeUpdateFailed("n1", n, "boom")
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
	r.NodeUpdateFailed("n1", n, "boom")
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

// lockingNode reads the registry while sending, as a slow connection would let other requests do.
type lockingNode struct {
	fakeNode
	r    *Registry
	fail bool
}

func (n *lockingNode) Send(m proto.Message) error {
	if m.Type == "update" {
		n.r.Nodes()
		if n.fail {
			return errors.New("connection lost")
		}
	}
	return n.fakeNode.Send(m)
}

func TestUpdateNodeSendsWithoutRegistryLock(t *testing.T) {
	setHubVersion(t, "v0.5.0")
	r, _ := newTestRegistry(t)
	n := &lockingNode{r: r}
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", Version: "v0.4.0", Updatable: true}, n)
	done := make(chan error, 1)
	go func() { done <- r.UpdateNode("n1") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("UpdateNode holds the registry lock while sending")
	}
	n.fail = true
	r.NodeUpdateFailed("n1", n, "boom")
	if err := r.UpdateNode("n1"); err == nil {
		t.Fatal("a failed send returned no error")
	}
	if node, _ := nodeByID(r, "n1"); node.Updating {
		t.Error("node is updating although the command never reached it")
	}
}

func TestNodeUpdateFailedFromOldConnection(t *testing.T) {
	r, old := updatableNode(t)
	r.UpdateNode("n1")
	// The updated node has registered again; the old process's report must not mark the update as failed.
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", Version: "v0.5.0", Updatable: true}, &fakeNode{})
	r.NodeUpdateFailed("n1", old, "installer: signal: terminated")
	if node, _ := nodeByID(r, "n1"); node.UpdateError != "" {
		t.Errorf("stale report shown: %q", node.UpdateError)
	}
}

func TestUpdateNodeExpires(t *testing.T) {
	r, n := updatableNode(t)
	if err := r.UpdateNode("n1"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.nodes["n1"].updatingSince = time.Now().Add(-updateTimeout - time.Second)
	r.mu.Unlock()
	node, _ := nodeByID(r, "n1")
	if node.Updating || !strings.Contains(node.UpdateError, "did not finish") {
		t.Errorf("an update without an answer still shows as running: %+v", node)
	}
	if err := r.UpdateNode("n1"); err != nil {
		t.Fatalf("retry after the timeout refused: %v", err)
	}
	if c := updatesSent(n); c != 2 {
		t.Errorf("update messages sent = %d, want 2", c)
	}
}
