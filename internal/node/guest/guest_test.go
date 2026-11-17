package guest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func requireShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("guest scripts target Ubuntu")
	}
	for _, bin := range []string{"sh", "curl", "sha256sum"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
}

type reply struct {
	code int
	body string
}

type tokenService struct {
	mu      sync.Mutex
	replies map[string]reply
	*httptest.Server
}

func newTokenService(t *testing.T, replies map[string]reply) *tokenService {
	s := &tokenService{replies: replies}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		rp, ok := s.replies[strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), "/token")]
		s.mu.Unlock()
		if !ok {
			rp = reply{code: http.StatusNotFound}
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(rp.code)
		w.Write([]byte(rp.body))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *tokenService) set(name string, rp reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies[name] = rp
}

func run(t *testing.T, env []string, stdin string, script string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if stderr.Len() > 0 {
		t.Errorf("%s wrote to stderr: %q", script, stderr.String())
	}
	return string(out), err
}

func TestCredentialHelperGitHub(t *testing.T) {
	requireShell(t)
	svc := newTokenService(t, map[string]reply{"github": {200, "ghp_x"}})
	out, err := run(t, []string{"VMM_TOKENS_URL=" + svc.URL}, "protocol=https\nhost=github.com\n\n", "git-credential-vmm", "get")
	if err != nil || !strings.Contains(out, "username=x-access-token\n") || !strings.Contains(out, "password=ghp_x\n") {
		t.Fatalf("output %q, %v", out, err)
	}
}

func TestCredentialHelperOtherHost(t *testing.T) {
	requireShell(t)
	svc := newTokenService(t, map[string]reply{"github": {200, "ghp_x"}})
	out, err := run(t, []string{"VMM_TOKENS_URL=" + svc.URL}, "protocol=https\nhost=gitlab.com\n\n", "git-credential-vmm", "get")
	if err != nil || out != "" {
		t.Fatalf("output %q, %v; want empty, exit 0", out, err)
	}
}

func TestHelpersQuietWhenServiceDown(t *testing.T) {
	requireShell(t)
	svc := newTokenService(t, nil)
	svc.Close()
	start := time.Now()
	out, err := run(t, []string{"VMM_TOKENS_URL=" + svc.URL}, "protocol=https\nhost=github.com\n\n", "git-credential-vmm", "get")
	if err != nil || out != "" {
		t.Fatalf("output %q, %v; want empty, exit 0", out, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

type syncEnv struct {
	creds, state, claudeLog, claudeHome, gitconfig string
	env                                 []string
}

func newSyncEnv(t *testing.T, url string) syncEnv {
	dir := t.TempDir()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	e := syncEnv{creds: filepath.Join(dir, "home", ".claude", ".credentials.json"), state: filepath.Join(dir, "state"), claudeLog: filepath.Join(dir, "claude.log"), claudeHome: filepath.Join(dir, "claude.home"), gitconfig: filepath.Join(dir, "gitconfig")}
	stub := filepath.Join(dir, "claude")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"$*\" >> '"+e.claudeLog+"'\necho \"$HOME\" > '"+e.claudeHome+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.env = []string{"VMM_TOKENS_URL=" + url, "VMM_STATE_DIR=" + e.state, "VMM_CLAUDE_CREDENTIALS=" + e.creds,
		"VMM_CLAUDE_BIN=" + stub, "VMM_SKIP_INSTALL=1", "VMM_USER=" + u.Username, "VMM_GITCONFIG=" + e.gitconfig, "HOME=" + filepath.Join(dir, "home")}
	return e
}

func (e syncEnv) sync(t *testing.T) {
	t.Helper()
	if out, err := run(t, e.env, "", "vmm-sync"); err != nil || out != "" {
		t.Fatalf("vmm-sync: output %q, %v", out, err)
	}
}

func (e syncEnv) writeUserCreds(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.creds), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.creds, []byte(`{"user":"login"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e syncEnv) wantCreds(t *testing.T, want string) {
	t.Helper()
	if b, err := os.ReadFile(e.creds); err != nil || string(b) != want {
		t.Fatalf("credentials = %q, %v; want %q", b, err, want)
	}
}

func TestClaudeSyncWrites(t *testing.T) {
	requireShell(t)
	svc := newTokenService(t, map[string]reply{"claude": {200, "sk-ant-oat01-x"}})
	e := newSyncEnv(t, svc.URL)
	e.sync(t)
	b, err := os.ReadFile(e.creds)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ClaudeAiOauth map[string]any `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	if v, ok := got.ClaudeAiOauth["refreshToken"]; got.ClaudeAiOauth["accessToken"] != "sk-ant-oat01-x" || !ok || v != nil {
		t.Fatalf("credentials = %s", b)
	}
	if st, _ := os.Stat(e.creds); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode())
	}
	if _, err := os.Stat(filepath.Join(e.state, "claude-credentials.sha256")); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(e.creds)); len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestClaudeSyncKeepsUserLogin(t *testing.T) {
	requireShell(t)
	svc := newTokenService(t, map[string]reply{"claude": {200, "sk-ant-oat01-x"}})
	e := newSyncEnv(t, svc.URL)
	e.writeUserCreds(t)
	e.sync(t)
	e.wantCreds(t, `{"user":"login"}`)
}

func TestClaudeSyncRemovesOwnFileOn404(t *testing.T) {
	requireShell(t)
	svc := newTokenService(t, map[string]reply{"claude": {200, "sk-ant-oat01-x"}})
	e := newSyncEnv(t, svc.URL)
	e.sync(t)
	svc.set("claude", reply{code: 404})
	e.sync(t)
	if _, err := os.Stat(e.creds); !os.IsNotExist(err) {
		t.Fatalf("own credentials not removed: %v", err)
	}

	e = newSyncEnv(t, svc.URL)
	e.writeUserCreds(t)
	e.sync(t)
	e.wantCreds(t, `{"user":"login"}`)
}

func TestClaudeSyncUnreachableNoChange(t *testing.T) {
	requireShell(t)
	svc := newTokenService(t, map[string]reply{"claude": {200, "sk-ant-oat01-x"}})
	e := newSyncEnv(t, svc.URL)
	e.sync(t)
	before, _ := os.ReadFile(e.creds)
	svc.Close()
	e.sync(t)
	e.wantCreds(t, string(before))
}

func TestLinearSync(t *testing.T) {
	requireShell(t)
	svc := newTokenService(t, map[string]reply{"linear": {200, "lin_api_a"}})
	e := newSyncEnv(t, svc.URL)
	e.sync(t)
	e.sync(t)
	svc.set("linear", reply{200, "lin_api_b"})
	e.sync(t)
	svc.set("linear", reply{code: 403})
	e.sync(t)
	b, _ := os.ReadFile(e.claudeLog)
	add := "mcp add --transport http --scope user linear https://mcp.linear.app/mcp --header Authorization: Bearer "
	rm := "mcp remove --scope user linear"
	want := add + "lin_api_a\n" + rm + "\n" + add + "lin_api_b\n" + rm + "\n"
	if string(b) != want {
		t.Fatalf("claude calls:\n%s\nwant:\n%s", b, want)
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if home, _ := os.ReadFile(e.claudeHome); string(home) != u.HomeDir+"\n" {
		t.Fatalf("claude ran with HOME %q, want %q", home, u.HomeDir)
	}
}

const vmmInclude = "[include]\n\tpath = /etc/vmm/gitconfig\n"

func (e syncEnv) wantGitconfig(t *testing.T, want string) {
	t.Helper()
	b, err := os.ReadFile(e.gitconfig)
	if err != nil || string(b) != want {
		t.Fatalf("gitconfig = %q, %v; want %q", b, err, want)
	}
	if _, err := exec.LookPath("git"); err == nil {
		cmd := exec.Command("git", "config", "-f", e.gitconfig, "--list")
		cmd.Dir = filepath.Dir(e.gitconfig)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config --list: %v\n%s", err, out)
		}
	}
}

func TestGitHubIncludeFollowsToken(t *testing.T) {
	requireShell(t)
	svc := newTokenService(t, map[string]reply{"github": {200, "ghp_x"}})
	e := newSyncEnv(t, svc.URL)
	own := "[user]\n\tname = me\n[include]\n\tpath = /home/me/.gitextra"
	if err := os.WriteFile(e.gitconfig, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	e.sync(t)
	e.wantGitconfig(t, own+"\n"+vmmInclude)
	e.sync(t)
	e.wantGitconfig(t, own+"\n"+vmmInclude)

	svc.set("github", reply{code: 404})
	e.sync(t)
	e.wantGitconfig(t, own+"\n")

	svc.set("github", reply{200, "ghp_x"})
	e.sync(t)
	svc.set("github", reply{code: 403})
	e.sync(t)
	e.wantGitconfig(t, own+"\n")
}

func TestGitHubIncludeUnreachableNoChange(t *testing.T) {
	requireShell(t)
	svc := newTokenService(t, map[string]reply{"github": {200, "ghp_x"}})
	e := newSyncEnv(t, svc.URL)
	e.sync(t)
	e.wantGitconfig(t, vmmInclude)
	if st, _ := os.Stat(e.gitconfig); st.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want world-readable", st.Mode())
	}
	svc.Close()
	e.sync(t)
	e.wantGitconfig(t, vmmInclude)
}

func TestGitHubIncludeNotCreatedWithoutToken(t *testing.T) {
	requireShell(t)
	svc := newTokenService(t, map[string]reply{})
	e := newSyncEnv(t, svc.URL)
	e.sync(t)
	if _, err := os.Stat(e.gitconfig); !os.IsNotExist(err) {
		t.Fatalf("gitconfig created without a token: %v", err)
	}
}

func TestSyncIgnoresPreviousBody(t *testing.T) {
	requireShell(t)
	svc := newTokenService(t, map[string]reply{"claude": {200, "sk-ant-oat01-x"}, "linear": {200, ""}})
	e := newSyncEnv(t, svc.URL)
	e.sync(t)
	if b, _ := os.ReadFile(e.claudeLog); len(b) != 0 {
		t.Fatalf("empty linear reply used a previous body: %s", b)
	}
}

func TestClaudeSyncIgnoresPlantedSymlink(t *testing.T) {
	requireShell(t)
	svc := newTokenService(t, map[string]reply{"claude": {200, "sk-ant-oat01-x"}})
	e := newSyncEnv(t, svc.URL)
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(e.creds), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, e.creds+".vmm-new"); err != nil {
		t.Fatal(err)
	}
	e.sync(t)
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatalf("symlink target overwritten: %q", b)
	}
	if st, err := os.Lstat(e.creds); err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		t.Fatalf("credentials not a 600 regular file: %v, %v", st, err)
	}
}

func TestScriptInstallsHelpers(t *testing.T) {
	requireShell(t)
	s := Script()
	for _, want := range []string{"/usr/local/bin/git-credential-vmm", "/usr/local/bin/gh", "/usr/local/bin/vmm-sync", "/etc/profile.d/vmm-linear.sh", "OnUnitActiveSec=5min", "TimeoutStartSec=4min"} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q", want)
		}
	}
	if strings.Contains(s, ">>/etc/gitconfig") {
		t.Error("script includes /etc/vmm/gitconfig unconditionally; vmm-sync manages it")
	}
	if out, err := exec.Command("sh", "-n", "-c", s).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
}
