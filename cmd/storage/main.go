package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"vm-manager/internal/storage"
	"vm-manager/internal/version"
)

func main() {
	hubURL := flag.String("hub", "", "hub WebSocket URL, e.g. ws://netns:8080/api/storage/connect")
	token := flag.String("token", os.Getenv("HUB_TOKEN"), "hub token")
	tokenFile := flag.String("token-file", "", "hub.json to read the token from on every connect, instead of -token")
	dir := flag.String("dir", "/disks", "directory holding the disk files")
	nbdPort := flag.Int("nbd-port", 10809, "NBD port nbdkit listens on")
	nbdkit := flag.Bool("nbdkit", true, "run nbdkit as a child process")
	flag.Parse()
	log.Printf("%s %s", filepath.Base(os.Args[0]), version.Version)
	if (*token == "") == (*tokenFile == "") {
		log.Fatal("set exactly one of -token (or HUB_TOKEN) and -token-file")
	}
	if *hubURL == "" {
		log.Fatal("-hub is required")
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *nbdkit {
		cmd := exec.Command("nbdkit", "--foreground", "--exit-with-parent", "--port", strconv.Itoa(*nbdPort), "file", "dir="+*dir)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			log.Fatal(err)
		}
		go func() { log.Fatalf("nbdkit exited: %v", cmd.Wait()) }()
	}
	log.Printf("storage agent serving %s", *dir)
	if err := storage.Run(ctx, *hubURL, hubToken(*token, *tokenFile), storage.Dir{Path: *dir}, 60*time.Second); err != nil && ctx.Err() == nil {
		log.Fatal(err)
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
