//go:build linux

package linuxvm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vm-manager/internal/chassisconf"
)

// nbdMu keeps two VMs starting at once from picking the same free device.
var nbdMu sync.Mutex

func NBDConnected(dev string) bool {
	b, err := os.ReadFile(filepath.Join("/sys/block", filepath.Base(dev), "size"))
	return err == nil && strings.TrimSpace(string(b)) != "0"
}

func NBDRecord(vmDir, diskID string) string { return filepath.Join(vmDir, "nbd-"+diskID) }

// AttachNBD connects the export to a free /dev/nbdN, reusing the device recorded for this disk if it is still connected (after a node crash).
func AttachNBD(vmDir, diskID, rawURL string) (string, error) {
	host, port, export, err := parseNBDURL(rawURL)
	if err != nil {
		return "", err
	}
	nbdMu.Lock()
	defer nbdMu.Unlock()
	if b, err := os.ReadFile(NBDRecord(vmDir, diskID)); err == nil {
		if dev := strings.TrimSpace(string(b)); NBDConnected(dev) {
			return dev, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	devs, err := filepath.Glob("/sys/block/nbd*")
	if err != nil {
		return "", err
	}
	for _, sys := range devs {
		dev := "/dev/" + filepath.Base(sys)
		if NBDConnected(dev) {
			continue
		}
		// Not nbd-client: its -persist does not reconnect, and writes after a storage restart were lost without an error.
		if err := chassisconf.Run("qemu-nbd", qemuNBDArgs(dev, host, port, export)...); err != nil {
			return "", fmt.Errorf("disk %s: %w", diskID, err)
		}
		// qemu-nbd returns once its daemon runs, which is before the kernel has the device.
		for i := 0; !NBDConnected(dev); i++ {
			if i == 50 {
				chassisconf.Run("qemu-nbd", "--disconnect", dev)
				return "", fmt.Errorf("disk %s: %s not connected 5s after qemu-nbd started", diskID, dev)
			}
			time.Sleep(100 * time.Millisecond)
		}
		return dev, os.WriteFile(NBDRecord(vmDir, diskID), []byte(dev+"\n"), 0o644)
	}
	return "", errors.New("no free /dev/nbd device (is the nbd module loaded?)")
}

func DetachNBD(vmDir, diskID string) error {
	b, err := os.ReadFile(NBDRecord(vmDir, diskID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if dev := strings.TrimSpace(string(b)); NBDConnected(dev) {
		if err := chassisconf.Run("qemu-nbd", "--disconnect", dev); err != nil {
			return err
		}
		// The kernel tears the device down asynchronously; a restart must not find it half-connected.
		for i := 0; NBDConnected(dev); i++ {
			if i == 50 {
				return fmt.Errorf("%s still connected 5s after disconnect", dev)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return os.Remove(NBDRecord(vmDir, diskID))
}

// NBDWatch mirrors lost NBD connections into Err.
type NBDWatch struct {
	errs sync.Map // vmID/diskID -> message of a lost NBD connection
}

// Watch records a lost NBD connection until done closes.
func (w *NBDWatch) Watch(vmID, dir, diskID string, done <-chan struct{}) {
	key := vmID + "/" + diskID
	defer w.errs.Delete(key)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		b, _ := os.ReadFile(NBDRecord(dir, diskID))
		if NBDConnected(strings.TrimSpace(string(b))) {
			w.errs.Delete(key)
		} else {
			w.errs.Store(key, "NBD connection lost")
		}
		select {
		case <-done:
			return
		case <-t.C:
		}
	}
}

func (w *NBDWatch) Err(vmID, diskID string) string {
	e, _ := w.errs.Load(vmID + "/" + diskID)
	msg, _ := e.(string)
	return msg
}
