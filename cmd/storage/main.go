package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"vm-manager/internal/storage"
)

func main() {
	hubURL := flag.String("hub", "", "hub WebSocket URL, e.g. ws://netns:8080/api/storage/connect")
	token := flag.String("token", os.Getenv("HUB_TOKEN"), "hub token")
	advertise := flag.String("advertise", os.Getenv("STORAGE_ENDPOINT"), "host:port nodes use to reach the NBD server")
	dir := flag.String("dir", "/disks", "directory holding the disk files")
	nbdPort := flag.Int("nbd-port", 10809, "NBD port nbdkit listens on")
	nbdkit := flag.Bool("nbdkit", true, "run nbdkit as a child process")
	flag.Parse()
	if *hubURL == "" || *advertise == "" {
		log.Fatal("-hub and -advertise (or STORAGE_ENDPOINT) are required")
	}
	if host, _, err := net.SplitHostPort(*advertise); err != nil || host == "" {
		log.Fatalf("-advertise %q must be host:port", *advertise)
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
	log.Printf("storage agent serving %s, advertised as %s", *dir, *advertise)
	if err := storage.Run(ctx, *hubURL, *token, *advertise, storage.Dir{Path: *dir}, 60*time.Second); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
