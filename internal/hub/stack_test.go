package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stackRunner records docker calls; fail maps a compose subcommand to the output it fails with.
type stackRunner struct {
	mu    sync.Mutex
	calls []string
	dirs  []string
	fail  map[string]string
	// failBuilds fails that many build calls before they succeed.
	failBuilds int
}

func (r *stackRunner) run(_ context.Context, dir string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := strings.Join(args, " ")
	r.calls = append(r.calls, call)
	r.dirs = append(r.dirs, dir)
	if strings.HasSuffix(call, " build") && r.failBuilds > 0 {
		r.failBuilds--
		return []byte("docker is not running"), errors.New("exit status 1")
	}
	for sub, out := range r.fail {
		if strings.Contains(call, " "+sub) {
			return []byte(out), errors.New("exit status 1")
		}
	}
	return nil, nil
}

func (r *stackRunner) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *stackRunner) count(suffix string) int {
	n := 0
	for _, c := range r.got() {
		if strings.HasSuffix(c, suffix) {
			n++
		}
	}
	return n
}

func testStack(t *testing.T) (*Stack, *stackRunner) {
	t.Helper()
	setHubVersion(t, "v0.5.0")
	var c Config
	c.Hub.Port, c.Underlay.Port, c.Clients.Port, c.Storage.Port = 8080, 51820, 51821, 10809
	s := NewStack(t.TempDir(), newTestConfig(t, c))
	r := &stackRunner{}
	s.run = r.run
	return s, r
}

const composeArgs = "compose -p vm-manager -f compose.yaml --env-file .env"

func TestStackEnsureWritesProject(t *testing.T) {
	s, r := testStack(t)
	if err := s.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.DataDir, "stack")
	for _, f := range []string{"compose.yaml", "deploy/gateway.Dockerfile", "deploy/storage.Dockerfile", "deploy/gateway-start.sh"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Error(err)
		}
	}
	if fi, err := os.Stat(filepath.Join(dir, "deploy/gateway-start.sh")); err == nil && fi.Mode()&0o111 == 0 {
		t.Error("gateway-start.sh is not executable")
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	want := "VMM_VERSION=v0.5.0\nVMM_RELEASE_URL=\nHUB_PORT=8080\nUNDERLAY_PORT=51820\nCLIENTS_PORT=51821\nSTORAGE_PORT=10809\nDATA_DIR=" + s.DataDir + "\n"
	if string(env) != want {
		t.Errorf(".env:\n%s\nwant:\n%s", env, want)
	}
	calls := r.got()
	if len(calls) < 2 || calls[0] != composeArgs+" build" || calls[1] != composeArgs+" up -d --remove-orphans" {
		t.Errorf("calls = %q", calls)
	}
	for _, d := range r.dirs {
		if d != dir {
			t.Errorf("docker ran in %s, want %s", d, dir)
		}
	}
	if st := s.State(); st != (StackState{Managed: true, State: "running"}) {
		t.Errorf("state = %+v", st)
	}
}

func TestStackStorageProfile(t *testing.T) {
	s, r := testStack(t)
	s.Storage = func() bool { return true }
	if err := s.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{composeArgs + " --profile storage build", composeArgs + " --profile storage up -d --remove-orphans"}
	if got := r.got(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

func TestStackStorageOff(t *testing.T) {
	s, r := testStack(t)
	if err := s.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Compose keeps a container of a profile that is no longer selected, so it must be removed.
	want := composeArgs + " --profile storage rm --stop --force storage"
	if got := r.got(); len(got) != 3 || got[2] != want {
		t.Errorf("calls = %q, want a third call %q", got, want)
	}
}

func TestStackBuildFailureLeavesContainers(t *testing.T) {
	s, r := testStack(t)
	r.fail = map[string]string{"build": "boom"}
	if err := s.Ensure(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if r.count("up -d --remove-orphans") != 0 {
		t.Error("containers were replaced although the build failed")
	}
	if st := s.State(); st.State != "failed" || !strings.Contains(st.Error, "boom") {
		t.Errorf("state = %+v", st)
	}
}

func TestStackUpFailure(t *testing.T) {
	s, r := testStack(t)
	r.fail = map[string]string{"up": "Bind for 0.0.0.0:51820 failed: port is already allocated"}
	if err := s.Ensure(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if st := s.State(); st.State != "failed" || !strings.Contains(st.Error, "port is already allocated") {
		t.Errorf("state = %+v", st)
	}
	r.mu.Lock()
	r.fail = nil
	r.mu.Unlock()
	if err := s.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.State(); st.State != "running" || st.Error != "" {
		t.Errorf("state after the port was freed = %+v", st)
	}
}

func TestStackRunRetries(t *testing.T) {
	s, r := testStack(t)
	s.Retry = 10 * time.Millisecond
	r.failBuilds = 2
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	waitFor(t, "the stack to run after two failures", func() bool { return s.State().State == "running" })
	if n := r.count(" build"); n != 3 {
		t.Errorf("build calls = %d, want 3", n)
	}
	s.Kick()
	waitFor(t, "a build after the kick", func() bool { return r.count(" build") == 4 })
}

func TestStackNil(t *testing.T) {
	var s *Stack
	s.Kick()
	if st := s.State(); st != (StackState{}) {
		t.Errorf("state of no stack = %+v", st)
	}
}
