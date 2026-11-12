package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"vm-manager/internal/proto"
)

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestWaitReadyIgnoresOldOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConsoleLog)
	appendFile(t, path, "boot 1\n"+ReadyMarker+"\r\n")
	off, err := ConsoleOffset(path)
	if err != nil {
		t.Fatal(err)
	}
	appendFile(t, path, "boot 2\n")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := WaitReady(ctx, path, off, 10*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady = %v, want deadline exceeded", err)
	}
	// The guest may write the marker onto a line that already holds other output.
	appendFile(t, path, "spike login: "+ReadyMarker+"\r\n")
	if err := WaitReady(context.Background(), path, off, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestWaitReadyMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConsoleLog)
	off, err := ConsoleOffset(path)
	if err != nil || off != 0 {
		t.Fatalf("offset = %d, %v", off, err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		appendFile(t, path, ReadyMarker+"\n")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := WaitReady(ctx, path, off, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestMgmtIP(t *testing.T) {
	f := VMFile{Spec: proto.VMSpec{NICs: []proto.NIC{{Network: "net1", IP: "10.210.0.10"}, {Network: "mgmt", IP: "10.200.0.12"}}}}
	if got := MgmtIP(f); got != "10.200.0.12" {
		t.Fatalf("MgmtIP = %q", got)
	}
	if got := MgmtIP(VMFile{}); got != "" {
		t.Fatalf("MgmtIP without NICs = %q", got)
	}
}
