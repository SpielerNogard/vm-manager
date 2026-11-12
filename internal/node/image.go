package node

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/lima-vm/go-qcow2reader"
	"github.com/lima-vm/go-qcow2reader/convert"
)

// EnsureBaseImage downloads url once into imagesDir and converts it (qcow2 or raw) to a raw disk.
func EnsureBaseImage(imagesDir, url string) (string, error) {
	sum := sha256.Sum256([]byte(url))
	raw := filepath.Join(imagesDir, hex.EncodeToString(sum[:8])+".raw")
	if _, err := os.Stat(raw); err == nil {
		return raw, nil
	}
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		return "", err
	}
	download := raw + ".download"
	defer os.Remove(download)
	if err := fetch(url, download); err != nil {
		return "", err
	}
	part := raw + ".part"
	if err := toRaw(download, part); err != nil {
		os.Remove(part)
		return "", err
	}
	return raw, os.Rename(part, raw)
}

func fetch(url, dst string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func toRaw(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	img, err := qcow2reader.Open(in)
	if err != nil {
		return err
	}
	defer img.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := out.Truncate(img.Size()); err != nil {
		out.Close()
		return err
	}
	if err := convert.Convert(out, img, convert.Options{}); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func CloneResize(src, dst string, sizeGB int) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	size := int64(sizeGB) << 30
	if size < st.Size() {
		return fmt.Errorf("disk size %d GB is smaller than base image (%d bytes)", sizeGB, st.Size())
	}
	if err := cloneFile(src, dst); err != nil {
		return err
	}
	return os.Truncate(dst, size)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func CloudInit(f VMFile) (userData, metaData, networkConfig string) {
	var keys strings.Builder
	for _, k := range f.Spec.SSHPublicKeys {
		j, _ := json.Marshal(k) // JSON string is valid YAML and escapes anything odd in the key
		fmt.Fprintf(&keys, "      - %s\n", j)
	}
	userData = fmt.Sprintf(`#cloud-config
hostname: %s
users:
  - name: ubuntu
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
%sbootcmd:
  # Kernel and systemd output to the virtio console (console.log); takes effect from the second boot.
  - [sh, -c, 'test -f /etc/default/grub.d/99-vmm-console.cfg || { echo "GRUB_CMDLINE_LINUX_DEFAULT=\"\$GRUB_CMDLINE_LINUX_DEFAULT console=tty1 console=hvc0\"" > /etc/default/grub.d/99-vmm-console.cfg && update-grub; }']
# Reapplies network-config every boot so a reseeded NIC list takes effect.
updates:
  network:
    when: [boot]
`, f.Spec.Name, keys.String())
	metaData = fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", f.Spec.ID, f.Spec.Name)
	// dhcp-identifier: mac makes the guest's DHCP lease findable by MAC (Ubuntu defaults to a DUID).
	networkConfig = fmt.Sprintf(`version: 2
ethernets:
  primary:
    match:
      macaddress: %q
    dhcp4: true
    dhcp-identifier: mac
`, f.MAC)
	netIdx := 0
	for _, n := range f.Spec.NICs {
		name, dns := "mgmt0", "    nameservers:\n      addresses: [10.201.0.1]\n      search: [\"~internal\"]\n"
		if n.Network != "mgmt" {
			name, dns = fmt.Sprintf("net%d", netIdx), ""
			netIdx++
		}
		// Address, MTU and routes come from OVN DHCP; the guest must not take DNS or hostname from it.
		// IPv6 link-local makes networkd bump the DHCP MTU to 1280, so OVN NICs are IPv4-only.
		networkConfig += fmt.Sprintf(`  %s:
    match:
      macaddress: %q
    dhcp4: true
    link-local: []
    accept-ra: false
    dhcp4-overrides:
      use-dns: false
      use-hostname: false
      use-domains: false
%s`, name, n.MAC, dns)
	}
	return userData, metaData, networkConfig
}
