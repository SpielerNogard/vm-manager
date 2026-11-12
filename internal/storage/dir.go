package storage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"vm-manager/internal/proto"
)

const (
	partialDir = ".partial"
	chunkSize  = 1 << 20
)

type Dir struct{ Path string }

// Apply creates missing disks (sparse, or copied from CopyFrom), grows smaller ones, removes Deleted; never shrinks or removes anything else.
func (d Dir) Apply(disks []proto.StorageDisk, deleted []string) error {
	if err := os.MkdirAll(filepath.Join(d.Path, partialDir), 0o755); err != nil {
		return err
	}
	var errs []error
	for _, id := range deleted {
		if !plainName(id) {
			errs = append(errs, fmt.Errorf("delete %q: not a plain file name", id))
			continue
		}
		if err := os.Remove(d.file(id)); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("delete %s: %w", id, err))
		}
	}
	for _, disk := range disks {
		if !plainName(disk.ID) || (disk.CopyFrom != "" && !plainName(disk.CopyFrom)) {
			errs = append(errs, fmt.Errorf("disk %q (copy from %q): not a plain file name", disk.ID, disk.CopyFrom))
			continue
		}
		if err := d.ensure(disk); err != nil {
			errs = append(errs, fmt.Errorf("disk %s: %w", disk.ID, err))
		}
	}
	return errors.Join(errs...)
}

// plainName rejects IDs that would resolve outside Path.
func plainName(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\`)
}

func (d Dir) file(id string) string { return filepath.Join(d.Path, proto.StorageFileName(id)) }

func (d Dir) ensure(disk proto.StorageDisk) error {
	path := d.file(disk.ID)
	if st, err := os.Stat(path); err == nil {
		if st.Size() < disk.SizeBytes {
			return os.Truncate(path, disk.SizeBytes)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	var src *os.File
	if disk.CopyFrom != "" {
		var err error
		if src, err = os.Open(d.file(disk.CopyFrom)); os.IsNotExist(err) {
			return nil // source not there yet, retried on the next Apply
		} else if err != nil {
			return err
		}
		defer src.Close()
	}

	// nbdkit exports every file in the dir, so build elsewhere and rename.
	partial := filepath.Join(d.Path, partialDir, filepath.Base(path))
	dst, err := os.OpenFile(partial, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if src != nil {
		err = copySparse(dst, src, disk.SizeBytes)
	}
	if err == nil {
		err = dst.Truncate(disk.SizeBytes)
	}
	// Without it a crash after the rename could leave a file with the copied data missing.
	if err == nil {
		err = dst.Sync()
	}
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(partial, path)
	}
	if err != nil {
		os.Remove(partial)
	}
	return err
}

// ponytail: zero-chunk sparse copy, use FICLONE where the fs supports reflink if copies get slow
func copySparse(dst, src *os.File, limit int64) error {
	buf, zero := make([]byte, chunkSize), make([]byte, chunkSize)
	for off := int64(0); off < limit; off += chunkSize {
		want := buf[:min(int64(chunkSize), limit-off)]
		n, err := src.ReadAt(want, off)
		if err != nil && err != io.EOF {
			return err
		}
		if n > 0 && !bytes.Equal(want[:n], zero[:n]) {
			if _, werr := dst.WriteAt(want[:n], off); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
	}
	return nil
}

// Scan lists regular files directly in Path, sorted by name.
func (d Dir) Scan() ([]proto.StorageFile, error) {
	entries, err := os.ReadDir(d.Path)
	if err != nil {
		return nil, err
	}
	var out []proto.StorageFile
	for _, e := range entries {
		st, err := e.Info()
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() {
			continue
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, fmt.Errorf("stat %s: unexpected type %T", e.Name(), st.Sys())
		}
		out = append(out, proto.StorageFile{Name: e.Name(), SizeBytes: st.Size(), AllocatedBytes: sys.Blocks * 512})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
