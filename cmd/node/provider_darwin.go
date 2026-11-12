package main

import (
	"fmt"
	"path/filepath"

	"vm-manager/internal/node"
	"vm-manager/internal/provider/vz"
)

func newProvider(cfg node.Config) (node.Provider, error) {
	switch cfg.Provider {
	case "vz":
		return vz.New(cfg)
	default:
		return nil, fmt.Errorf("unknown provider %q for this OS", cfg.Provider)
	}
}

func dataDir(home string) string { return filepath.Join(home, ".vm-manager") }
