package netframe

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	for _, typ := range []byte{AddPort, RemovePort, Ethernet, ConfigureFrame, ConfigureResult} {
		for _, size := range []int{0, 1, 1514, 65535} {
			payload := bytes.Repeat([]byte{0xab}, size)
			var b bytes.Buffer
			if err := Write(&b, typ, 513, payload); err != nil {
				t.Fatal(err)
			}
			gt, gp, gpl, err := Read(&b, nil)
			if err != nil {
				t.Fatal(err)
			}
			if gt != typ || gp != 513 || !bytes.Equal(gpl, payload) {
				t.Fatalf("type %d size %d: got %d %d len %d", typ, size, gt, gp, len(gpl))
			}
		}
	}
}

func TestFrameRejectsOversize(t *testing.T) {
	hdr := make([]byte, 7)
	binary.BigEndian.PutUint32(hdr, 70000)
	if _, _, _, err := Read(bytes.NewReader(hdr), nil); err == nil {
		t.Fatal("expected error for oversize frame")
	}
	if err := Write(&bytes.Buffer{}, Ethernet, 1, make([]byte, 65536)); err == nil {
		t.Fatal("expected error writing oversize payload")
	}
}
