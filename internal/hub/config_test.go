package hub

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hub.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func newTestConfig(t *testing.T, c Config) *ConfigStore {
	t.Helper()
	return &ConfigStore{path: filepath.Join(t.TempDir(), "hub.json"), cfg: c}
}

func TestLoadOrCreateConfigCreates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hub.json")
	s, err := LoadOrCreateConfig(p, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	c := s.Get()
	if len(c.Token) < 32 || strings.Trim(c.Token, "0123456789abcdef") != "" {
		t.Errorf("token = %q", c.Token)
	}
	if c.Clients.Endpoint != "" || c.Listen != ":8080" || c.GuestMTU != 1300 || c.Hub.Port != 8080 || c.Underlay.Port != 51820 || c.Clients.Port != 51821 {
		t.Errorf("config = %+v", c)
	}
	s2, err := LoadOrCreateConfig(p, envOf(nil))
	if err != nil || s2.Get() != c {
		t.Errorf("reload = %+v, %v", s2.Get(), err)
	}
}

func TestLoadOrCreateConfigKeepsExisting(t *testing.T) {
	p := writeFile(t, `{"token":"abcdef0123456789abcd","clients":{"endpoint":"x.lan","port":51821},"underlay":{"port":51820}}`)
	s, err := LoadOrCreateConfig(p, envOf(map[string]string{"HUB_PORT": "18080"}))
	if err != nil {
		t.Fatal(err)
	}
	c := s.Get()
	if c.Token != "abcdef0123456789abcd" || c.Clients.Endpoint != "x.lan" || c.Hub.Port != 18080 {
		t.Errorf("config = %+v", c)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"hub"`) || !strings.Contains(string(b), "18080") {
		t.Errorf("file = %s", b)
	}
}

func TestLoadOrCreateConfigEnvPorts(t *testing.T) {
	p := writeFile(t, `{"token":"t","clients":{"port":1},"underlay":{"port":2}}`)
	s, err := LoadOrCreateConfig(p, envOf(map[string]string{"UNDERLAY_PORT": "52820", "CLIENTS_PORT": "52821"}))
	if err != nil {
		t.Fatal(err)
	}
	if c := s.Get(); c.Underlay.Port != 52820 || c.Clients.Port != 52821 {
		t.Errorf("config = %+v", c)
	}
	for _, v := range []string{"abc", "0", "65536"} {
		if _, err := LoadOrCreateConfig(p, envOf(map[string]string{"HUB_PORT": v})); err == nil || !strings.Contains(err.Error(), "HUB_PORT") {
			t.Errorf("HUB_PORT=%s: err = %v", v, err)
		}
	}
}

func TestLoadOrCreateConfigBareHubPort(t *testing.T) {
	s, err := LoadOrCreateConfig(writeFile(t, `{"listen":":9090","token":"t"}`), envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c := s.Get(); c.Hub.Port != 9090 {
		t.Errorf("config = %+v", c)
	}
}

func TestLoadOrCreateConfigBareHubPortWrittenBack(t *testing.T) {
	p := writeFile(t, `{"listen":":9090","token":"t"}`)
	if _, err := LoadOrCreateConfig(p, envOf(nil)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "9090") || !strings.Contains(string(b), `"hub"`) {
		t.Errorf("file = %s", b)
	}
}

func TestLoadOrCreateConfigNoPortInListenDefaultsAndSettles(t *testing.T) {
	p := writeFile(t, `{"listen":"localhost","token":"t"}`)
	s, err := LoadOrCreateConfig(p, envOf(nil))
	if err != nil || s.Get().Hub.Port != 8080 {
		t.Fatalf("config = %+v, %v", s.Get(), err)
	}
	first, _ := os.Stat(p)
	if err := os.Chtimes(p, first.ModTime().Add(-time.Hour), first.ModTime().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(p)
	if _, err := LoadOrCreateConfig(p, envOf(nil)); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(p)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("config rewritten on second start")
	}
}

func TestLoadOrCreateConfigErrors(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"missing token": {`{}`, "token"},
		"bad json":      {`{`, "fix or delete it"},
		"empty file":    {``, "fix or delete it"},
	} {
		_, err := LoadOrCreateConfig(writeFile(t, tc.body), envOf(nil))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestConfigStoreUpdateAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hub.json")
	s, err := LoadOrCreateConfig(p, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Update(func(c *Config) error { c.Clients.Endpoint = "vmm.lan"; return nil })
	if err != nil || c.Clients.Endpoint != "vmm.lan" || s.Get().Clients.Endpoint != "vmm.lan" {
		t.Fatalf("update = %+v, %v", c, err)
	}
	if s2, err := LoadOrCreateConfig(p, envOf(nil)); err != nil || s2.Get().Clients.Endpoint != "vmm.lan" {
		t.Fatalf("reload = %+v, %v", s2, err)
	}

	before := s.Get()
	if _, err := s.Update(func(c *Config) error { c.Clients.Endpoint = "other"; return errors.New("nope") }); err == nil || s.Get() != before {
		t.Errorf("failing f: err = %v, config = %+v", err, s.Get())
	}

	if os.Getuid() == 0 {
		return
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if _, err := s.Update(func(c *Config) error { c.Clients.Endpoint = "other"; return nil }); err == nil || s.Get() != before {
		t.Errorf("read-only dir: err = %v, config = %+v", err, s.Get())
	}
}

func TestLiveTokenCheck(t *testing.T) {
	cfg := newTestConfig(t, Config{Token: "old"})
	r := NewRegistry(newTestNet(t), newTestStore(t), newTestDisks(t), cfg)
	srv := httptest.NewServer(NodeHandler(r, func() string { return cfg.Get().Token }))
	t.Cleanup(srv.Close)

	if _, err := cfg.Update(func(c *Config) error { c.Token = "new"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := dialNode(t, srv.URL, "old", testReg); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("old token: err = %v", err)
	}
	if _, err := dialNode(t, srv.URL, "new", testReg); err != nil {
		t.Fatalf("new token: %v", err)
	}
	waitFor(t, "node online", func() bool { ns := r.Nodes(); return len(ns) == 1 && ns[0].State == "online" })
}

func TestStorageEndpointDefaultsToClientsEndpoint(t *testing.T) {
	p := writeFile(t, `{"token":"abcdef0123456789abcd","clients":{"endpoint":"fd00::1"}}`)
	s, err := LoadOrCreateConfig(p, envOf(map[string]string{"STORAGE_PORT": "10900"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Get().StorageEndpoint(); got != "[fd00::1]:10900" {
		t.Fatalf("derived = %q", got)
	}
	c := s.Get()
	c.Storage.Endpoint = "st:1"
	if got := c.StorageEndpoint(); got != "st:1" {
		t.Fatalf("explicit = %q", got)
	}
	c.Storage.Endpoint, c.Clients.Endpoint = "", ""
	if got := c.StorageEndpoint(); got != "" {
		t.Fatalf("before setup = %q", got)
	}
	if _, err := LoadOrCreateConfig(p, envOf(map[string]string{"STORAGE_PORT": "0"})); err == nil {
		t.Fatal("invalid STORAGE_PORT accepted")
	}
}

func TestMigrateStorageEndpointKeepsAdvertisedOne(t *testing.T) {
	cases := []struct {
		name, file, old, want string
	}{
		{"carried over", `{"token":"abcdef0123456789abcd","clients":{"endpoint":"hub.lan"}}`, "100.1.2.3:10809", "100.1.2.3:10809"},
		{"same as derived", `{"token":"abcdef0123456789abcd","clients":{"endpoint":"hub.lan"}}`, "hub.lan:10809", ""},
		{"already migrated", `{"token":"abcdef0123456789abcd","clients":{"endpoint":"hub.lan"},"storage":{"port":10809}}`, "100.1.2.3:10809", ""},
		{"no old endpoint", `{"token":"abcdef0123456789abcd","clients":{"endpoint":"hub.lan"}}`, "", ""},
	}
	for _, tc := range cases {
		s, err := LoadOrCreateConfig(writeFile(t, tc.file), envOf(nil))
		if err != nil {
			t.Fatal(err)
		}
		disks := newTestDisks(t)
		if err := disks.SetEndpoint(tc.old); err != nil {
			t.Fatal(err)
		}
		if err := MigrateStorageEndpoint(s, disks); err != nil {
			t.Fatal(err)
		}
		if got := s.Get().Storage.Endpoint; got != tc.want {
			t.Errorf("%s: storage.endpoint = %q, want %q", tc.name, got, tc.want)
		}
	}
}
