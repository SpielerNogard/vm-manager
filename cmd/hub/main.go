package main

import (
	"flag"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"vm-manager/internal/hub"
	"vm-manager/web"
)

func main() {
	home, _ := os.UserHomeDir()
	configPath := flag.String("config", filepath.Join(home, ".vm-manager", "hub.json"), "path to hub config")
	flag.Parse()

	cfg, err := hub.LoadConfig(expandHome(*configPath, home))
	if err != nil {
		log.Fatal(err)
	}

	r := hub.NewRegistry(cfg.SSHPorts.Min, cfg.SSHPorts.Max)
	listeners := map[int]io.Closer{} // only touched from registry hooks, which run under the registry lock
	r.OnPortOpen = func(p int) error {
		l, err := hub.ServeSSHPort(r, p)
		if err != nil {
			return err
		}
		listeners[p] = l
		return nil
	}
	r.OnPortClose = func(p int) {
		if l, ok := listeners[p]; ok {
			l.Close()
			delete(listeners, p)
		}
	}

	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/nodes/connect", hub.NodeHandler(r, cfg.Token))
	mux.Handle("/api/", hub.APIHandler(r))
	mux.Handle("/", http.FileServerFS(dist))

	log.Printf("hub listening on %s, SSH ports %d-%d", cfg.Listen, cfg.SSHPorts.Min, cfg.SSHPorts.Max)
	log.Fatal(http.ListenAndServe(cfg.Listen, mux))
}

func expandHome(p, home string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[1:])
	}
	return p
}
