package proto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	in := Message{
		Type: "register",
		Register: &Register{
			NodeID: "n1", Hostname: "mac", Provider: "vz", CPUs: 8, MemoryMB: 16384,
			VMs: []VMRecord{{
				Spec:   VMSpec{ID: "abc", Name: "dev", CPUs: 2, MemoryMB: 2048, DiskGB: 20, DataDisksGB: []int{5}, SSHPublicKey: "ssh-ed25519 AAA", SSHPort: 22001},
				Status: VMStatus{ID: "abc", State: StateRunning, IP: "192.168.64.2"},
			}},
		},
	}
	var buf bytes.Buffer
	if err := NewEncoder(&buf).Encode(in); err != nil {
		t.Fatal(err)
	}
	var out Message
	if err := json.NewDecoder(&buf).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
}

func TestStreamHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteHeader(&buf, StreamHeader{VMID: "abc"}); err != nil {
		t.Fatal(err)
	}
	buf.WriteString("SSH-2.0")
	r := bufio.NewReader(&buf)
	h, err := ReadHeader(r)
	if err != nil {
		t.Fatal(err)
	}
	if h.VMID != "abc" {
		t.Fatalf("VMID = %q", h.VMID)
	}
	rest, _ := io.ReadAll(r)
	if string(rest) != "SSH-2.0" {
		t.Fatalf("rest = %q", rest)
	}
}
