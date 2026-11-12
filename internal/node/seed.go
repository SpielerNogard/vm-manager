package node

import (
	"errors"
	"os"
	"path/filepath"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/filesystem/iso9660"
)

const SeedISO = "seed.iso"

// GuestSeedFiles is the cloud-init NoCloud seed of a guest VM.
func GuestSeedFiles(f VMFile) map[string][]byte {
	userData, metaData, networkConfig := CloudInit(f)
	return map[string][]byte{
		"user-data": []byte(userData), "meta-data": []byte(metaData),
		"network-config": []byte(networkConfig), "vmm-disks.sh": []byte(DiskScript(f)),
	}
}

// WriteSeedISO builds beside dir/seed.iso and renames, so a failed build keeps the old seed bootable.
func WriteSeedISO(dir string, files map[string][]byte) (err error) {
	tmp := filepath.Join(dir, "seed-new.iso")
	os.Remove(tmp)
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()
	work, err := os.MkdirTemp("", "vmm-seed")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	size := int64(1 << 20)
	for _, b := range files {
		size += int64(len(b)) + 2048
	}
	size = (size + 2047) &^ 2047
	d, err := diskfs.Create(tmp, size, diskfs.SectorSize(2048))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	fs, err := d.CreateFilesystem(disk.FilesystemSpec{FSType: filesystem.TypeISO9660, VolumeLabel: "cidata", WorkDir: work})
	if err != nil {
		return err
	}
	for name, b := range files {
		f, err := fs.OpenFile("/"+name, os.O_CREATE|os.O_RDWR)
		if err != nil {
			return err
		}
		if _, err := f.Write(b); err != nil {
			return err
		}
	}
	iso, ok := fs.(*iso9660.FileSystem)
	if !ok {
		return errors.New("seed: unexpected filesystem type")
	}
	if err := iso.Finalize(iso9660.FinalizeOptions{RockRidge: true, VolumeIdentifier: "cidata"}); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, SeedISO))
}
