package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vm-manager/internal/proto"
)

func apiServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _, _ := apiServerWith(t, testAPIConfig())
	return srv
}

func testAPIConfig() Config {
	var cfg Config
	cfg.Token, cfg.Underlay.Port, cfg.Clients.Endpoint, cfg.Clients.Port, cfg.GuestMTU = "tok", 51820, "vmm.lan", 51821, 1300
	return cfg
}

func apiServerWith(t *testing.T, cfg Config) (*httptest.Server, *Registry, *NetModel) {
	t.Helper()
	nm := newTestNet(t)
	r := NewRegistry(nm, cfg)
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192}, &fakeNode{})
	keysPath := filepath.Join(t.TempDir(), "keys.json")
	seed := `[{"id":"k1","name":"test","publicKey":"` + testKey + `","type":"ssh-ed25519","fingerprint":"` + testFingerprint + `"}]`
	if err := os.WriteFile(keysPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := LoadKeyStore(keysPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(APIHandler(r, keys, nm, cfg))
	t.Cleanup(srv.Close)
	return srv, r, nm
}

func postVM(t *testing.T, srv *httptest.Server, body string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/vms", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

const validBody = `{"nodeId":"n1","name":"dev","cpus":2,"memoryMB":2048,"diskGB":20,"sshKeyIds":["k1"]}`

func TestAPICreateAndList(t *testing.T) {
	srv := apiServer(t)
	resp, vm := postVM(t, srv, validBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d body = %v", resp.StatusCode, vm)
	}
	if vm["state"] != "pending" || vm["provider"] != "vz" {
		t.Fatalf("vm = %v", vm)
	}

	resp, err := http.Get(srv.URL + "/api/vms")
	if err != nil {
		t.Fatal(err)
	}
	var vms []map[string]any
	json.NewDecoder(resp.Body).Decode(&vms)
	resp.Body.Close()
	if len(vms) != 1 || vms[0]["id"] != vm["id"] {
		t.Fatalf("vms = %v", vms)
	}

	resp, err = http.Get(srv.URL + "/api/nodes")
	if err != nil {
		t.Fatal(err)
	}
	var nodes []map[string]any
	json.NewDecoder(resp.Body).Decode(&nodes)
	resp.Body.Close()
	if len(nodes) != 1 || nodes[0]["freeCpus"] != float64(2) {
		t.Fatalf("nodes = %v", nodes)
	}
}

func TestAPIValidation(t *testing.T) {
	srv := apiServer(t)
	for name, body := range map[string]string{
		"cpus":         strings.Replace(validBody, `"cpus":2`, `"cpus":0`, 1),
		"unknown node": strings.Replace(validBody, `"n1"`, `"nope"`, 1),
		"bad json":     `{`,
		"no key":       strings.Replace(validBody, `["k1"]`, `[]`, 1),
		"unknown key":  strings.Replace(validBody, `"k1"`, `"nope"`, 1),
	} {
		resp, out := postVM(t, srv, body)
		if resp.StatusCode != http.StatusBadRequest || out["error"] == "" || out["error"] == nil {
			t.Errorf("%s: status = %d body = %v", name, resp.StatusCode, out)
		}
	}
}

func TestAPIConflict(t *testing.T) {
	srv := apiServer(t)
	resp, out := postVM(t, srv, strings.Replace(validBody, `"cpus":2`, `"cpus":64`, 1))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d body = %v", resp.StatusCode, out)
	}
}

func TestAPIDelete(t *testing.T) {
	srv := apiServer(t)
	_, vm := postVM(t, srv, validBody)
	del := func(id string) int {
		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/vms/"+id, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if s := del(vm["id"].(string)); s != http.StatusNoContent {
		t.Fatalf("delete status = %d", s)
	}
	if s := del("unknown"); s != http.StatusNotFound {
		t.Fatalf("delete unknown status = %d", s)
	}
}

func TestAPIGatewayStateNeedsToken(t *testing.T) {
	srv := apiServer(t)
	resp, err := http.Get(srv.URL + "/api/gateway/state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/gateway/state", nil)
	req.Header.Set("Authorization", "Bearer tok")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st map[string]any
	json.NewDecoder(resp.Body).Decode(&st)
	if resp.StatusCode != http.StatusOK || st["underlayKey"] == "" || st["underlayKey"] == nil {
		t.Fatalf("status = %d state = %v", resp.StatusCode, st)
	}
	resp, err = http.Post(srv.URL+"/api/gateway/status", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status post = %d", resp.StatusCode)
	}
}

func TestAPIDevicesLifecycle(t *testing.T) {
	srv, _, nm := apiServerWith(t, testAPIConfig())
	resp, err := http.Post(srv.URL+"/api/devices", "application/json", strings.NewReader(`{"name":"laptop"}`))
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		Device Device `json:"device"`
		Config string `json:"config"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || !strings.Contains(created.Config, "PrivateKey = "+nm.Devices()[0].PrivateKey) {
		t.Fatalf("status = %d created = %+v", resp.StatusCode, created)
	}
	id := created.Device.ID

	resp, _ = http.Get(srv.URL + "/api/devices")
	var raw bytes.Buffer
	raw.ReadFrom(resp.Body)
	resp.Body.Close()
	if !strings.Contains(raw.String(), "laptop") || strings.Contains(strings.ToLower(raw.String()), "privatekey") {
		t.Fatalf("list = %s", raw.String())
	}

	resp, _ = http.Get(srv.URL + "/api/devices/" + id + "/config")
	raw.Reset()
	raw.ReadFrom(resp.Body)
	resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || !strings.Contains(raw.String(), "Endpoint = vmm.lan:51821") {
		t.Fatalf("config = %s (%s)", raw.String(), resp.Header.Get("Content-Type"))
	}

	req, _ := http.NewRequest("DELETE", srv.URL+"/api/devices/"+id, nil)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || len(nm.Devices()) != 0 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestAPIDeviceNeedsEndpoint(t *testing.T) {
	cfg := testAPIConfig()
	cfg.Clients.Endpoint = ""
	srv, _, nm := apiServerWith(t, cfg)
	resp, err := http.Post(srv.URL+"/api/devices", "application/json", strings.NewReader(`{"name":"laptop"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(out["error"], "clients.endpoint") || len(nm.Devices()) != 0 {
		t.Fatalf("status = %d out = %v", resp.StatusCode, out)
	}
}

func TestAPINetworks(t *testing.T) {
	srv, r, _ := apiServerWith(t, testAPIConfig())
	if _, err := r.CreateVM(validReq()); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.URL + "/api/networks")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out) != 1 || out[0]["name"] != "mgmt" || out[0]["subnet"] != "10.200.0.0/16" || out[0]["members"] != float64(1) {
		t.Fatalf("networks = %v", out)
	}
}
