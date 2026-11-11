package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type peer struct {
	PublicKey string
	AllowedIP string
}

func syncWG(run runner, iface, privKey string, listenPort int, peers []peer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nListenPort = %d\n", privKey, listenPort)
	for _, p := range peers {
		fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nAllowedIPs = %s\n", p.PublicKey, p.AllowedIP)
	}
	f, err := os.CreateTemp("", "wg-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, werr := f.WriteString(b.String())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	if _, err := run("wg", "syncconf", iface, f.Name()); err != nil {
		return fmt.Errorf("wg syncconf %s: %w", iface, err)
	}
	return nil
}

func handshakes(run runner, iface string) (map[string]time.Time, error) {
	out, err := run("wg", "show", iface, "latest-handshakes")
	if err != nil {
		return nil, err
	}
	res := map[string]time.Time{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		var sec int64
		if _, err := fmt.Sscan(f[1], &sec); err == nil && sec > 0 {
			res[f[0]] = time.Unix(sec, 0)
		}
	}
	return res, nil
}
