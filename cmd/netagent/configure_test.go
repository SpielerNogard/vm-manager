package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vm-manager/internal/netframe"
)

func TestApplyConfigureWritesAndRestartsOnce(t *testing.T) {
	root := t.TempDir()
	c := netframe.Configure{
		Name: "mac-1", PrivateKey: "priv", Address: "10.198.0.11/24", HubKey: "hubpub",
		Endpoint: "hub.lan:51820", SBRemote: "tcp:10.198.0.1:6642", MTU: 1420,
	}
	var calls []string
	run := func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}

	changed, err := applyConfigure(c, root, run)
	if err != nil || !changed {
		t.Fatalf("first call: changed=%v err=%v", changed, err)
	}
	conf, err := os.ReadFile(filepath.Join(root, "etc/wireguard/wg-underlay.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Endpoint = hub.lan:51820", "MTU = 1420", "Address = 10.198.0.11/24", "AllowedIPs = 10.198.0.0/24", "PersistentKeepalive = 25"} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("conf missing %q:\n%s", want, conf)
		}
	}
	id, _ := os.ReadFile(filepath.Join(root, "etc/openvswitch/system-id.conf"))
	if string(id) != "mac-1" {
		t.Errorf("system-id.conf = %q", id)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "restart") || !strings.Contains(joined, "external_ids:ovn-encap-ip=10.198.0.11") {
		t.Errorf("calls:\n%s", joined)
	}

	calls = nil
	changed, err = applyConfigure(c, root, run)
	if err != nil || changed {
		t.Fatalf("second call: changed=%v err=%v", changed, err)
	}
	if strings.Contains(strings.Join(calls, "\n"), "restart") {
		t.Errorf("unexpected restart: %v", calls)
	}
}

func TestApplyConfigureRetriesRestartAfterFailure(t *testing.T) {
	root := t.TempDir()
	c := netframe.Configure{
		Name: "mac-1", PrivateKey: "priv", Address: "10.198.0.11/24", HubKey: "hubpub",
		Endpoint: "hub.lan:51820", SBRemote: "tcp:10.198.0.1:6642", MTU: 1420,
	}
	failing := func(name string, args ...string) error {
		if name == "systemctl" {
			return errors.New("boom")
		}
		return nil
	}
	if _, err := applyConfigure(c, root, failing); err == nil {
		t.Fatal("expected error")
	}

	var calls []string
	ok := func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	if _, err := applyConfigure(c, root, ok); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "restart") {
		t.Errorf("retry did not restart: %v", calls)
	}

	calls = nil
	if _, err := applyConfigure(c, root, ok); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(calls, "\n"), "restart") {
		t.Errorf("restart after success: %v", calls)
	}
}

func TestHandleConfigureRepliesResult(t *testing.T) {
	payload := []byte(`{"Name":"mac-1","Address":"10.198.0.11/24","SBRemote":"tcp:10.198.0.1:6642"}`)
	for _, tc := range []struct {
		name     string
		payload  []byte
		applyErr error
		want     string
	}{
		{"ok", payload, nil, ""},
		{"apply error", payload, errors.New("ovs-vsctl: not found"), "ovs-vsctl: not found"},
		{"bad json", []byte("{"), nil, "unexpected end of JSON input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got netframe.Configure
			apply := func(c netframe.Configure) error { got = c; return tc.applyErr }
			var conn bytes.Buffer
			handleConfigure(&conn, tc.payload, apply)
			typ, _, reply, err := netframe.Read(&conn, nil)
			if err != nil {
				t.Fatal(err)
			}
			if typ != netframe.ConfigureResult || !strings.Contains(string(reply), tc.want) || (tc.want == "") != (len(reply) == 0) {
				t.Fatalf("reply type %d %q, want ConfigureResult containing %q", typ, reply, tc.want)
			}
			if conn.Len() != 0 {
				t.Fatalf("%d trailing bytes, want exactly one frame", conn.Len())
			}
			if tc.want == "" && got.Name != "mac-1" {
				t.Fatalf("apply got %+v", got)
			}
		})
	}
}
