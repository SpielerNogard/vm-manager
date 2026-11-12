package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"vm-manager/internal/node"
	"vm-manager/internal/version"
)

func main() {
	home, _ := os.UserHomeDir()
	configPath := flag.String("config", filepath.Join(home, ".vm-manager", "node.json"), "path to node config")
	flag.Parse()

	cfg, err := node.LoadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	provider, err := newProvider(cfg)
	if err != nil {
		log.Fatal(err)
	}
	for _, d := range []string{cfg.Paths.VMs, cfg.Paths.Images} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatal(err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	public, err := cfg.Public()
	if err != nil {
		log.Fatal(err)
	}
	chassisPriv, chassisPub, err := node.LoadOrCreateChassisKey(filepath.Join(cfg.Paths.VMs, "chassis.key"))
	if err != nil {
		log.Fatal(err)
	}
	a := &node.Agent{
		ChassisKey: chassisPub, ChassisPrivateKey: chassisPriv, UnderlayEndpoint: cfg.Underlay.Endpoint, UnderlayMTU: cfg.Underlay.MTU,
		HubURL: cfg.Hub.URL, Token: cfg.Hub.Token, NodeID: cfg.NodeID, VMsDir: cfg.Paths.VMs,
		CPUs: cfg.Capacity.CPUs, MemoryMB: cfg.Capacity.MemoryMB, Provider: provider,
		Config: public, Machine: node.MachineInfo(),
	}
	log.Printf("node %s (%s): provider %s, %d CPUs, %d MB", cfg.NodeID, version.Version, provider.Name(), cfg.Capacity.CPUs, cfg.Capacity.MemoryMB)
	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
