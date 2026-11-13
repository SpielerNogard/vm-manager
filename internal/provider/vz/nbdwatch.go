//go:build darwin

package vz

import (
	"io"
	"net"
	"time"
)

const (
	nbdProbeInterval = 2 * time.Second
	nbdRestartProbes = 5
)

// A forwarded port of a stopped container accepts and closes, so only the NBD greeting counts.
func nbdServerUp(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	magic := make([]byte, 8)
	_, err = io.ReadFull(c, magic)
	return err == nil && string(magic) == "NBDMAGIC"
}

// watchNBD restarts the VM when its disk stays failed although the server is back: after some errors only a new VM gets a working NBD client.
func watchNBD(errs <-chan error, connected, done <-chan struct{}, tick <-chan time.Time, serverUp func() bool, setErr func(string), restart func()) {
	failed, up := false, 0
	for {
		select {
		case err := <-errs:
			if err != nil {
				failed, up = true, 0
				setErr(err.Error())
			}
		case <-connected:
			failed = false
			setErr("")
		case <-tick:
			if !failed {
				continue
			}
			if !serverUp() {
				up = 0
				continue
			}
			if up++; up >= nbdRestartProbes {
				restart()
				return
			}
		case <-done:
			return
		}
	}
}
