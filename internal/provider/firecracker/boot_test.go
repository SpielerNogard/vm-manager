package firecracker

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/gpt"
	"github.com/klauspost/compress/zstd"
)

// fakeBzImage builds a minimal bzImage: setup header with payload_offset/length, then the compressed payload.
func fakeBzImage(t *testing.T, payload []byte) []byte {
	t.Helper()
	img := make([]byte, 2*512)
	img[0x1f1] = 1 // setup_sects: protected-mode code starts at (1+1)*512
	copy(img[0x202:], "HdrS")
	pm := make([]byte, 300)
	binary.LittleEndian.PutUint32(img[0x248:], uint32(len(pm)))
	binary.LittleEndian.PutUint32(img[0x24c:], uint32(len(payload)))
	img = append(img, pm...)
	img = append(img, payload...)
	return append(img, make([]byte, 128)...)
}

func elf() []byte { return append([]byte("\x7fELF"), bytes.Repeat([]byte("kernel"), 1000)...) }

func TestBzImageToELFZstd(t *testing.T) {
	enc, _ := zstd.NewWriter(nil)
	payload := enc.EncodeAll(elf(), nil)
	// The kernel build appends the uncompressed size after a zstd payload.
	payload = binary.LittleEndian.AppendUint32(payload, uint32(len(elf())))
	got, err := bzImageToELF(bytes.NewReader(fakeBzImage(t, payload)))
	if err != nil || !bytes.Equal(got, elf()) {
		t.Fatalf("got %d bytes, %v", len(got), err)
	}
}

func TestBzImageToELFGzip(t *testing.T) {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write(elf())
	w.Close()
	got, err := bzImageToELF(bytes.NewReader(fakeBzImage(t, b.Bytes())))
	if err != nil || !bytes.Equal(got, elf()) {
		t.Fatalf("got %d bytes, %v", len(got), err)
	}
}

func TestBzImageToELFRejectsJunk(t *testing.T) {
	_, err := bzImageToELF(bytes.NewReader(fakeBzImage(t, bytes.Repeat([]byte{0x42}, 64))))
	if err == nil || !strings.Contains(err.Error(), "no compressed kernel") {
		t.Fatalf("err = %v", err)
	}
	if _, err := bzImageToELF(bytes.NewReader(make([]byte, 4096))); err == nil {
		t.Fatal("accepted an image without a setup header")
	}
}

// bootDisk builds a GPT disk whose only partition is an empty ext4 filesystem labelled BOOT.
func bootDisk(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "root.img")
	d, err := diskfs.Create(path, 64<<20, diskfs.SectorSizeDefault)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	table := &gpt.Table{Partitions: []*gpt.Partition{{Index: 1, Start: 2048, End: 2048 + (48<<20)/512 - 1, Size: 48 << 20, Type: gpt.LinuxFilesystem, Name: "boot"}}}
	if err := d.Partition(table); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateFilesystem(disk.FilesystemSpec{Partition: 1, FSType: filesystem.TypeExt4, VolumeLabel: "BOOT"}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractBootMissingKernel(t *testing.T) {
	out := t.TempDir()
	_, _, err := extractBoot(bootDisk(t), out)
	if err == nil || !strings.Contains(err.Error(), "vmlinuz") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Fatalf("left files behind: %v", entries)
	}
}

func TestExtractBootRealImage(t *testing.T) {
	img := os.Getenv("VMM_TEST_IMAGE")
	if img == "" {
		t.Skip("VMM_TEST_IMAGE not set")
	}
	kernel, initrd, err := extractBoot(img, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k, err := os.ReadFile(kernel)
	if err != nil || !bytes.HasPrefix(k, []byte("\x7fELF")) {
		t.Fatalf("kernel is not ELF: %v", err)
	}
	if st, err := os.Stat(initrd); err != nil || st.Size() < 1<<20 {
		t.Fatalf("initrd: %v", err)
	}
}

func TestBootFilesFallsBackToLastGood(t *testing.T) {
	dir := t.TempDir()
	disk := bootDisk(t)
	if _, _, err := bootFiles(disk, dir); err == nil {
		t.Fatal("no kernel and no previous pair must fail")
	}
	for _, f := range []string{"vmlinux", "initrd.img"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A half-finished kernel upgrade must not leave the VM unbootable: the last extracted pair still works.
	k, i, err := bootFiles(disk, dir)
	if err != nil || k != filepath.Join(dir, "vmlinux") || i != filepath.Join(dir, "initrd.img") {
		t.Fatalf("got %s %s %v", k, i, err)
	}
}

func TestBzImageToELFRejectsBomb(t *testing.T) {
	enc, _ := zstd.NewWriter(nil)
	big := append([]byte("\x7fELF"), make([]byte, maxKernelSize+1)...)
	if _, err := bzImageToELF(bytes.NewReader(fakeBzImage(t, enc.EncodeAll(big, nil)))); err == nil {
		t.Fatal("accepted a kernel larger than the limit")
	}
}
