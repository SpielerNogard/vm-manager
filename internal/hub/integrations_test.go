package hub

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
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
	s, _ := LoadIntegrationStore(path, stubVerifiers("octo", nil))
	if _, err := s.Set(context.Background(), "github", "pat", "ghp_x"); err != nil {
		t.Fatal(err)
	}
	s, err := LoadIntegrationStore(path, stubVerifiers("", nil))
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
	s, _ := LoadIntegrationStore(filepath.Join(t.TempDir(), "i.json"), stubVerifiers("", errors.New("Bad credentials")))
	_, err := s.Set(context.Background(), "github", "pat", "x")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := s.Secret("github"); ok {
		t.Fatal("secret stored despite verifier error")
	}
}

func TestDeleteDisconnects(t *testing.T) {
	s := newTestIntegrations(t)
	s.Set(context.Background(), "github", "pat", "x")
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

func TestClaudePrefix(t *testing.T) {
	v := DefaultVerifiers()["claude"]
	if _, err := v(context.Background(), "sk-ant-oat01-abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := v(context.Background(), "abc"); err == nil {
		t.Fatal("expected error")
	}
}
