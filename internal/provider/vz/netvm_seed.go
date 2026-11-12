package vz

import (
	"fmt"
	"os"
)

const netvmUserData = `#cloud-config
hostname: netvm
package_update: true
packages: [openvswitch-switch, ovn-host, wireguard-tools]
bootcmd:
  # Kernel and systemd output to the virtio console (console.log); takes effect from the second boot.
  - [sh, -c, 'test -f /etc/default/grub.d/99-vmm-console.cfg || { echo "GRUB_CMDLINE_LINUX_DEFAULT=\"\$GRUB_CMDLINE_LINUX_DEFAULT console=tty1 console=hvc0\"" > /etc/default/grub.d/99-vmm-console.cfg && update-grub; }']
  # The seed carries the node's current netagent; installing it on every boot keeps the netvm in step with the node.
  - [sh, -c, 'mkdir -p /run/cidata && mount -o ro LABEL=cidata /run/cidata && install -m 0755 /run/cidata/netagent /usr/local/bin/netagent; umount /run/cidata']
write_files:
  - path: /etc/systemd/system/netagent.service
    content: |
      [Unit]
      Description=vm-manager netagent
      Wants=openvswitch-switch.service
      After=openvswitch-switch.service cloud-init.service
      [Service]
      ExecStart=/usr/local/bin/netagent
      Restart=always
      RestartSec=2
      [Install]
      WantedBy=multi-user.target
runcmd:
  - [systemctl, enable, --now, netagent.service]
`

// netvmSeedFiles is the netvm's cloud-init seed, including the netagent binary.
func netvmSeedFiles(agentPath, mac string) (map[string][]byte, error) {
	agent, err := os.ReadFile(agentPath)
	if err != nil {
		return nil, err
	}
	networkConfig := fmt.Sprintf(`version: 2
ethernets:
  primary:
    match:
      macaddress: %q
    dhcp4: true
    dhcp-identifier: mac
`, mac)
	return map[string][]byte{
		"user-data":      []byte(netvmUserData),
		"meta-data":      []byte("instance-id: vmm-netvm\nlocal-hostname: netvm\n"),
		"network-config": []byte(networkConfig),
		"netagent":       agent,
	}, nil
}
