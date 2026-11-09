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

func DataDiskName(i int) string { return fmt.Sprintf("data-%d.img", i) }

func CreateDataDisks(dir string, sizesGB []int) error {
	for i, gb := range sizesGB {
		f, err := os.Create(filepath.Join(dir, DataDiskName(i)))
		if err != nil {
			return err
		}
		err = f.Truncate(int64(gb) << 30)
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func CloudInit(f VMFile) (userData, metaData, networkConfig string) {
	key, _ := json.Marshal(f.Spec.SSHPublicKey) // JSON string is valid YAML and escapes anything odd in the key
	userData = fmt.Sprintf(`#cloud-config
hostname: %s
users:
  - name: ubuntu
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - %s
`, f.Spec.Name, key)
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
	return userData, metaData, networkConfig
}
