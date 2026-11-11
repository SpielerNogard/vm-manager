package proto

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	for _, in := range []Message{
		{Type: "register", Register: &Register{
			NodeID: "n1", Hostname: "mac", Provider: "vz", CPUs: 8, MemoryMB: 16384,
			VMs: []VMStatus{{ID: "abc", State: StateRunning, IP: "192.168.64.2", Generation: 3}},
		}},
		{Type: "desired", Desired: []DesiredVM{{
			Spec:  VMSpec{ID: "abc", Name: "dev", CPUs: 2, MemoryMB: 2048, DiskGB: 20, DataDisks: []DataDisk{{Name: "data", MountPath: "/data", SizeGB: 5}}, SSHPublicKeys: []string{"ssh-ed25519 AAA"}, NICs: []NIC{{Network: "mgmt", MAC: "02:0a:c8:00:00:0a", IP: "10.200.0.10", Port: "abc-mgmt"}}},
			Power: PowerStopped, StopMode: StopHard, Generation: 3, Provisioned: true,
		}}},
	} {
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
}

func TestNICJSONAndNoSSHPort(t *testing.T) {
	b, err := json.Marshal(VMSpec{ID: "a", NICs: []NIC{{Network: "mgmt", MAC: "02:0a:c8:00:00:0a", IP: "10.200.0.10", Port: "a-mgmt"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"nics"`)) || bytes.Contains(b, []byte("sshPort")) {
		t.Fatalf("unexpected JSON: %s", b)
	}
}
