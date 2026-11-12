// Package firecracker implements node.Provider with Firecracker microVMs on Linux.
package firecracker

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/klauspost/compress/zstd"
)

// extractBoot copies the guest's current kernel (as ELF) and initrd out of its raw root disk, so apt kernel upgrades take effect.
func extractBoot(rootImg, outDir string) (kernel, initrd string, err error) {
	d, err := diskfs.Open(rootImg, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		return "", "", err
	}
	defer d.Close()
	fs, prefix, err := bootFilesystem(d)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", rootImg, err)
	}

	kernel, initrd = filepath.Join(outDir, "vmlinux"), filepath.Join(outDir, "initrd.img")
	kTmp, iTmp := kernel+".tmp", initrd+".tmp"
	defer os.Remove(kTmp)
	defer os.Remove(iTmp)

	vmlinuz, err := fs.OpenFile(prefix+"vmlinuz", os.O_RDONLY)
	if err != nil {
		return "", "", fmt.Errorf("guest has no %svmlinuz: %w", prefix, err)
	}
	elf, err := bzImageToELF(vmlinuz)
	if err != nil {
		return "", "", fmt.Errorf("guest %svmlinuz: %w", prefix, err)
	}
	if err := os.WriteFile(kTmp, elf, 0o644); err != nil {
		return "", "", err
	}
	in, err := fs.OpenFile(prefix+"initrd.img", os.O_RDONLY)
	if err != nil {
		return "", "", fmt.Errorf("guest has no %sinitrd.img: %w", prefix, err)
	}
	if err := writeFrom(iTmp, in); err != nil {
		return "", "", err
	}
	if err := os.Rename(kTmp, kernel); err != nil {
		return "", "", err
	}
	return kernel, initrd, os.Rename(iTmp, initrd)
}

// bootFilesystem finds /boot: the ext4 labelled BOOT (noble), else /boot on the root filesystem.
func bootFilesystem(d interface {
	GetFilesystem(int) (filesystem.FileSystem, error)
}) (filesystem.FileSystem, string, error) {
	var root filesystem.FileSystem
	for i := 1; i <= 128; i++ {
		fs, err := d.GetFilesystem(i)
		if err != nil {
			continue
		}
		switch strings.TrimRight(fs.Label(), "\x00 ") {
		case "BOOT":
			return fs, "/", nil
		case "cloudimg-rootfs":
			root = fs
		}
	}
	if root == nil {
		return nil, "", errors.New("no filesystem labelled BOOT or cloudimg-rootfs")
	}
	return root, "/boot/", nil
}

func writeFrom(path string, r io.Reader) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// bzImageToELF decompresses the vmlinux inside an x86 bzImage, located via the setup header's payload_offset/length.
func bzImageToELF(r io.Reader) ([]byte, error) {
	img, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(img) < 0x250 || string(img[0x202:0x206]) != "HdrS" {
		return nil, errors.New("not a bzImage")
	}
	setupSects := int(img[0x1f1])
	if setupSects == 0 {
		setupSects = 4
	}
	start := (setupSects+1)*512 + int(binary.LittleEndian.Uint32(img[0x248:]))
	end := start + int(binary.LittleEndian.Uint32(img[0x24c:]))
	if start > len(img) || end > len(img) || end < start {
		return nil, errors.New("bzImage payload out of range")
	}
	payload := img[start:end]

	var out []byte
	switch {
	case bytes.HasPrefix(payload, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		dec, err := zstd.NewReader(nil)
		if err != nil {
			return nil, err
		}
		defer dec.Close()
		// The kernel build appends the uncompressed size, which is not a zstd frame.
		if out, err = dec.DecodeAll(payload, nil); err != nil && len(payload) > 4 {
			out, err = dec.DecodeAll(payload[:len(payload)-4], nil)
		}
		if err != nil {
			return nil, fmt.Errorf("zstd: %w", err)
		}
	case bytes.HasPrefix(payload, []byte{0x1f, 0x8b}):
		gz, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		gz.Multistream(false)
		if out, err = io.ReadAll(gz); err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
	default:
		return nil, errors.New("no compressed kernel (payload is neither zstd nor gzip)")
	}
	if !bytes.HasPrefix(out, []byte("\x7fELF")) {
		return nil, errors.New("decompressed kernel is not ELF")
	}
	return out, nil
}
