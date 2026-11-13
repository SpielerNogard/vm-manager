package hub

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vm-manager/deploy"
	"vm-manager/internal/version"
)

const stackErrorTail = 2000

type StackState struct {
	Managed bool `json:"managed"`
	// State is "building", "running" or "failed".
	State string `json:"state,omitempty"`
	Error string `json:"error,omitempty"`
}

// Stack runs the gateway and the optional storage server as a compose project of a hub that runs on the host.
type Stack struct {
	DataDir    string
	ReleaseURL string
	Storage    func() bool
	Retry      time.Duration

	cfg  *ConfigStore
	run  func(ctx context.Context, dir string, args ...string) ([]byte, error)
	kick chan struct{}

	mu    sync.Mutex
	state StackState
}

func NewStack(dataDir string, cfg *ConfigStore) *Stack {
	return &Stack{DataDir: dataDir, Retry: time.Minute, cfg: cfg, run: runDocker, kick: make(chan struct{}, 1),
		state: StackState{Managed: true, State: "building"}}
}

func runDocker(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func (s *Stack) State() StackState {
	if s == nil {
		return StackState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Stack) Kick() {
	if s == nil {
		return
	}
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run brings the stack up now, again after every Kick, and every Retry while the last attempt failed.
func (s *Stack) Run(ctx context.Context) {
	for {
		var retry <-chan time.Time
		if err := s.Ensure(ctx); err != nil {
			retry = time.After(s.Retry)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		case <-retry:
		}
	}
}

// Ensure writes the project out, builds and starts it; building first leaves the running containers alone if the build fails.
func (s *Stack) Ensure(ctx context.Context) error {
	s.set("building", "")
	err := s.ensure(ctx)
	if err != nil {
		s.set("failed", err.Error())
		return err
	}
	s.set("running", "")
	return nil
}

func (s *Stack) set(state, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = StackState{Managed: true, State: state, Error: msg}
}

func (s *Stack) ensure(ctx context.Context) error {
	dir := filepath.Join(s.DataDir, "stack")
	if err := s.writeProject(dir); err != nil {
		return err
	}
	storage := s.Storage != nil && s.Storage()
	compose := func(profile bool, args ...string) error {
		base := []string{"compose", "-p", "vm-manager", "-f", "compose.yaml", "--env-file", ".env"}
		if profile {
			base = append(base, "--profile", "storage")
		}
		out, err := s.run(ctx, dir, append(base, args...)...)
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if len(msg) > stackErrorTail {
				msg = msg[len(msg)-stackErrorTail:]
			}
			return fmt.Errorf("docker compose %s: %w\n%s", args[0], err, msg)
		}
		return nil
	}
	if err := compose(storage, "build"); err != nil {
		return err
	}
	if err := compose(storage, "up", "-d", "--remove-orphans"); err != nil {
		return err
	}
	if storage {
		return nil
	}
	// Compose keeps a container whose profile is no longer selected.
	return compose(true, "rm", "--stop", "--force", "storage")
}

func (s *Stack) writeProject(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "deploy"), 0o755); err != nil {
		return err
	}
	for name, to := range map[string]string{
		"stack.compose.yaml": "compose.yaml",
		"gateway.Dockerfile": "deploy/gateway.Dockerfile",
		"storage.Dockerfile": "deploy/storage.Dockerfile",
		"gateway-start.sh":   "deploy/gateway-start.sh",
	} {
		b, err := deploy.Stack.ReadFile(name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, to), b, 0o755); err != nil {
			return err
		}
	}
	c := s.cfg.Get()
	env := fmt.Sprintf("VMM_VERSION=%s\nVMM_RELEASE_URL=%s\nHUB_PORT=%d\nUNDERLAY_PORT=%d\nCLIENTS_PORT=%d\nSTORAGE_PORT=%d\nDATA_DIR=%s\n",
		version.Version, s.ReleaseURL, c.Hub.Port, c.Underlay.Port, c.Clients.Port, c.Storage.Port, s.DataDir)
	return os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o644)
}
