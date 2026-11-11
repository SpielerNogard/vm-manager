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

	// A failure below must not make the retry see unchanged files and skip the restart.
	pending := filepath.Join(root, "run/netagent-restart-pending")
	if changed {
		if _, err := writeIfChanged(pending, nil, 0o644); err != nil {
			return changed, err
		}
	}
	_, statErr := os.Stat(pending)
	restart := statErr == nil

	if err := run("ovs-vsctl", "set", "open", ".",
		"external_ids:system-id="+c.Name,
		"external_ids:ovn-remote="+c.SBRemote,
		"external_ids:ovn-encap-type=geneve",
		"external_ids:ovn-encap-ip="+encapIP); err != nil {
		return changed, err
	}
	if !restart {
		return false, nil
	}
	if err := run("systemctl", "enable", "--now", wgService); err != nil {
		return changed, err
	}
	if err := run("systemctl", "restart", wgService, "ovn-controller"); err != nil {
		return changed, err
	}
	return true, os.Remove(pending)
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
