package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"vm-manager/internal/version"
)

const (
	releaseDownloads  = "https://github.com/SpielerNogard/vm-manager/releases/download/"
	hubDownloadWait   = 10 * time.Minute
	checksumsMaxBytes = 1 << 20
)

func hubAsset(goos, goarch string) string { return "vm-manager-hub-" + goos + "-" + goarch }

// restartInPlace replaces this process with the binary at exe, keeping its arguments; it only returns on failure.
func restartInPlace(exe string) error { return syscall.Exec(exe, os.Args, os.Environ()) }

// StartHubUpdate begins replacing the hub's own binary with the latest release; the outcome shows in Status.
func (u *Updates) StartHubUpdate() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	refuse := func(why string) error { return fmt.Errorf("%w: %s", ErrConflict, why) }
	switch {
	case !version.Released(version.Version):
		return refuse("this hub is a development build")
	case u.SelfExe == "":
		return refuse("this hub does not update itself; in Docker Compose set VMM_VERSION and rebuild the stack")
	case !version.Older(version.Version, u.latest):
		return refuse("the hub is up to date")
	case u.hubUpdating:
		return refuse("a hub update is already running")
	}
	u.hubUpdating, u.hubErr = true, ""
	target := u.latest
	go func() {
		err := u.applyHubUpdate(context.Background(), target)
		u.mu.Lock()
		defer u.mu.Unlock()
		u.hubUpdating = false
		if err != nil {
			log.Printf("hub update: %v", err)
			u.hubErr = err.Error()
		}
	}()
	return nil
}

// applyHubUpdate downloads and verifies the hub binary of target before it touches the running one.
func (u *Updates) applyHubUpdate(ctx context.Context, target string) error {
	ctx, cancel := context.WithTimeout(ctx, hubDownloadWait)
	defer cancel()
	asset, base := hubAsset(runtime.GOOS, runtime.GOARCH), strings.TrimRight(u.ReleaseURL(target), "/")
	want, err := u.checksum(ctx, base+"/SHA256SUMS", asset, target)
	if err != nil {
		return err
	}
	fresh := u.SelfExe + ".new"
	if err := u.downloadVerified(ctx, base+"/"+asset, fresh, want); err != nil {
		os.Remove(fresh)
		return err
	}
	previous := u.SelfExe + ".previous"
	os.Remove(previous)
	if err := os.Link(u.SelfExe, previous); err != nil {
		os.Remove(fresh)
		return fmt.Errorf("keeping the previous hub binary: %w", err)
	}
	if err := os.Rename(fresh, u.SelfExe); err != nil {
		os.Remove(fresh)
		os.Remove(previous)
		return err
	}
	if u.BeforeRestart != nil {
		u.BeforeRestart()
	}
	if err := u.restart(u.SelfExe); err != nil {
		// Put the running version back, so a second attempt cannot overwrite it in .previous.
		if rerr := os.Rename(previous, u.SelfExe); rerr != nil {
			return fmt.Errorf("%s is installed but the hub could not restart itself (%w), and the previous binary could not be put back (%v); restart the hub service", target, err, rerr)
		}
		return fmt.Errorf("the hub could not restart itself as %s (%w); it keeps running %s, restart the hub service before trying again", target, err, version.Version)
	}
	return nil
}

func (u *Updates) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "vm-manager")
	// The check's client has a short timeout; a binary download is bounded by ctx instead.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	return resp, nil
}

func (u *Updates) checksum(ctx context.Context, url, asset, target string) (string, error) {
	resp, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, checksumsMaxBytes))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("%s is missing from SHA256SUMS of %s", asset, target)
}

func (u *Updates) downloadVerified(ctx context.Context, url, to, want string) error {
	resp, err := u.get(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, SHA256SUMS says %s", url, got, want)
	}
	return nil
}
