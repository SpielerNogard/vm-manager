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
	if len(os.Args) > 1 && os.Args[1] == "init" {
		if err := runInit(os.Args[2:], home); err != nil {
			log.Fatal(err)
		}
		return
	}
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

// runInit writes or updates node.json for the installer; data lives under dataDir unless the config already says otherwise.
func runInit(args []string, home string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	var o node.InitOptions
	fs.StringVar(&o.Path, "config", filepath.Join(home, ".vm-manager", "node.json"), "path to node config")
	fs.StringVar(&o.HubURL, "hub", "", "hub address, http://host:port")
	fs.StringVar(&o.Token, "token", "", "hub token")
	tokenStdin := fs.Bool("token-stdin", false, "read the hub token from stdin")
	fs.StringVar(&o.Name, "name", "", "node name")
	fs.StringVar(&o.Provider, "provider", "", "hypervisor")
	fs.StringVar(&o.UnderlayEndpoint, "underlay-endpoint", "", "hub WireGuard endpoint host:port")
	fs.IntVar(&o.UnderlayMTU, "underlay-mtu", 0, "WireGuard MTU")
	fs.IntVar(&o.MemoryMB, "memory-mb", 0, "memory offered to VMs")
	fs.Parse(args)
	if *tokenStdin {
		t, err := node.ReadToken(os.Stdin)
		if err != nil {
			return err
		}
		o.Token = t
	}
	dir := dataDir(home)
	return node.InitConfig(o, filepath.Join(dir, "vms"), filepath.Join(dir, "images"))
}
