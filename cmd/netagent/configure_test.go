package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"vm-manager/internal/netframe"
)

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
