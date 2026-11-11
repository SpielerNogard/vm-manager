// Package netframe is the vsock wire format: uint32 BE length | uint8 type | uint16 BE portID | payload.
package netframe

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	AddPort    = 1
	RemovePort = 2
	Ethernet   = 3
	// ConfigureFrame is named apart from the Configure payload type, which Go would reject as a duplicate.
	ConfigureFrame = 4
	// ConfigureResult answers a ConfigureFrame: empty payload means applied, otherwise the error text.
	ConfigureResult = 5

	MaxPayload = 65535
)

// Configure is the JSON payload of a ConfigureFrame; Address includes the prefix.
type Configure struct {
	Name       string
	PrivateKey string
	Address    string
	HubKey     string
	Endpoint   string
	SBRemote   string
	MTU        int
}

func Write(w io.Writer, typ byte, port uint16, payload []byte) error {
	if len(payload) > MaxPayload {
		return fmt.Errorf("payload of %d bytes exceeds %d", len(payload), MaxPayload)
	}
	buf := make([]byte, 7+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(3+len(payload)))
	buf[4] = typ
	binary.BigEndian.PutUint16(buf[5:], port)
	copy(buf[7:], payload)
	_, err := w.Write(buf)
	return err
}

// Read reuses buf when it is large enough.
func Read(r io.Reader, buf []byte) (typ byte, port uint16, payload []byte, err error) {
	var hdr [7]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return
	}
	n := int(binary.BigEndian.Uint32(hdr[:])) - 3
	if n < 0 || n > MaxPayload {
		return 0, 0, nil, fmt.Errorf("invalid frame length %d", n+3)
	}
	if cap(buf) < n {
		buf = make([]byte, n)
	}
	payload = buf[:n]
	_, err = io.ReadFull(r, payload)
	return hdr[4], binary.BigEndian.Uint16(hdr[5:]), payload, err
}
