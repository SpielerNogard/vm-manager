package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"vm-manager/internal/hub"
)

func main() {
	hubURL := flag.String("hub", "http://127.0.0.1:8080", "hub base URL")
	tokenFile := flag.String("token-file", "/data/hub.json", "hub config holding the API token")
	nbDB := flag.String("nb", "unix:/var/run/ovn/ovnnb_db.sock", "OVN NB database")
	flag.Parse()

	raw, err := os.ReadFile(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	var cfg struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil || cfg.Token == "" {
		log.Fatalf("no token in %s", *tokenFile)
	}

	run := func(args ...string) (string, error) {
		if args[0] == "ovn-nbctl" {
			args = append([]string{args[0], "--db=" + *nbDB}, args[1:]...)
		}
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("%w: %s", err, bytes.TrimSpace(out))
		}
		return string(out), nil
	}

	dns := &dnsServer{}
	upstream := upstreamResolver()
	pc, err := net.ListenPacket("udp", "10.201.0.1:53")
	if err != nil {
		log.Fatal(err)
	}
	tl, err := net.Listen("tcp", "10.201.0.1:53")
	if err != nil {
		log.Fatal(err)
	}
	go dns.serveUDP(pc, upstream)
	go dns.serveTCP(tl, upstream)

	client := &http.Client{Timeout: 5 * time.Second}
	call := func(method, path string, body []byte) ([]byte, error) {
		req, err := http.NewRequest(method, *hubURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s %s: %s", method, path, resp.Status)
		}
		return b, nil
	}

	var lastHash [32]byte
	var lastApply, lastStatus time.Time
	for ; ; time.Sleep(2 * time.Second) {
		body, err := call("GET", "/api/gateway/state", nil)
		if err != nil {
			log.Print(err)
			continue
		}
		var st hub.GatewayState
		if err := json.Unmarshal(body, &st); err != nil {
			log.Print(err)
			continue
		}
		records := map[string]string{}
		for _, p := range st.Ports {
			records[strings.ToLower(p.Hostname)+"."] = p.IP
		}
		dns.setRecords(records)

		if h := sha256.Sum256(body); h != lastHash || time.Since(lastApply) > 60*time.Second {
			if err := apply(run, st); err != nil {
				log.Print(err)
			} else {
				lastHash, lastApply = h, time.Now()
			}
		}
		if time.Since(lastStatus) >= 10*time.Second {
			lastStatus = time.Now()
			if b, err := json.Marshal(observe(run)); err == nil {
				if _, err := call("POST", "/api/gateway/status", b); err != nil {
					log.Print(err)
				}
			}
		}
	}
}

func apply(run runner, st hub.GatewayState) error {
	if err := reconcileNB(run, st); err != nil {
		return err
	}
	var underlay, clients []peer
	for _, c := range st.Chassis {
		underlay = append(underlay, peer{c.PublicKey, c.IP + "/32"})
	}
	for _, d := range st.Devices {
		clients = append(clients, peer{d.PublicKey, d.IP + "/32"})
	}
	if err := syncWG(run, "wg-underlay", st.UnderlayKey, 51820, underlay); err != nil {
		return err
	}
	return syncWG(run, "wg-clients", st.ClientsKey, 51821, clients)
}

func observe(run runner) hub.GatewayStatus {
	s := hub.GatewayStatus{Handshakes: map[string]time.Time{}, Chassis: []string{}}
	for _, iface := range []string{"wg-underlay", "wg-clients"} {
		hs, err := handshakes(run, iface)
		if err != nil {
			log.Print(err)
		}
		for k, v := range hs {
			s.Handshakes[k] = v
		}
	}
	if out, err := run("ovn-sbctl", "--bare", "--columns=name", "find", "chassis"); err == nil {
		s.Chassis = strings.Fields(out)
	} else {
		log.Print(err)
	}
	return s
}

func upstreamResolver() string {
	b, _ := os.ReadFile("/etc/resolv.conf")
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "nameserver" {
			return net.JoinHostPort(f[1], "53")
		}
	}
	return "127.0.0.11:53"
}
