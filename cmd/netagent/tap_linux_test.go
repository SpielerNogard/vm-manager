package main

import (
	"os"
	"testing"
	"time"
)

func TestTapCloseUnblocksReadAndReopen(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Skip("no /dev/net/tun")
	}
	tap, err := openTap("vmmtest")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		tap.Read(make([]byte, 2048))
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	tap.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Read still blocked after Close")
	}
	again, err := openTap("vmmtest")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	again.Close()
}
