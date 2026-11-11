//go:build linux

// netagent runs in a helper VM: a vsock connection carries one guest NIC as a br-int tap, or one Configure.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"

	"golang.org/x/sys/unix"

	"vm-manager/internal/netframe"
)

const vsockPort = 6000

func main() {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		log.Fatal(err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: vsockPort}); err != nil {
		log.Fatal(err)
	}
	if err := unix.Listen(fd, 16); err != nil {
		log.Fatal(err)
	}
	log.Printf("listening on vsock port %d", vsockPort)
	for {
		nfd, _, err := unix.Accept(fd)
		if err != nil {
			log.Print(err)
			continue
		}
		go serve(os.NewFile(uintptr(nfd), "vsock"))
	}
}

func serve(conn *os.File) {
	defer conn.Close()
	r := bufio.NewReaderSize(conn, 1<<20)
	var wmu sync.Mutex
	var tap *os.File
	var name string
	defer func() {
		if tap != nil {
			tap.Close()
			if err := run("ovs-vsctl", "--if-exists", "del-port", "br-int", name); err != nil {
				log.Print(err)
			}
			log.Printf("removed %s", name)
		}
	}()
	buf := make([]byte, netframe.MaxPayload)
	for {
		typ, id, payload, err := netframe.Read(r, buf)
		if err != nil {
			return
		}
		switch typ {
		case netframe.AddPort:
			if tap != nil {
				return
			}
			name = fmt.Sprintf("vmm%d", id)
			if tap, err = openTap(name); err != nil {
				log.Printf("tap %s: %v", name, err)
				return
			}
			if err := run("ip", "link", "set", name, "mtu", "1500", "up"); err != nil {
				log.Print(err)
				return
			}
			if err := run("ovs-vsctl", "--may-exist", "add-port", "br-int", name, "--", "set", "interface", name, "external_ids:iface-id="+string(payload)); err != nil {
				log.Print(err)
				return
			}
			log.Printf("added %s iface-id=%s", name, payload)
			go pumpTap(tap, conn, &wmu, id)
		case netframe.Ethernet:
			if tap != nil {
				if _, err := tap.Write(payload); err != nil {
					log.Printf("%s: %v", name, err)
				}
			}
		case netframe.RemovePort:
			return
		case netframe.ConfigureFrame:
			var c netframe.Configure
			if err := json.Unmarshal(payload, &c); err != nil {
				log.Printf("configure: %v", err)
				return
			}
			changed, err := applyConfigure(c, "/", run)
			log.Printf("configure %s: changed=%v err=%v", c.Name, changed, err)
			return
		}
	}
}

func pumpTap(tap, conn *os.File, wmu *sync.Mutex, id uint16) {
	b := make([]byte, netframe.MaxPayload)
	for {
		n, err := tap.Read(b)
		if err != nil {
			return
		}
		wmu.Lock()
		err = netframe.Write(conn, netframe.Ethernet, id, b[:n])
		wmu.Unlock()
		if err != nil {
			return
		}
	}
}

func openTap(name string) (*os.File, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	ifr.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func run(name string, args ...string) error {
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s %v: %w: %s", name, args, err, out)
	}
	return nil
}
