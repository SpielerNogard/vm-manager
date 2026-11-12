//go:build linux

package firecracker

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

func nbdConnected(dev string) bool {
	b, err := os.ReadFile(filepath.Join("/sys/block", filepath.Base(dev), "size"))
	return err == nil && strings.TrimSpace(string(b)) != "0"
}

func nbdRecord(vmDir, diskID string) string { return filepath.Join(vmDir, "nbd-"+diskID) }

// attachNBD connects the export to a free /dev/nbdN, reusing the device recorded for this disk if it is still connected (after a node crash).
func attachNBD(vmDir, diskID, rawURL string) (string, error) {
	host, port, export, err := parseNBDURL(rawURL)
	if err != nil {
		return "", err
	}
	nbdMu.Lock()
	defer nbdMu.Unlock()
	if b, err := os.ReadFile(nbdRecord(vmDir, diskID)); err == nil {
		if dev := strings.TrimSpace(string(b)); nbdConnected(dev) {
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
		if nbdConnected(dev) {
			continue
		}
		// -persist makes nbd-client reconnect when the storage server restarts.
		if err := chassisconf.Run("nbd-client", "-N", export, host, port, dev, "-persist"); err != nil {
			return "", fmt.Errorf("disk %s: %w", diskID, err)
		}
		return dev, os.WriteFile(nbdRecord(vmDir, diskID), []byte(dev+"\n"), 0o644)
	}
	return "", errors.New("no free /dev/nbd device (is the nbd module loaded?)")
}

func detachNBD(vmDir, diskID string) error {
	b, err := os.ReadFile(nbdRecord(vmDir, diskID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if dev := strings.TrimSpace(string(b)); nbdConnected(dev) {
		if err := chassisconf.Run("nbd-client", "-d", dev); err != nil {
			return err
		}
		// The kernel tears the device down asynchronously; a restart must not find it half-connected.
		for i := 0; nbdConnected(dev); i++ {
			if i == 50 {
				return fmt.Errorf("%s still connected 5s after disconnect", dev)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return os.Remove(nbdRecord(vmDir, diskID))
}
