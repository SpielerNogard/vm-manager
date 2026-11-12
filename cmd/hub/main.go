package main

import (
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"vm-manager/internal/hub"
	"vm-manager/web"
)

func main() {
	home, _ := os.UserHomeDir()
	configPath := flag.String("config", filepath.Join(home, ".vm-manager", "hub.json"), "path to hub config")
	sshDir := flag.String("ssh-dir", "~/.ssh", "directory holding the hub's default SSH public keys")
	flag.Parse()

	cfg, err := hub.LoadOrCreateConfig(expandHome(*configPath, home), os.Getenv)
	if err != nil {
		log.Fatal(err)
	}

	keysPath := filepath.Join(filepath.Dir(expandHome(*configPath, home)), "keys.json")
	keys, err := hub.LoadKeyStore(keysPath)
	if err != nil {
		log.Fatal(err)
	}
	hubKeys, err := keys.LoadHubKeys(hub.HubKeyFiles(expandHome(*sshDir, home)))
	if err != nil {
		log.Fatal(err)
	}
	for _, k := range hubKeys {
		log.Printf("hub SSH key %s (%s)", k.Name, k.Fingerprint)
	}

	dir := filepath.Dir(expandHome(*configPath, home))
	netModel, err := hub.LoadNetModel(filepath.Join(dir, "network.json"))
	if err != nil {
		log.Fatal(err)
	}
	vmStore, err := hub.LoadVMStore(filepath.Join(dir, "vms.json"))
	if err != nil {
		log.Fatal(err)
	}
	r := hub.NewRegistry(netModel, vmStore, cfg)

	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/nodes/connect", hub.NodeHandler(r, func() string { return cfg.Get().Token }))
	mux.Handle("/api/", hub.APIHandler(r, keys, netModel, cfg))
	mux.Handle("/", spa(dist))

	listen := cfg.Get().Listen
	log.Printf("hub listening on %s, keys in %s", listen, keysPath)
	log.Fatal(http.ListenAndServe(listen, mux))
}

// spa serves index.html for extensionless paths that are not files, so GUI routes like /vms survive a reload.
func spa(dist fs.FS) http.Handler {
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean(r.URL.Path)
		if _, err := fs.Stat(dist, strings.TrimPrefix(p, "/")); err != nil && path.Ext(p) == "" {
			http.ServeFileFS(w, r, dist, "index.html")
			return
		}
		files.ServeHTTP(w, r)
	})
}

func expandHome(p, home string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[1:])
	}
	return p
}
