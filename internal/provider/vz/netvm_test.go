//go:build darwin

package vz

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"vm-manager/internal/netframe"
)

func TestConfigureOverRequiresOkResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reply   func(net.Conn)
		wantErr string
	}{
		{"ok", func(c net.Conn) { netframe.Write(c, netframe.ConfigureResult, 0, nil) }, ""},
		{"apply error", func(c net.Conn) { netframe.Write(c, netframe.ConfigureResult, 0, []byte("apt failed")) }, "apt failed"},
		{"close without reply", func(net.Conn) {}, "EOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := net.Pipe()
			defer b.Close()
			go func() {
				typ, _, _, err := netframe.Read(b, nil)
				if err == nil && typ == netframe.ConfigureFrame {
					tc.reply(b)
				}
				b.Close()
			}()
			err := configureOver(context.Background(), a, []byte(`{}`), 5*time.Second)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("got %v, want nil", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestNetvmEndpointRewritesLoopback(t *testing.T) {
	lease := func() (string, error) { return "192.168.64.5", nil }
	for in, want := range map[string]string{
		"localhost:51820": "192.168.64.1:51820",
		"127.0.0.1:6000":  "192.168.64.1:6000",
		"[::1]:51820":     "192.168.64.1:51820",
		"hub.lan:51820":   "hub.lan:51820",
		"10.12.5.1:51820": "10.12.5.1:51820",
	} {
		got, err := netvmEndpoint(in, lease)
		if err != nil || got != want {
			t.Errorf("netvmEndpoint(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	noLease := func() (string, error) { return "", errors.New("no lease") }
	if got, err := netvmEndpoint("hub.lan:51820", noLease); err != nil || got != "hub.lan:51820" {
		t.Errorf("non-loopback must not need a lease: %q, %v", got, err)
	}
	if _, err := netvmEndpoint("localhost:51820", noLease); err == nil {
		t.Error("loopback without lease: want error")
	}
}
