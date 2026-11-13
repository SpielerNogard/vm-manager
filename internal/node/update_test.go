package node

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vm-manager/internal/proto"
)

func TestInstallerManaged(t *testing.T) {
	for _, tc := range []struct {
		exe, goos, home string
		want            bool
	}{
		{"/usr/local/bin/vm-manager-node", "linux", "/root", true},
		{"/Users/a/.vm-manager/bin/vm-manager-node", "darwin", "/Users/a", true},
		{"/home/a/src/bin/vm-manager-node", "linux", "/home/a", false},
		{"/Users/a/.vm-manager/bin/node", "darwin", "/Users/a", false},
	} {
		if got := InstallerManaged(tc.exe, tc.goos, tc.home); got != tc.want {
			t.Errorf("InstallerManaged(%q, %q, %q) = %v, want %v", tc.exe, tc.goos, tc.home, got, tc.want)
		}
	}
}

func TestRegisterCarriesVersion(t *testing.T) {
	a, _ := newReconcileAgent(t)
	a.Version, a.Updatable = "v0.5.0", true
	if reg := a.registerMsg(); reg.Version != "v0.5.0" || !reg.Updatable {
		t.Errorf("register = version %q updatable %v", reg.Version, reg.Updatable)
	}
}

const updateToken = "tok-SECRET-0123456789"

// updateAgent returns an agent whose hub serves script as the installer, the messages it sends and the hub's request count.
func updateAgent(t *testing.T, status int, script string) (*Agent, *[]proto.Message, *atomic.Int32, string) {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		if req.URL.Path != "/node/install.sh" {
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
		w.Write([]byte(script))
	}))
	t.Cleanup(srv.Close)
	a, _ := newReconcileAgent(t)
	a.HubURL = "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/nodes/connect"
	a.Token, a.Updatable = updateToken, true
	var mu sync.Mutex
	var sent []proto.Message
	a.send = func(m proto.Message) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, m)
		return nil
	}
	a.installerCmd = func(script string, args []string) *exec.Cmd {
		return exec.Command("sh", append([]string{script}, args...)...)
	}
	return a, &sent, &requests, srv.URL
}

func TestHubHTTPURL(t *testing.T) {
	for in, want := range map[string]string{
		"ws://h:8080/api/nodes/connect":  "http://h:8080",
		"wss://h:8443/api/nodes/connect": "https://h:8443",
	} {
		if got, err := hubHTTPURL(in); err != nil || got != want {
			t.Errorf("hubHTTPURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"http://h:1", "ws://h/api/nodes/connect", ""} {
		if got, err := hubHTTPURL(in); err == nil {
			t.Errorf("hubHTTPURL(%q) = %q, want an error", in, got)
		}
	}
}

func TestUpdateRunsInstaller(t *testing.T) {
	out := t.TempDir()
	a, sent, _, hub := updateAgent(t, http.StatusOK, `printf '%s\n' "$@" > "`+out+`/args"; cat > "`+out+`/stdin"`)
	a.update(context.Background())
	args, _ := os.ReadFile(filepath.Join(out, "args"))
	want := "--hub\n" + hub + "\n--token-stdin\n--name\nn1\n--provider\n" + a.Provider.Name() + "\n"
	if string(args) != want {
		t.Errorf("installer arguments:\n%s\nwant:\n%s", args, want)
	}
	if stdin, _ := os.ReadFile(filepath.Join(out, "stdin")); string(stdin) != updateToken+"\n" {
		t.Errorf("installer stdin = %q, want the token", stdin)
	}
	if len(*sent) != 0 {
		t.Errorf("a successful installer run sent %+v", *sent)
	}
}

func TestUpdateReportsFailure(t *testing.T) {
	a, sent, _, _ := updateAgent(t, http.StatusOK, `i=0; while [ $i -lt 100 ]; do printf '%050d' 0; i=$((i+1)); done; cat >/dev/null; echo "the end" >&2; exit 3`)
	a.update(context.Background())
	if len(*sent) != 1 || (*sent)[0].Type != "updateFailed" {
		t.Fatalf("sent %+v, want one updateFailed", *sent)
	}
	msg := (*sent)[0].Error
	if !strings.Contains(msg, "exit status 3") || !strings.Contains(msg, "the end") {
		t.Errorf("error lacks the exit status or the end of the output: %q", msg)
	}
	if len(msg) > updateOutputTail+200 {
		t.Errorf("error is %d bytes, want at most %d", len(msg), updateOutputTail+200)
	}
	if strings.Contains(msg, updateToken) {
		t.Error("the token is in the reported error")
	}
}

func TestUpdateDownloadFails(t *testing.T) {
	a, sent, _, hub := updateAgent(t, http.StatusInternalServerError, "")
	a.update(context.Background())
	if len(*sent) != 1 || (*sent)[0].Type != "updateFailed" {
		t.Fatalf("sent %+v, want one updateFailed", *sent)
	}
	if msg := (*sent)[0].Error; !strings.Contains(msg, hub+"/node/install.sh") || !strings.Contains(msg, "500") {
		t.Errorf("error = %q", msg)
	}
}

func TestUpdateNotUpdatable(t *testing.T) {
	a, sent, requests, _ := updateAgent(t, http.StatusOK, "exit 0")
	a.Updatable = false
	a.update(context.Background())
	if len(*sent) != 1 || !strings.Contains((*sent)[0].Error, "not installed by the installer") {
		t.Errorf("sent %+v", *sent)
	}
	if requests.Load() != 0 {
		t.Error("the installer was downloaded for a node that cannot be updated")
	}
}

func TestUpdateOnlyOnce(t *testing.T) {
	a, sent, requests, _ := updateAgent(t, http.StatusOK, "sleep 1")
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); a.update(context.Background()) }()
	}
	wg.Wait()
	if n := requests.Load(); n != 1 {
		t.Errorf("installer downloaded %d times, want 1", n)
	}
	if len(*sent) != 1 || !strings.Contains((*sent)[0].Error, "already running") {
		t.Errorf("the second update was dropped without a report: %+v", *sent)
	}
}

func TestUpdateSilentWhenNodeStops(t *testing.T) {
	// A successful installer stops this node, which also ends the installer's client process with an error.
	a, sent, _, _ := updateAgent(t, http.StatusOK, "sleep 0.3; exit 143")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	a.update(ctx)
	if len(*sent) != 0 {
		t.Errorf("a stopping node reported %+v", *sent)
	}
}

func TestRemoveStaleUpdateDirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	stale, other := filepath.Join(tmp, "vm-manager-update-123"), filepath.Join(tmp, "something-else")
	for _, d := range []string{stale, other} {
		if err := os.MkdirAll(filepath.Join(d, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	removeStaleUpdateDirs()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale update directory still there: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("unrelated directory removed: %v", err)
	}
}
