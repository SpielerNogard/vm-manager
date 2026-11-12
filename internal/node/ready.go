package node

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"time"
)

const (
	// ReadyMarker is what the guest writes to its console once its primary NIC has an address.
	ReadyMarker = "vmm-guest-ready"
	ConsoleLog  = "console.log"
)

// ConsoleOffset is the console log's current size; WaitReady ignores everything before it.
func ConsoleOffset(path string) (int64, error) {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// WaitReady polls the console log until ReadyMarker appears after offset.
func WaitReady(ctx context.Context, path string, offset int64, every time.Duration) error {
	marker := []byte(ReadyMarker)
	var tail []byte // keeps a marker split across two reads findable
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		f, err := os.Open(path)
		if err == nil {
			_, err = f.Seek(offset, io.SeekStart)
			var b []byte
			if err == nil {
				b, err = io.ReadAll(f)
			}
			f.Close()
			if err != nil {
				return err
			}
			offset += int64(len(b))
			buf := append(tail, b...)
			if bytes.Contains(buf, marker) {
				return nil
			}
			tail = buf[max(0, len(buf)-len(marker)+1):]
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func MgmtIP(f VMFile) string {
	for _, n := range f.Spec.NICs {
		if n.Network == "mgmt" {
			return n.IP
		}
	}
	return ""
}
