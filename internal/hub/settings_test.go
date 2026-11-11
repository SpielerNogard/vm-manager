package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func settingsServer(t *testing.T, c Config) (*httptest.Server, *ConfigStore, *Registry) {
	t.Helper()
	cfg := newTestConfig(t, c)
	nm := newTestNet(t)
	r := NewRegistry(nm, newTestStore(t), cfg)
	keys, err := LoadKeyStore(t.TempDir() + "/keys.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(APIHandler(r, keys, nm, cfg))
	t.Cleanup(srv.Close)
	return srv, cfg, r
}

func freshConfig() Config {
	c := testAPIConfig()
	c.Token, c.Clients.Endpoint = "0123456789abcdef", ""
	c.Hub.Port = 8080
	return c
}

func TestSettingsGetFresh(t *testing.T) {
	srv, _, _ := settingsServer(t, freshConfig())
	s, body := doJSON(t, "GET", srv.URL+"/api/settings", "")
	var out struct {
		Token         string `json:"token"`
		SetupComplete bool   `json:"setupComplete"`
		Underlay      struct{ Port int }
		Clients       struct{ Port int }
		Hub           struct{ Port int }
	}
	json.Unmarshal(body, &out)
	if s != 200 || out.SetupComplete || out.Token == "" || out.Underlay.Port != 51820 || out.Clients.Port != 51821 || out.Hub.Port != 8080 {
		t.Fatalf("status = %d body = %s", s, body)
	}
}

func TestSettingsPutValidation(t *testing.T) {
	srv, cfg, _ := settingsServer(t, freshConfig())
	before := cfg.Get()
	file, _ := os.ReadFile(cfg.path)
	for _, b := range []string{
		`{"clients":{"endpoint":"http://x"}}`, `{"clients":{"endpoint":"x:51821"}}`, `{"clients":{"endpoint":"a b"}}`,
		`{"token":""}`, `{"token":"short"}`, `{"token":"has space 0123456789"}`, `{"guestMTU":100}`, `{"guestMTU":9001}`,
		`{"clients":{"endpoint":""}}`, `{"clients":{"endpoint":"-"}}`, `{"clients":{"endpoint":"a..b"}}`, `{"clients":{"endpoint":"*"}}`,
		`{"clients":{"endpoint":"a=b"}}`, `{"clients":{"endpoint":"a,b"}}`, `{"clients":{"endpoint":"[::1]"}}`, `{"clients":{"endpoint":"-a.b"}}`,
		`{"clients":{"endpoint":"a-.b"}}`, `{"clients":{"endpoint":".a"}}`, `{"clients":{"endpoint":"a."}}`, `{"clients":{"endpoint":"fe80::1%eth0"}}`,
		`{"listen":":9"}`, `{"clients":{"port":1}}`, `{"hub":{"port":1}}`,
	} {
		if s, body := doJSON(t, "PUT", srv.URL+"/api/settings", b); s != 400 {
			t.Errorf("%s: status = %d body = %s", b, s, body)
		}
	}
	after, _ := os.ReadFile(cfg.path)
	if cfg.Get() != before || string(after) != string(file) {
		t.Fatalf("config changed: %+v", cfg.Get())
	}
}

func TestSettingsPutEndpointCompletesSetup(t *testing.T) {
	srv, _, _ := settingsServer(t, freshConfig())
	s, body := doJSON(t, "PUT", srv.URL+"/api/settings", `{"clients":{"endpoint":"100.114.108.108"},"guestMTU":1400}`)
	if s != 200 || !strings.Contains(string(body), `"setupComplete":true`) || !strings.Contains(string(body), `"guestMTU":1400`) {
		t.Fatalf("status = %d body = %s", s, body)
	}
	_, created := doJSON(t, "POST", srv.URL+"/api/devices", `{"name":"laptop"}`)
	if !strings.Contains(string(created), "Endpoint = 100.114.108.108:51821") {
		t.Fatalf("device = %s", created)
	}
}

func TestSettingsTokenRegenerate(t *testing.T) {
	srv, cfg, r := settingsServer(t, freshConfig())
	nodeSrv := httptest.NewServer(NodeHandler(r, func() string { return cfg.Get().Token }))
	t.Cleanup(nodeSrv.Close)
	old := cfg.Get().Token
	s, body := doJSON(t, "POST", srv.URL+"/api/settings/token", "")
	var out struct{ Token string }
	json.Unmarshal(body, &out)
	if s != 200 || out.Token == "" || out.Token == old || cfg.Get().Token != out.Token {
		t.Fatalf("status = %d body = %s", s, body)
	}
	if _, err := dialNode(t, nodeSrv.URL, old, testReg); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("old token: err = %v", err)
	}
	n, err := dialNode(t, nodeSrv.URL, out.Token, testReg)
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	waitFor(t, "node online", func() bool { ns := r.Nodes(); return len(ns) == 1 && ns[0].Online })
	n.sess.Close()
	waitFor(t, "node offline", func() bool { ns := r.Nodes(); return len(ns) == 1 && !ns[0].Online })
}

func TestSetupGuard(t *testing.T) {
	srv, cfg, r := settingsServer(t, freshConfig())
	nodeSrv := httptest.NewServer(NodeHandler(r, func() string { return cfg.Get().Token }))
	t.Cleanup(nodeSrv.Close)
	status := func(method, path, body string) int {
		s, _ := doJSON(t, method, srv.URL+path, body)
		return s
	}
	if s := status("POST", "/api/keys", `{}`); s != 409 {
		t.Errorf("POST keys = %d", s)
	}
	if s := status("POST", "/api/vms", `{}`); s != 409 {
		t.Errorf("POST vms = %d", s)
	}
	if s := status("GET", "/api/vms", ""); s != 200 {
		t.Errorf("GET vms = %d", s)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/gateway/state", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Get().Token)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Errorf("gateway state: %v %v", resp, err)
	}
	if n, err := dialNode(t, nodeSrv.URL, cfg.Get().Token, testReg); err != nil {
		t.Errorf("node connect: %v", err)
	} else {
		waitFor(t, "node online", func() bool { ns := r.Nodes(); return len(ns) == 1 && ns[0].Online })
		defer func() {
			n.sess.Close()
			waitFor(t, "node offline", func() bool { ns := r.Nodes(); return len(ns) == 1 && !ns[0].Online })
		}()
	}
	if s := status("DELETE", "/api/keys/k1", ""); s != 409 {
		t.Errorf("DELETE keys = %d", s)
	}
	if s := status("POST", "/api/settings/token", ""); s != 200 {
		t.Errorf("POST settings/token = %d", s)
	}
	if s := status("PUT", "/api/settings", `{"clients":{"endpoint":"vmm.lan"}}`); s != 200 {
		t.Fatalf("PUT settings = %d", s)
	}
	if s := status("POST", "/api/keys", `{}`); s == 409 {
		t.Errorf("POST keys still locked")
	}
}

func TestSettingsIPv6EndpointInDeviceConfig(t *testing.T) {
	srv, _, _ := settingsServer(t, freshConfig())
	if s, body := doJSON(t, "PUT", srv.URL+"/api/settings", `{"clients":{"endpoint":"fd00::1"}}`); s != 200 {
		t.Fatalf("PUT = %d %s", s, body)
	}
	_, created := doJSON(t, "POST", srv.URL+"/api/devices", `{"name":"laptop"}`)
	if !strings.Contains(string(created), "Endpoint = [fd00::1]:51821") {
		t.Fatalf("device = %s", created)
	}
}
