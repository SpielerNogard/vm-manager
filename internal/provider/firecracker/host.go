//go:build linux

package firecracker

import (
	"fmt"
	"net"
	"os"
	"os/exec"

	"vm-manager/internal/chassisconf"
	"vm-manager/internal/node"
)

const (
	natBridge = "vmm0"
	intBridge = "br-int"
)

// checkHost fails early with what is missing; install.sh (or the manual setup docs) provides it.
func checkHost() error {
	if _, err := exec.LookPath("firecracker"); err != nil {
		return fmt.Errorf("firecracker binary not in PATH: %w", err)
	}
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err != nil {
		return fmt.Errorf("KVM not usable: %w", err)
	} else {
		f.Close()
	}
	if _, err := net.InterfaceByName(natBridge); err != nil {
		return fmt.Errorf("NAT bridge %s missing (see docs/install.md): %w", natBridge, err)
	}
	if err := chassisconf.Run("ovs-vsctl", "--version"); err != nil {
		return fmt.Errorf("Open vSwitch missing: %w", err)
	}
	return nil
}

// setupTaps recreates the VM's taps: eth0 on the NAT bridge, OVN NICs on br-int.
func setupTaps(f node.VMFile) error {
	removeTaps(f)
	for i := 0; i <= len(f.Spec.NICs); i++ {
		tap := tapName(f.Spec.ID, i)
		if err := chassisconf.Run("ip", "tuntap", "add", "dev", tap, "mode", "tap"); err != nil {
			return err
		}
		if err := chassisconf.Run("ip", "link", "set", tap, "mtu", "1500", "up"); err != nil {
			return err
		}
		var err error
		if i == 0 {
			err = chassisconf.Run("ip", "link", "set", tap, "master", natBridge)
		} else {
			// ovn-controller adopts an existing br-int; creating it here keeps a first boot from racing it.
			err = chassisconf.Run("ovs-vsctl", "--may-exist", "add-br", intBridge, "--", "--may-exist", "add-port", intBridge, tap,
				"--", "set", "interface", tap, "external_ids:iface-id="+f.Spec.NICs[i-1].Port)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func removeTaps(f node.VMFile) {
	for i := 0; i <= len(f.Spec.NICs); i++ {
		tap := tapName(f.Spec.ID, i)
		if i > 0 {
			chassisconf.Run("ovs-vsctl", "--if-exists", "del-port", intBridge, tap)
		}
		if _, err := net.InterfaceByName(tap); err == nil {
			chassisconf.Run("ip", "link", "del", tap)
		}
	}
}
