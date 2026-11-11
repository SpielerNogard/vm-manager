package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"vm-manager/internal/wgkey"
)

func LoadOrCreateChassisKey(path string) (priv, pub string, err error) {
	st, err := os.Stat(path)
	if err == nil {
		if st.Mode().Perm()&0o077 != 0 {
			return "", "", fmt.Errorf("chassis key %s has permissions %o, want 0600", path, st.Mode().Perm())
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", "", err
		}
		priv = strings.TrimSpace(string(b))
		if pub, err = wgkey.Public(priv); err != nil {
			return "", "", fmt.Errorf("chassis key %s is invalid (delete it to generate a new one): %w", path, err)
		}
		return priv, pub, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	if priv, pub, err = wgkey.Generate(); err != nil {
		return "", "", err
	}
	if err := writeFileAtomic(path, []byte(priv+"\n")); err != nil {
		return "", "", err
	}
	return priv, pub, nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
