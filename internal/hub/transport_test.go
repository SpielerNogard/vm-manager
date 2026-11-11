package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"vm-manager/internal/proto"
)

const testToken = "secret"

type testNode struct {
	sess *yamux.Session
	enc  *json.Encoder
	dec  *json.Decoder
}

func dialNode(t *testing.T, srvURL, token string, reg proto.Register) (*testNode, error) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srvURL, "http") + "/api/nodes/connect"
	c, resp, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		if resp != nil {
			return nil, errors.New(resp.Status)
		}
		return nil, err
	}
	sess, err := yamux.Client(websocket.NetConn(context.Background(), c, websocket.MessageBinary), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctrl, err := sess.Open()
	if err != nil {
		t.Fatal(err)
	}
	n := &testNode{sess: sess, enc: proto.NewEncoder(ctrl), dec: json.NewDecoder(ctrl)}
	if err := n.enc.Encode(proto.Message{Type: "register", Register: &reg}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return n, nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func startHub(t *testing.T) (*Registry, *httptest.Server) {
	t.Helper()
	r := NewRegistry(newTestNet(t), Config{})
	srv := httptest.NewServer(NodeHandler(r, testToken))
	t.Cleanup(srv.Close)
	return r, srv
}

var testReg = proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192}

func TestNodeRegisterAndCreate(t *testing.T) {
	r, srv := startHub(t)
	n, err := dialNode(t, srv.URL, testToken, testReg)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "node online", func() bool { ns := r.Nodes(); return len(ns) == 1 && ns[0].Online })

	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	var m proto.Message
	if err := n.dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m.Type != "create" || m.Create.ID != vm.ID {
		t.Fatalf("got %+v", m)
	}

	if err := n.enc.Encode(proto.Message{Type: "status", Status: &proto.VMStatus{ID: vm.ID, State: proto.StateRunning, IP: "10.0.0.2"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool { return r.VMs()[0].State == proto.StateRunning })
}

func TestBadToken(t *testing.T) {
	_, srv := startHub(t)
	_, err := dialNode(t, srv.URL, "wrong", testReg)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestStaleDisconnectKeepsNewSessionOnline(t *testing.T) {
	r, srv := startHub(t)
	old, err := dialNode(t, srv.URL, testToken, testReg)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "node online", func() bool { return len(r.Nodes()) == 1 })
	if _, err := dialNode(t, srv.URL, testToken, testReg); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	old.sess.Close()
	time.Sleep(200 * time.Millisecond)
	if !r.Nodes()[0].Online {
		t.Fatal("old session's disconnect took the new session offline")
	}
}
