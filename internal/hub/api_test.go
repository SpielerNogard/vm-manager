package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func apiServer(t *testing.T) *httptest.Server {
	t.Helper()
	r, _ := newTestRegistry(t)
	keysPath := filepath.Join(t.TempDir(), "keys.json")
	seed := `[{"id":"k1","name":"test","publicKey":"` + testKey + `","type":"ssh-ed25519","fingerprint":"` + testFingerprint + `"}]`
	if err := os.WriteFile(keysPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := LoadKeyStore(keysPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(APIHandler(r, keys))
	t.Cleanup(srv.Close)
	return srv
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
