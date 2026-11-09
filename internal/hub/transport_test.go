package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startHub(t *testing.T, portMin, portMax int) (*Registry, *httptest.Server) {
	t.Helper()
	r := NewRegistry(portMin, portMax)
	listeners := map[int]io.Closer{}
	r.OnPortOpen = func(p int) error {
		l, err := ServeSSHPort(r, p)
		if err == nil {
			listeners[p] = l
		}
		return err
	}
	r.OnPortClose = func(p int) { listeners[p].Close(); delete(listeners, p) }
	srv := httptest.NewServer(NodeHandler(r, testToken))
	t.Cleanup(func() {
		srv.Close()
		for _, l := range listeners {
			l.Close()
		}
	})
	return r, srv
}

var testReg = proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192}

func TestNodeRegisterAndCreate(t *testing.T) {
	p := freePort(t)
	r, srv := startHub(t, p, p)
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
	_, srv := startHub(t, 22000, 22999)
	_, err := dialNode(t, srv.URL, "wrong", testReg)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestSSHProxy(t *testing.T) {
	p := freePort(t)
	r, srv := startHub(t, p, p)
	n, err := dialNode(t, srv.URL, testToken, testReg)
	if err != nil {
		t.Fatal(err)
	}
	gotID := make(chan string, 1)
	go func() {
		for {
			s, err := n.sess.Accept()
			if err != nil {
				return
			}
			br := bufio.NewReader(s)
			h, err := proto.ReadHeader(br)
			if err != nil {
				return
			}
			gotID <- h.VMID
			go io.Copy(s, br)
		}
	}()
	waitFor(t, "node online", func() bool { return len(r.Nodes()) == 1 })
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}

	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(vm.SSHPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("ping"))
	buf := make([]byte, 4)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("read %q err %v", buf, err)
	}
	if id := <-gotID; id != vm.ID {
		t.Fatalf("header VMID = %q", id)
	}
}

func TestSSHProxyNodeOffline(t *testing.T) {
	p := freePort(t)
	r, srv := startHub(t, p, p)
	n, err := dialNode(t, srv.URL, testToken, testReg)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "node online", func() bool { return len(r.Nodes()) == 1 })
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	n.sess.Close()
	waitFor(t, "node offline", func() bool { return !r.Nodes()[0].Online })

	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(vm.SSHPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("err = %v, want EOF", err)
	}
}

func TestStaleDisconnectKeepsNewSessionOnline(t *testing.T) {
	r, srv := startHub(t, 22000, 22999)
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
