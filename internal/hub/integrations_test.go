package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stubVerifiers(account string, err error) map[string]Verifier {
	v := func(context.Context, string) (string, error) { return account, err }
	return map[string]Verifier{"github": v, "claude": v, "linear": v}
}

func newTestIntegrations(t *testing.T) *IntegrationStore {
	t.Helper()
	s, err := LoadIntegrationStore(filepath.Join(t.TempDir(), "integrations.json"), stubVerifiers("octo", nil))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSetPersistsAndHidesSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "integrations.json")
	s, err := LoadIntegrationStore(path, stubVerifiers("octo", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(context.Background(), "github", "pat", "ghp_x"); err != nil {
		t.Fatal(err)
	}
	s, err = LoadIntegrationStore(path, stubVerifiers("", nil))
	if err != nil {
		t.Fatal(err)
	}
	var gh Integration
	for _, i := range s.List() {
		if i.Name == "github" {
			gh = i
		}
	}
	if !gh.Connected || gh.Account != "octo" {
		t.Fatalf("github = %+v", gh)
	}
	if sec, ok := s.Secret("github"); !ok || sec != "ghp_x" {
		t.Fatalf("secret = %q, %v", sec, ok)
	}
	b, _ := json.Marshal(s.List())
	if strings.Contains(string(b), "ghp_x") {
		t.Fatalf("secret leaked: %s", b)
	}
}

func TestListAlwaysHasThreeSorted(t *testing.T) {
	l := newTestIntegrations(t).List()
	if len(l) != 3 || l[0].Name != "claude" || l[1].Name != "github" || l[2].Name != "linear" || l[0].Connected {
		t.Fatalf("list = %+v", l)
	}
}

func TestSetTrimsAndRejectsEmpty(t *testing.T) {
	s := newTestIntegrations(t)
	if _, err := s.Set(context.Background(), "github", "pat", "  ghp_x\n"); err != nil {
		t.Fatal(err)
	}
	if sec, _ := s.Secret("github"); sec != "ghp_x" {
		t.Fatalf("secret = %q", sec)
	}
	if _, err := s.Set(context.Background(), "github", "pat", "  \n"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetRejectsWrongSourceAndName(t *testing.T) {
	s := newTestIntegrations(t)
	if _, err := s.Set(context.Background(), "github", "api-key", "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("source err = %v", err)
	}
	if _, err := s.Set(context.Background(), "slack", "pat", "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("name err = %v", err)
	}
}

func TestSetVerifierError(t *testing.T) {
	s, err := LoadIntegrationStore(filepath.Join(t.TempDir(), "i.json"), stubVerifiers("", errors.New("Bad credentials")))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Set(context.Background(), "github", "pat", "x")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := s.Secret("github"); ok {
		t.Fatal("secret stored despite verifier error")
	}
}

func TestDeleteDisconnects(t *testing.T) {
	s := newTestIntegrations(t)
	if _, err := s.Set(context.Background(), "github", "pat", "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("github"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Secret("github"); ok {
		t.Fatal("secret still there")
	}
	for _, i := range s.List() {
		if i.Name == "github" && i.Connected {
			t.Fatal("still connected")
		}
	}
	if err := s.Delete("github"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if err := s.Delete("slack"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetWithoutVerifier(t *testing.T) {
	s, err := LoadIntegrationStore(filepath.Join(t.TempDir(), "i.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(context.Background(), "github", "pat", "x"); err == nil {
		t.Fatal("expected an error without a verifier")
	}
}

func TestListOmitsUnsetConnectedAt(t *testing.T) {
	s := newTestIntegrations(t)
	if _, err := s.Set(context.Background(), "github", "pat", "x"); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s.List())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), `"connectedAt"`); n != 1 || strings.Contains(string(b), "0001-01-01") {
		t.Fatalf("connectedAt %d times: %s", n, b)
	}
}

func TestClaudePrefix(t *testing.T) {
	v := DefaultVerifiers()["claude"]
	if _, err := v(context.Background(), "sk-ant-oat01-abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := v(context.Background(), "abc"); err == nil {
		t.Fatal("expected error")
	}
}

func stubProvider(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	oldGH, oldLin := githubUserURL, linearURL
	githubUserURL, linearURL = srv.URL, srv.URL
	t.Cleanup(func() { githubUserURL, linearURL = oldGH, oldLin })
}

func TestProviderVerifiers(t *testing.T) {
	cases := []struct {
		name, verifier string
		status         int
		body           string
		account, errIn string
	}{
		{"github ok", "github", 200, `{"login":"octo"}`, "octo", ""},
		{"github 401", "github", 401, `{"message":"Bad credentials"}`, "", "Bad credentials"},
		{"github html 200", "github", 200, `<html>portal</html>`, "", "unreadable"},
		{"github 500 no body", "github", 500, ``, "", "500"},
		{"linear ok", "linear", 200, `{"data":{"viewer":{"Email":"a@b.c"}}}`, "a@b.c", ""},
		{"linear errors", "linear", 200, `{"errors":[{"Message":"Authentication required"}]}`, "", "Authentication required"},
		{"linear data null", "linear", 200, `{"data":null}`, "", "no viewer email"},
		{"linear html 200", "linear", 200, `<html>portal</html>`, "", "unreadable"},
		{"linear 401", "linear", 401, `{}`, "", "401"},
		{"github body over 1 MiB", "github", 200, `{"login":"octo","pad":"` + strings.Repeat("x", 1<<20) + `"}`, "", "unreadable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubProvider(t, c.status, c.body)
			got, err := DefaultVerifiers()[c.verifier](context.Background(), "secret")
			if c.errIn == "" {
				if err != nil || got != c.account {
					t.Fatalf("got %q, %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.errIn) {
				t.Fatalf("err = %v, want %q", err, c.errIn)
			}
		})
	}
}

func TestProviderTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	oldURL, oldTimeout := githubUserURL, providerTimeout
	githubUserURL, providerTimeout = srv.URL, 50*time.Millisecond
	t.Cleanup(func() { githubUserURL, providerTimeout = oldURL, oldTimeout })
	if _, err := DefaultVerifiers()["github"](context.Background(), "secret"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}
