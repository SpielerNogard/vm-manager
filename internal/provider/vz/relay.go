//go:build darwin

package vz

import (
	"bufio"
	"context"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"vm-manager/internal/netframe"
)

var relayRetry = 2 * time.Second

// newNICSocketpair returns the end handed to vz as a file-handle NIC and the end the relay reads.
func newNICSocketpair() (guestEnd, vmEnd *os.File, err error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		return nil, nil, err
	}
	for _, fd := range fds {
		// Small default buffers drop frames under load (Lima uses the same sizes).
		unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_SNDBUF, 1<<20)
		unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 4<<20)
	}
	// Non-blocking so the runtime poller owns the fd and deadlines can interrupt a pending Read.
	if err := unix.SetNonblock(fds[1], true); err != nil {
		unix.Close(fds[0])
		unix.Close(fds[1])
		return nil, nil, err
	}
	return os.NewFile(uintptr(fds[0]), "nic-guest"), os.NewFile(uintptr(fds[1]), "nic-relay"), nil
}

// relayNIC bridges guest datagrams and netagent Ethernet frames, redialing and re-sending AddPort until ctx is done.
func relayNIC(ctx context.Context, vmEnd *os.File, dial func() (net.Conn, error), port uint16, ifaceID string) {
	for {
		if c, err := dial(); err == nil {
			relayConn(ctx, vmEnd, c, port, ifaceID)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(relayRetry):
		}
	}
}

func relayConn(ctx context.Context, vmEnd *os.File, c net.Conn, port uint16, ifaceID string) {
	defer c.Close()
	if err := netframe.Write(c, netframe.AddPort, port, []byte(ifaceID)); err != nil {
		return
	}
	exited := make(chan struct{}, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		b := make([]byte, netframe.MaxPayload)
		for {
			n, err := vmEnd.Read(b)
			if err != nil || netframe.Write(c, netframe.Ethernet, port, b[:n]) != nil {
				break
			}
		}
		exited <- struct{}{}
	}()
	go func() {
		defer wg.Done()
		r := bufio.NewReaderSize(c, 1<<20)
		buf := make([]byte, netframe.MaxPayload)
		for {
			typ, _, p, err := netframe.Read(r, buf)
			if err != nil {
				break
			}
			if typ == netframe.Ethernet {
				vmEnd.Write(p) // a full guest queue drops the frame, like a real NIC
			}
		}
		exited <- struct{}{}
	}()
	select {
	case <-exited:
	case <-ctx.Done():
	}
	c.Close()
	// Stop the guest-side reader too, so it cannot swallow a datagram meant for the next connection.
	vmEnd.SetDeadline(time.Now())
	wg.Wait()
	vmEnd.SetDeadline(time.Time{})
	if ctx.Err() == nil {
		log.Printf("nic %s: netvm connection lost, reconnecting", ifaceID)
	}
}
