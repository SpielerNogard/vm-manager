package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"vm-manager/internal/version"
)

func main() {
	hubURL := flag.String("hub", "", "hub base URL, e.g. http://host.docker.internal:8080")
	token := flag.String("token", os.Getenv("HUB_TOKEN"), "hub token")
	tokenFile := flag.String("token-file", "", "hub.json to read the token from on every request, instead of -token")
	listen := flag.String("listen", "10.201.0.1:9002", "address to serve on")
	allowCIDR := flag.String("allow", "10.200.0.0/16", "only answer sources in this prefix")
	health := flag.String("health", "10.201.0.1:53", "address that must accept TCP connections, else exit; empty disables")
	flag.Parse()
	log.Printf("%s %s", filepath.Base(os.Args[0]), version.Version)
	if (*token == "") == (*tokenFile == "") {
		log.Fatal("set exactly one of -token (or HUB_TOKEN) and -token-file")
	}
	if *hubURL == "" {
		log.Fatal("-hub is required")
	}
	allow, err := netip.ParsePrefix(*allowCIDR)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *health != "" {
		go func() { log.Fatal(watchHealth(ctx, *health, 10*time.Second, 3)) }()
	}
	srv := &http.Server{Addr: *listen, Handler: newHandler(*hubURL, hubToken(*token, *tokenFile), allow, &http.Client{Timeout: 5 * time.Second})}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	log.Printf("token service on %s", *listen)
	if err := srv.ListenAndServe(); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func newHandler(hubURL string, hubToken func() (string, error), allow netip.Prefix, client *http.Client) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/{name}/token", func(w http.ResponseWriter, req *http.Request) {
		host, _, err := net.SplitHostPort(req.RemoteAddr)
		if err != nil {
			http.Error(w, "bad remote address", http.StatusForbidden)
			return
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || !allow.Contains(ip.Unmap()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		tok, err := hubToken()
		if err != nil {
			log.Printf("hub token: %v", err)
			http.Error(w, "hub token unavailable", http.StatusBadGateway)
			return
		}
		target := strings.TrimRight(hubURL, "/") + "/api/tokens/" + url.PathEscape(req.PathValue("name")) + "?ip=" + url.QueryEscape(ip.Unmap().String())
		hubReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadGateway)
			return
		}
		hubReq.Header.Set("Authorization", "Bearer "+tok)
		resp, err := client.Do(hubReq)
		if err != nil {
			log.Printf("hub: %v", err)
			http.Error(w, "hub unreachable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			w.WriteHeader(resp.StatusCode)
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			http.Error(w, "unreadable hub response", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, body.Token)
	})
	return mux
}

func watchHealth(ctx context.Context, addr string, every time.Duration, failures int) error {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	failed := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		conn, err := net.DialTimeout("tcp", addr, every)
		if err == nil {
			conn.Close()
			failed = 0
			continue
		}
		if failed++; failed >= failures {
			return fmt.Errorf("health check %s failed %d times: %w", addr, failed, err)
		}
	}
}

func hubToken(token, file string) func() (string, error) {
	if file == "" {
		return func() (string, error) { return token, nil }
	}
	return func() (string, error) {
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		var cfg struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return "", fmt.Errorf("%s: %w", file, err)
		}
		if cfg.Token == "" {
			return "", fmt.Errorf("no token in %s", file)
		}
		return cfg.Token, nil
	}
}
