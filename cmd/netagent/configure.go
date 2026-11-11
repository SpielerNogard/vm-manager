package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"vm-manager/internal/netframe"
)

const wgService = "wg-quick@wg-underlay"

func applyConfigure(c netframe.Configure, root string, run func(name string, args ...string) error) (changed bool, err error) {
	if c.Name == "" || c.Address == "" || c.SBRemote == "" {
		return false, fmt.Errorf("configure: name, address and sbRemote are required")
	}
	encapIP, _, _ := strings.Cut(c.Address, "/")

	wg := fmt.Sprintf(`[Interface]
Address = %s
PrivateKey = %s
MTU = %d

[Peer]
PublicKey = %s
Endpoint = %s
AllowedIPs = 10.198.0.0/24
PersistentKeepalive = 25
`, c.Address, c.PrivateKey, c.MTU, c.HubKey, c.Endpoint)

	wgChanged, err := writeIfChanged(filepath.Join(root, "etc/wireguard/wg-underlay.conf"), []byte(wg), 0o600)
	if err != nil {
		return false, err
	}
	// Ubuntu's openvswitch-switch resets system-id from this file on every boot.
	idChanged, err := writeIfChanged(filepath.Join(root, "etc/openvswitch/system-id.conf"), []byte(c.Name), 0o644)
	if err != nil {
		return false, err
	}
	changed = wgChanged || idChanged

	if err := run("ovs-vsctl", "set", "open", ".",
		"external_ids:system-id="+c.Name,
		"external_ids:ovn-remote="+c.SBRemote,
		"external_ids:ovn-encap-type=geneve",
		"external_ids:ovn-encap-ip="+encapIP); err != nil {
		return changed, err
	}
	if !changed {
		return false, nil
	}
	if err := run("systemctl", "enable", "--now", wgService); err != nil {
		return true, err
	}
	return true, run("systemctl", "restart", wgService, "ovn-controller")
}

func writeIfChanged(path string, content []byte, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		return false, err
	}
	return true, os.Chmod(path, mode)
}
