package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

type selfUpdateEnv struct {
	u        *Updates
	exe      string
	files    map[string]string // served release files
	restarts []string
	requests atomic.Int32  // of the hub binary
	block    chan struct{} // non-nil: the binary download waits for it
}

// selfUpdater returns a v0.5.0 hub that knows v0.6.0 and downloads it from a local release server.
func selfUpdater(t *testing.T) *selfUpdateEnv {
	t.Helper()
	setHubVersion(t, "v0.5.0")
	asset := hubAsset(runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256([]byte("new binary"))
	e := &selfUpdateEnv{files: map[string]string{asset: "new binary", "SHA256SUMS": hex.EncodeToString(sum[:]) + "  " + asset + "\n"}}
	rel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		name := strings.TrimPrefix(req.URL.Path, "/")
		if name == asset {
			e.requests.Add(1)
			if e.block != nil {
				<-e.block
			}
		}
		body, ok := e.files[name]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(rel.Close)
	status, answer := 0, `{"tag_name":"v0.6.0"}`
	e.u, _, _ = releaseServer(t, &status, &answer)
	if err := e.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.exe = filepath.Join(t.TempDir(), "vm-manager-hub")
	if err := os.WriteFile(e.exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.u.SelfExe = e.exe
	e.u.ReleaseURL = func(string) string { return rel.URL }
	e.u.restart = func(exe string) error { e.restarts = append(e.restarts, exe); return nil }
	return e
}

func (e *selfUpdateEnv) untouched(t *testing.T) {
	t.Helper()
	if b, _ := os.ReadFile(e.exe); string(b) != "old binary" {
		t.Errorf("the hub binary was replaced: %q", b)
	}
	for _, suffix := range []string{".new", ".previous"} {
		if _, err := os.Stat(e.exe + suffix); err == nil {
			t.Errorf("%s was left behind", filepath.Base(e.exe)+suffix)
		}
	}
	if len(e.restarts) != 0 {
		t.Error("the hub restarted")
	}
}

func TestHubAsset(t *testing.T) {
	if got := hubAsset("linux", "amd64"); got != "vm-manager-hub-linux-amd64" {
		t.Error(got)
	}
	if got := hubAsset("darwin", "arm64"); got != "vm-manager-hub-darwin-arm64" {
		t.Error(got)
	}
}

func TestApplyHubUpdate(t *testing.T) {
	e := selfUpdater(t)
	if err := e.u.applyHubUpdate(context.Background(), "v0.6.0"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(e.exe); string(b) != "new binary" {
		t.Errorf("hub binary = %q", b)
	}
	if fi, err := os.Stat(e.exe); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("hub binary mode: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(e.exe + ".previous"); string(b) != "old binary" {
		t.Errorf("previous binary = %q", b)
	}
	if _, err := os.Stat(e.exe + ".new"); err == nil {
		t.Error("the download was left behind")
	}
	if len(e.restarts) != 1 || e.restarts[0] != e.exe {
		t.Errorf("restarts = %q", e.restarts)
	}
}

func TestApplyHubUpdateChecksumMismatch(t *testing.T) {
	e := selfUpdater(t)
	e.files["SHA256SUMS"] = strings.Repeat("0", 64) + "  " + hubAsset(runtime.GOOS, runtime.GOARCH) + "\n"
	err := e.u.applyHubUpdate(context.Background(), "v0.6.0")
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v", err)
	}
	e.untouched(t)
}

func TestApplyHubUpdateAssetMissing(t *testing.T) {
	asset := hubAsset(runtime.GOOS, runtime.GOARCH)
	e := selfUpdater(t)
	delete(e.files, asset)
	if err := e.u.applyHubUpdate(context.Background(), "v0.6.0"); err == nil || !strings.Contains(err.Error(), asset) {
		t.Fatalf("no binary for this platform: err = %v", err)
	}
	e.untouched(t)

	e = selfUpdater(t)
	e.files["SHA256SUMS"] = strings.Repeat("0", 64) + "  something-else\n"
	if err := e.u.applyHubUpdate(context.Background(), "v0.6.0"); err == nil || !strings.Contains(err.Error(), "is missing from SHA256SUMS") {
		t.Fatalf("no checksum line: err = %v", err)
	}
	e.untouched(t)
}

func TestApplyHubUpdateNotWritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	e := selfUpdater(t)
	dir := filepath.Dir(e.exe)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if err := e.u.applyHubUpdate(context.Background(), "v0.6.0"); err == nil {
		t.Fatal("no error")
	}
	e.untouched(t)
}

func TestStartHubUpdateRefused(t *testing.T) {
	refused := func(t *testing.T, e *selfUpdateEnv, want string) {
		t.Helper()
		err := e.u.StartHubUpdate()
		if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want a conflict containing %q", err, want)
		}
	}
	t.Run("compose", func(t *testing.T) {
		e := selfUpdater(t)
		e.u.SelfExe = ""
		refused(t, e, "Compose")
	})
	t.Run("up to date", func(t *testing.T) {
		e := selfUpdater(t)
		setHubVersion(t, "v0.6.0")
		refused(t, e, "up to date")
	})
	t.Run("dev", func(t *testing.T) {
		e := selfUpdater(t)
		setHubVersion(t, "dev")
		refused(t, e, "development build")
	})
	t.Run("twice", func(t *testing.T) {
		e := selfUpdater(t)
		e.block = make(chan struct{})
		if err := e.u.StartHubUpdate(); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the download to start", func() bool { return e.requests.Load() == 1 })
		refused(t, e, "already running")
		close(e.block)
		r, _ := newTestRegistry(t)
		waitFor(t, "the update to finish", func() bool { return !e.u.Status(r, nil).HubUpdating })
		if n := e.requests.Load(); n != 1 {
			t.Errorf("the binary was downloaded %d times", n)
		}
	})
}

func TestHubUpdateStatus(t *testing.T) {
	e := selfUpdater(t)
	r, _ := newTestRegistry(t)
	if st := e.u.Status(r, nil); st.HubUpdate != "self" || !st.UpdateAvailable {
		t.Errorf("status = %+v", st)
	}
	delete(e.files, "SHA256SUMS")
	if err := e.u.StartHubUpdate(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failed update", func() bool { return e.u.Status(r, nil).HubUpdateError != "" })
	if st := e.u.Status(r, nil); st.HubUpdating {
		t.Errorf("status = %+v", st)
	}
	e.u.SelfExe = ""
	if st := e.u.Status(r, nil); st.HubUpdate != "manual" {
		t.Errorf("hubUpdate without a binary to replace = %q", st.HubUpdate)
	}
}

func TestApplyAPI(t *testing.T) {
	e := selfUpdater(t)
	srv, _ := updatesServer(t, testAPIConfig(), e.u)
	if code, body := doJSON(t, "POST", srv.URL+"/api/updates/apply", ""); code != 202 {
		t.Errorf("status %d body %s", code, body)
	}
	r, _ := newTestRegistry(t)
	waitFor(t, "the update to finish", func() bool { return !e.u.Status(r, nil).HubUpdating })
	e.u.SelfExe = ""
	if code, _ := doJSON(t, "POST", srv.URL+"/api/updates/apply", ""); code != 409 {
		t.Errorf("manual hub: status %d, want 409", code)
	}
}

func TestApplyHubUpdateRestartFails(t *testing.T) {
	e := selfUpdater(t)
	var order []string
	e.u.BeforeRestart = func() { order = append(order, "stop stack") }
	e.u.restart = func(string) error { order = append(order, "restart"); return errors.New("exec format error") }
	err := e.u.applyHubUpdate(context.Background(), "v0.6.0")
	if err == nil || !strings.Contains(err.Error(), "exec format error") {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(order, ",") != "stop stack,restart" {
		t.Errorf("order = %v, want the stack stopped before the restart", order)
	}
	// A second click must not overwrite .previous with a binary that cannot start.
	if b, _ := os.ReadFile(e.exe); string(b) != "old binary" {
		t.Errorf("after a failed restart the hub binary is %q, want the old one back", b)
	}
	if _, err := os.Stat(e.exe + ".previous"); err == nil {
		t.Error(".previous left behind after the rollback")
	}
}
