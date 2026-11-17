package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// integrationSources maps each supported integration to the only credential kind it accepts.
var integrationSources = map[string]string{"github": "pat", "claude": "setup-token", "linear": "api-key"}

type Integration struct {
	Name        string    `json:"name"`
	Source      string    `json:"source"`
	Connected   bool      `json:"connected"`
	Account     string    `json:"account"`
	ConnectedAt time.Time `json:"connectedAt"`
}

// Verifier checks a secret against the provider and returns the account it belongs to.
type Verifier func(ctx context.Context, secret string) (account string, err error)

type storedIntegration struct {
	Name        string    `json:"name"`
	Source      string    `json:"source"`
	Secret      string    `json:"secret"`
	Account     string    `json:"account"`
	ConnectedAt time.Time `json:"connectedAt"`
}

// IntegrationStore keeps provider secrets in a JSON file, in plain text like device keys.
type IntegrationStore struct {
	mu     sync.Mutex
	path   string
	verify map[string]Verifier
	items  map[string]storedIntegration
}

func LoadIntegrationStore(path string, verify map[string]Verifier) (*IntegrationStore, error) {
	s := &IntegrationStore{path: path, verify: verify, items: map[string]storedIntegration{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []storedIntegration
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, i := range list {
		s.items[i.Name] = i
	}
	return s, nil
}

func (s *IntegrationStore) List() []Integration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Integration{}
	for name, source := range integrationSources {
		i := Integration{Name: name, Source: source}
		if c, ok := s.items[name]; ok {
			i.Connected, i.Account, i.ConnectedAt = true, c.Account, c.ConnectedAt
		}
		out = append(out, i)
	}
	slices.SortFunc(out, func(a, b Integration) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (s *IntegrationStore) Set(ctx context.Context, name, source, secret string) (Integration, error) {
	want, ok := integrationSources[name]
	if !ok {
		return Integration{}, fmt.Errorf("%w: unknown integration %q", ErrInvalid, name)
	}
	if source != want {
		return Integration{}, fmt.Errorf("%w: %s takes source %q", ErrInvalid, name, want)
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return Integration{}, fmt.Errorf("%w: secret is required", ErrInvalid)
	}
	account, err := s.verify[name](ctx, secret)
	if err != nil {
		return Integration{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	c := storedIntegration{Name: name, Source: source, Secret: secret, Account: account, ConnectedAt: time.Now().UTC()}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.save(name, &c); err != nil {
		return Integration{}, err
	}
	s.items[name] = c
	return Integration{Name: name, Source: source, Connected: true, Account: account, ConnectedAt: c.ConnectedAt}, nil
}

func (s *IntegrationStore) Delete(name string) error {
	if _, ok := integrationSources[name]; !ok {
		return fmt.Errorf("%w: integration %q", ErrNotFound, name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[name]; !ok {
		return nil
	}
	if err := s.save(name, nil); err != nil {
		return err
	}
	delete(s.items, name)
	return nil
}

func (s *IntegrationStore) Secret(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.items[name]
	return c.Secret, ok
}

// save writes the items with name replaced by c, or removed when c is nil; memory is updated by the caller.
func (s *IntegrationStore) save(name string, c *storedIntegration) error {
	list := []storedIntegration{}
	for n, i := range s.items {
		if n != name {
			list = append(list, i)
		}
	}
	if c != nil {
		list = append(list, *c)
	}
	slices.SortFunc(list, func(a, b storedIntegration) int { return strings.Compare(a.Name, b.Name) })
	return saveJSON(s.path, list)
}

func DefaultVerifiers() map[string]Verifier {
	return map[string]Verifier{
		"github": verifyGitHub,
		"linear": verifyLinear,
		"claude": func(_ context.Context, secret string) (string, error) {
			if !strings.HasPrefix(secret, "sk-ant-oat") {
				return "", errors.New(`not a Claude setup token (expected the "sk-ant-oat" prefix from "claude setup-token")`)
			}
			return "", nil
		},
	}
}

var (
	githubUserURL = "https://api.github.com/user"
	linearURL     = "https://api.linear.app/graphql"
)

func verifyGitHub(ctx context.Context, secret string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, githubUserURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	var out struct {
		Login   string `json:"login"`
		Message string `json:"message"`
	}
	if err := callProvider(ctx, req, &out, func() string { return out.Message }); err != nil {
		return "", err
	}
	return out.Login, nil
}

func verifyLinear(ctx context.Context, secret string) (string, error) {
	body := `{"query":"{ viewer { name email } }"}`
	req, err := http.NewRequest(http.MethodPost, linearURL, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", secret)
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		Data struct {
			Viewer struct{ Email string } `json:"viewer"`
		} `json:"data"`
		Errors []struct{ Message string } `json:"errors"`
	}
	msg := func() string {
		if len(out.Errors) > 0 {
			return out.Errors[0].Message
		}
		return ""
	}
	if err := callProvider(ctx, req, &out, msg); err != nil {
		return "", err
	}
	return out.Data.Viewer.Email, nil
}

// callProvider decodes the JSON reply into out; a non-2xx status or a non-empty msg() is an error.
func callProvider(ctx context.Context, req *http.Request, out any, msg func() string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	json.NewDecoder(resp.Body).Decode(out)
	if m := msg(); m != "" {
		return errors.New(m)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("provider answered %s", resp.Status)
	}
	return nil
}
