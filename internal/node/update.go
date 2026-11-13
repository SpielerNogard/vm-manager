package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"vm-manager/internal/proto"
)

const (
	updateOutputTail  = 2000
	installScriptMax  = 1 << 20
	installScriptWait = 30 * time.Second
)

// InstallerManaged reports whether exe is where install.sh puts the node, so re-running the installer updates this node.
func InstallerManaged(exe, goos, home string) bool {
	want := "/usr/local/bin/vm-manager-node"
	if goos == "darwin" {
		want = filepath.Join(home, ".vm-manager", "bin", "vm-manager-node")
	}
	return filepath.Clean(exe) == want
}

func hubHTTPURL(wsURL string) (string, error) {
	u, err := url.Parse(wsURL)
	if err != nil || u.Host == "" || u.Port() == "" || (u.Scheme != "ws" && u.Scheme != "wss") {
		return "", fmt.Errorf("hub url %q: want ws://host:port/...", wsURL)
	}
	scheme := "http"
	if u.Scheme == "wss" {
		scheme = "https"
	}
	return scheme + "://" + u.Host, nil
}

// update runs the hub's installer, which replaces and restarts this node; only a failure comes back to report.
func (a *Agent) update(ctx context.Context) {
	err := errors.New("an update is already running on this node")
	if a.updateMu.TryLock() {
		defer a.updateMu.Unlock()
		err = a.runInstaller(ctx)
	}
	// A node that is stopping was most likely stopped by the installer; its client process dies with it.
	if err != nil && ctx.Err() == nil {
		log.Printf("update: %v", err)
		a.mu.Lock()
		send := a.send
		a.mu.Unlock()
		if send != nil {
			send(proto.Message{Type: "updateFailed", Error: err.Error()})
		}
	}
}

func (a *Agent) runInstaller(ctx context.Context) error {
	if !a.Updatable {
		return errors.New("this node was not installed by the installer; re-run its install command")
	}
	hub, err := hubHTTPURL(a.HubURL)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "vm-manager-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "install.sh")
	if err := download(ctx, hub+"/node/install.sh", script); err != nil {
		return err
	}
	// Regular files, not Go pipes: the installer outlives this process and must not die of SIGPIPE when it stops the node.
	out, err := os.Create(filepath.Join(dir, "install.log"))
	if err != nil {
		return err
	}
	defer out.Close()
	tokenR, tokenW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer tokenR.Close()
	if _, err := tokenW.WriteString(a.Token + "\n"); err != nil {
		tokenW.Close()
		return err
	}
	tokenW.Close()

	// node init keeps every node.json key it is not given, so the optional installer options are not repeated.
	args := []string{"--hub", hub, "--token-stdin", "--name", a.NodeID, "--provider", a.Provider.Name()}
	build := a.installerCmd
	if build == nil {
		build = installerCommand
	}
	cmd := build(script, args)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tokenR, out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("installer: %w\n%s", err, tail(out.Name(), updateOutputTail))
	}
	return nil
}

// removeStaleUpdateDirs clears what earlier updates left: the installer stops the node before its directory can be removed.
func removeStaleUpdateDirs() {
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), "vm-manager-update-*"))
	for _, d := range dirs {
		os.RemoveAll(d)
	}
}

func download(ctx context.Context, from, to string) error {
	ctx, cancel := context.WithTimeout(ctx, installScriptWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, from, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: %s", from, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, installScriptMax))
	if err != nil {
		return fmt.Errorf("downloading %s: %w", from, err)
	}
	return os.WriteFile(to, b, 0o700)
}

func tail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}
