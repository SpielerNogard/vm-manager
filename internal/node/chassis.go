package node

import (
	"errors"
	"os"
	"strings"

	"vm-manager/internal/wgkey"
)

func LoadOrCreateChassisKey(path string) (priv, pub string, err error) {
	b, err := os.ReadFile(path)
	if err == nil {
		priv = strings.TrimSpace(string(b))
		pub, err = wgkey.Public(priv)
		return priv, pub, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	if priv, pub, err = wgkey.Generate(); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, []byte(priv+"\n"), 0o600); err != nil {
		return "", "", err
	}
	return priv, pub, nil
}
