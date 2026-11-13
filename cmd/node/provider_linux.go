package main

import (
	"fmt"

	"vm-manager/internal/node"
	"vm-manager/internal/provider/cloudhypervisor"
	"vm-manager/internal/provider/firecracker"
)

func newProvider(cfg node.Config) (node.Provider, error) {
	switch cfg.Provider {
	case "firecracker":
		return firecracker.New(cfg)
	case "cloud-hypervisor":
		return cloudhypervisor.New(cfg)
	default:
		return nil, fmt.Errorf("unknown provider %q for this OS", cfg.Provider)
	}
}

func dataDir(string) string { return "/var/lib/vm-manager" }
