package vz

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vm-manager/internal/netframe"
)

// fakeNetvm hands out one end of a net.Pipe per dial after failing the first failDials dials.
type fakeNetvm struct {
	failDials int32
	dials     atomic.Int32
	conns     chan net.Conn
}

func (f *fakeNetvm) dial() (net.Conn, error) {
	if f.dials.Add(1) <= f.failDials {
		return nil, errors.New("netvm not up")
	}
	a, b := net.Pipe()
	f.conns <- b
	return a, nil
}

func startRelay(t *testing.T, failDials int32) (guest *os.File, nv *fakeNetvm) {
	t.Helper()
	old := relayRetry
	relayRetry = 10 * time.Millisecond
	guestEnd, vmEnd, err := newNICSocketpair()
	if err != nil {
		t.Fatal(err)
	}
	nv = &fakeNetvm{failDials: failDials, conns: make(chan net.Conn, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		relayNIC(ctx, vmEnd, nv.dial, 7, "vm1-mgmt")
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		relayRetry = old
		guestEnd.Close()
		vmEnd.Close()
	})
	return guestEnd, nv
}

func nextConn(t *testing.T, nv *fakeNetvm) net.Conn {
	t.Helper()
	select {
	case c := <-nv.conns:
		c.SetDeadline(time.Now().Add(5 * time.Second))
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not dial")
		return nil
	}
}

func expectFrame(t *testing.T, c net.Conn, typ byte, payload string) {
	t.Helper()
	gt, port, p, err := netframe.Read(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gt != typ || port != 7 || string(p) != payload {
		t.Fatalf("got frame type %d port %d %q, want type %d port 7 %q", gt, port, p, typ, payload)
	}
}

func TestRelayRetriesAndReattaches(t *testing.T) {
	guest, nv := startRelay(t, 2)
	c := nextConn(t, nv)
	expectFrame(t, c, netframe.AddPort, "vm1-mgmt")
	if n := nv.dials.Load(); n != 3 {
		t.Fatalf("dials = %d, want 3", n)
	}
	if _, err := guest.Write([]byte("frame-from-guest")); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, c, netframe.Ethernet, "frame-from-guest")

	c.Close()
	c2 := nextConn(t, nv)
	expectFrame(t, c2, netframe.AddPort, "vm1-mgmt")
	if _, err := guest.Write([]byte("after-reconnect")); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, c2, netframe.Ethernet, "after-reconnect")
}

func TestRelayDeliversFramesToGuest(t *testing.T) {
	guest, nv := startRelay(t, 0)
	c := nextConn(t, nv)
	expectFrame(t, c, netframe.AddPort, "vm1-mgmt")
	frame := bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 375)
	if err := netframe.Write(c, netframe.Ethernet, 7, frame); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 65536)
	n, err := guest.Read(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:n], frame) {
		t.Fatalf("guest got %d bytes, want %d identical bytes", n, len(frame))
	}
}

func TestNetvmSeedContainsAgentAndUnit(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "netagent-linux-arm64")
	if err := os.WriteFile(agent, []byte("agent-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	files, err := netvmSeedFiles(agent, "02:00:00:00:00:01")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"netagent.service", "LABEL=cidata", "openvswitch-switch", "ovn-host", "wireguard-tools", "/usr/local/bin/netagent"} {
		if !strings.Contains(string(files["user-data"]), want) {
			t.Errorf("user-data lacks %q", want)
		}
	}
	nc := string(files["network-config"])
	if !strings.Contains(nc, "dhcp-identifier: mac") || !strings.Contains(nc, "02:00:00:00:00:01") {
		t.Errorf("network-config = %q", nc)
	}
	if _, ok := files["meta-data"]; !ok {
		t.Error("meta-data missing")
	}
	if b := files["netagent"]; string(b) != "agent-binary" {
		t.Fatalf("agent copy = %q", b)
	}
}
