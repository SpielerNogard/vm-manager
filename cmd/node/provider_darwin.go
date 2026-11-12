package main

import (
	"fmt"

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
