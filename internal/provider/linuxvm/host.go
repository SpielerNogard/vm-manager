//go:build linux

package linuxvm

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"

	"vm-manager/internal/chassisconf"
	"vm-manager/internal/node"
)

const (
	NATBridge = "vmm0"
	IntBridge = "br-int"
)

// CheckHost fails early with what is missing; install.sh (or the manual setup docs) provides it.
func CheckHost(binaries ...string) error {
	for _, binary := range binaries {
		if _, err := exec.LookPath(binary); err != nil {
			return fmt.Errorf("%s binary not in PATH: %w", binary, err)
		}
	}
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err != nil {
		return fmt.Errorf("KVM not usable: %w", err)
	} else {
		f.Close()
	}
	if _, err := net.InterfaceByName(NATBridge); err != nil {
		return fmt.Errorf("NAT bridge %s missing (see docs/install.md): %w", NATBridge, err)
	}
	if err := chassisconf.Run("ovs-vsctl", "--version"); err != nil {
		return fmt.Errorf("Open vSwitch missing: %w", err)
	}
	return nil
}

// SetupTaps recreates the VM's taps: eth0 on the NAT bridge, OVN NICs on br-int. An owner > 0 may open them without root.
func SetupTaps(f node.VMFile, owner int) error {
	RemoveTaps(f)
	add := []string{"tuntap", "add", "mode", "tap"}
	if owner > 0 {
		add = append(add, "user", strconv.Itoa(owner))
	}
	for i := 0; i <= len(f.Spec.NICs); i++ {
		tap := TapName(f.Spec.ID, i)
		if err := chassisconf.Run("ip", append(add, "dev", tap)...); err != nil {
			return err
		}
		if err := chassisconf.Run("ip", "link", "set", tap, "mtu", "1500", "up"); err != nil {
			return err
		}
		var err error
		if i == 0 {
			err = chassisconf.Run("ip", "link", "set", tap, "master", NATBridge)
		} else {
			// ovn-controller adopts an existing br-int; creating it here keeps a first boot from racing it.
			err = chassisconf.Run("ovs-vsctl", "--may-exist", "add-br", IntBridge, "--", "--may-exist", "add-port", IntBridge, tap,
				"--", "set", "interface", tap, "external_ids:iface-id="+f.Spec.NICs[i-1].Port)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func RemoveTaps(f node.VMFile) {
	for i := 0; i <= len(f.Spec.NICs); i++ {
		tap := TapName(f.Spec.ID, i)
		if i > 0 {
			chassisconf.Run("ovs-vsctl", "--if-exists", "del-port", IntBridge, tap)
		}
		if _, err := net.InterfaceByName(tap); err == nil {
			chassisconf.Run("ip", "link", "del", tap)
		}
	}
}
