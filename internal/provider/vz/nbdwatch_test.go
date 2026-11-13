//go:build darwin

package vz

import (
	"errors"
	"net"
	"testing"
	"time"
)

func TestNBDServerUp(t *testing.T) {
	serve := func(greeting string) string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				c.Write([]byte(greeting))
				c.Close()
			}
		}()
		return ln.Addr().String()
	}
	if !nbdServerUp(serve("NBDMAGICIHAVEOPT")) {
		t.Error("a server sending the NBD greeting counts as down")
	}
	// A port forwarder accepts connections for a stopped container and closes them.
	if nbdServerUp(serve("")) {
		t.Error("a port that accepts and closes counts as an NBD server")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	if nbdServerUp(addr) {
		t.Error("a closed port counts as an NBD server")
	}
}

// nbdWatchHarness drives watchNBD step by step.
type nbdWatchHarness struct {
	errs      chan error
	connected chan struct{}
	done      chan struct{}
	tick      chan time.Time
	up        bool
	stored    chan string
	killed    chan struct{}
	exited    chan struct{}
}

func startNBDWatch(t *testing.T) *nbdWatchHarness {
	t.Helper()
	h := &nbdWatchHarness{errs: make(chan error), connected: make(chan struct{}), done: make(chan struct{}), tick: make(chan time.Time),
		stored: make(chan string, 8), killed: make(chan struct{}, 1), exited: make(chan struct{})}
	t.Cleanup(func() { close(h.done) })
	go func() {
		defer close(h.exited)
		watchNBD(h.errs, h.connected, h.done, h.tick, func() bool { return h.up }, func(msg string) { h.stored <- msg }, func() { h.killed <- struct{}{} })
	}()
	return h
}

// ticks sends n probe ticks; the nil error after each is ignored by watchNBD and only proves the tick was handled before up changes.
func (h *nbdWatchHarness) ticks(n int) {
	for range n {
		for _, send := range []func() bool{
			func() bool {
				select {
				case h.tick <- time.Time{}:
					return true
				case <-h.exited:
					return false
				}
			},
			func() bool {
				select {
				case h.errs <- nil:
					return true
				case <-h.exited:
					return false
				}
			},
		} {
			if !send() {
				return
			}
		}
	}
}

func (h *nbdWatchHarness) wasKilled() bool {
	select {
	case <-h.killed:
		return true
	case <-time.After(50 * time.Millisecond):
		return false
	}
}

func TestWatchNBDRestartsWhenServerIsBack(t *testing.T) {
	h := startNBDWatch(t)
	h.errs <- errors.New("Failed to negotiate with server")
	if msg := <-h.stored; msg != "Failed to negotiate with server" {
		t.Errorf("stored %q", msg)
	}
	h.ticks(nbdRestartProbes * 3)
	if h.wasKilled() {
		t.Fatal("the VM was restarted while the storage server was still down")
	}
	h.up = true
	h.ticks(nbdRestartProbes - 1)
	if h.wasKilled() {
		t.Fatal("the VM was restarted before the disk had time to reconnect by itself")
	}
	h.ticks(1)
	if !h.wasKilled() {
		t.Fatal("the VM was not restarted although the storage server is back and the disk still failed")
	}
}

func TestWatchNBDLeavesRecoveredDiskAlone(t *testing.T) {
	h := startNBDWatch(t)
	h.errs <- errors.New("boom")
	<-h.stored
	h.up = true
	h.ticks(2)
	h.connected <- struct{}{}
	if msg := <-h.stored; msg != "" {
		t.Errorf("after reconnecting the error is %q, want it cleared", msg)
	}
	h.ticks(nbdRestartProbes * 2)
	if h.wasKilled() {
		t.Error("a disk that reconnected by itself got its VM restarted")
	}
}

func TestWatchNBDServerFlapping(t *testing.T) {
	h := startNBDWatch(t)
	h.errs <- errors.New("boom")
	<-h.stored
	for range 4 {
		h.up = true
		h.ticks(nbdRestartProbes - 1)
		h.up = false
		h.ticks(1)
	}
	if h.wasKilled() {
		t.Error("the VM was restarted although the server never stayed up long enough")
	}
}
