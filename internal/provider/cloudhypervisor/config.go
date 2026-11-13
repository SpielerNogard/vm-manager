package cloudhypervisor

import (
	"fmt"
	"os"
	"strconv"

	"vm-manager/internal/node"
	"vm-manager/internal/provider/linuxvm"
)

const DefaultFirmware = "/usr/local/share/cloud-hypervisor/hypervisor-fw"

type settings struct {
	ImageURL string `json:"imageURL"`
	Firmware string `json:"firmware"`
}

func parseSettings(c node.Config) (settings, error) {
	var s settings
	if err := c.ProviderSection(&s); err != nil {
		return s, fmt.Errorf("config cloud-hypervisor section: %w", err)
	}
	if s.ImageURL == "" {
		s.ImageURL = linuxvm.DefaultImageURL
	}
	if s.Firmware == "" {
		s.Firmware = DefaultFirmware
	}
	return s, nil
}

func checkFirmware(path string) error {
	fi, err := os.Stat(path)
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("not a regular file")
	}
	if err != nil {
		return fmt.Errorf("firmware %s missing (see docs/install.md): %w", path, err)
	}
	return nil
}

// args is the cloud-hypervisor command line; drives holds the host path of each node.AttachOrder disk.
func args(f node.VMFile, firmware, sock string, drives []string) ([]string, error) {
	order := node.AttachOrder(f)
	if len(drives) != len(order) {
		return nil, fmt.Errorf("got %d drive paths for %d disks", len(drives), len(order))
	}
	a := []string{"--api-socket", "path=" + sock, "--firmware", firmware,
		"--cpus", "boot=" + strconv.Itoa(f.Spec.CPUs), "--memory", "size=" + strconv.Itoa(f.Spec.MemoryMB) + "M", "--disk"}
	for i, d := range order {
		disk := "path=" + drives[i]
		if d.ReadOnly {
			disk += ",readonly=on"
		}
		a = append(a, disk)
	}
	a = append(a, "--net", "tap="+linuxvm.TapName(f.Spec.ID, 0)+",mac="+f.MAC)
	for i, n := range f.Spec.NICs {
		a = append(a, "tap="+linuxvm.TapName(f.Spec.ID, i+1)+",mac="+n.MAC)
	}
	return append(a, "--serial", "tty", "--console", "off"), nil
}
