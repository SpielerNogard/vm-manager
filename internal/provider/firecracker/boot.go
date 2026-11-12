// Package firecracker implements node.Provider with Firecracker microVMs on Linux.
package firecracker

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/klauspost/compress/zstd"
)

// Limits for what is read from the guest-controlled disk, far above real Ubuntu kernels (~70 MiB ELF) and initrds.
const (
	maxKernelSize = 256 << 20
	maxInitrdSize = 1 << 30
)

// bootFiles extracts the guest's current kernel, falling back to the last extracted pair so a broken /boot (e.g. an interrupted upgrade) stays bootable.
func bootFiles(rootImg, dir string) (kernel, initrd string, err error) {
	kernel, initrd, err = extractBoot(rootImg, dir)
	if err == nil {
		return kernel, initrd, nil
	}
	kernel, initrd = filepath.Join(dir, "vmlinux"), filepath.Join(dir, "initrd.img")
	if _, kerr := os.Stat(kernel); kerr == nil {
		if _, ierr := os.Stat(initrd); ierr == nil {
			log.Printf("%s: %v; booting the last extracted kernel", rootImg, err)
			return kernel, initrd, nil
		}
	}
	return "", "", err
}

// extractBoot copies the guest's current kernel (as ELF) and initrd out of its raw root disk, so apt kernel upgrades take effect.
func extractBoot(rootImg, outDir string) (kernel, initrd string, err error) {
	// The disk is guest-controlled; a parser panic on corrupt metadata must not take the node and its other VMs down.
	defer func() {
		if r := recover(); r != nil {
			kernel, initrd, err = "", "", fmt.Errorf("reading %s: %v", rootImg, r)
		}
	}()
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
	if err := writeFrom(iTmp, in, maxInitrdSize); err != nil {
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

func writeFrom(path string, r io.Reader, limit int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), limit)
	}
	if err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readLimited reads r fully, failing instead of growing past limit bytes.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("larger than %d bytes", limit)
	}
	return b, err
}

// bzImageToELF decompresses the vmlinux inside an x86 bzImage, located via the setup header's payload_offset/length.
func bzImageToELF(r io.Reader) ([]byte, error) {
	img, err := readLimited(r, maxKernelSize)
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
		dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxKernelSize))
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
		if out, err = readLimited(gz, maxKernelSize); err != nil {
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
