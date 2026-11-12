package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"vm-manager/internal/proto"
	"vm-manager/internal/version"
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

func apiServerWith(t *testing.T, c Config) (*httptest.Server, *Registry, *NetModel) {
	t.Helper()
	cfg := newTestConfig(t, c)
	nm := newTestNet(t)
	r := NewRegistry(nm, newTestStore(t), newTestDisks(t), cfg)
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

func TestAPIDeviceBlockedBeforeSetup(t *testing.T) {
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
	if resp.StatusCode != http.StatusConflict || !strings.Contains(out["error"], "client endpoint") || len(nm.Devices()) != 0 {
		t.Fatalf("status = %d out = %v", resp.StatusCode, out)
	}
}

func doJSON(t *testing.T, method, url, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func TestAPINetworksLifecycle(t *testing.T) {
	srv, r, _ := apiServerWith(t, testAPIConfig())
	s, body := doJSON(t, "POST", srv.URL+"/api/networks", `{"name":"a"}`)
	var a Network
	json.Unmarshal(body, &a)
	if s != http.StatusCreated || a.ID == "" || a.Subnet != "10.210.0.0/24" {
		t.Fatalf("auto: status = %d body = %s", s, body)
	}
	s, body = doJSON(t, "POST", srv.URL+"/api/networks", `{"name":"b","subnet":"10.50.0.0/24"}`)
	var b Network
	json.Unmarshal(body, &b)
	if s != http.StatusCreated || b.Subnet != "10.50.0.0/24" {
		t.Fatalf("custom: status = %d body = %s", s, body)
	}
	if s, body := doJSON(t, "POST", srv.URL+"/api/networks", `{"name":"c","subnet":"8.8.8.0/24"}`); s != http.StatusBadRequest {
		t.Fatalf("invalid: status = %d body = %s", s, body)
	}
	q := validReq()
	q.NetworkIDs = []string{a.ID}
	vm, err := r.CreateVM(q)
	if err != nil {
		t.Fatal(err)
	}
	s, body = doJSON(t, "GET", srv.URL+"/api/networks", "")
	var list []struct {
		ID, Name, Subnet string
		Builtin          bool
		Members          []string
	}
	json.Unmarshal(body, &list)
	if s != http.StatusOK || len(list) != 3 || list[0].ID != "mgmt" || list[0].Name != "mgmt" || list[0].Subnet != "10.200.0.0/16" ||
		!list[0].Builtin || !slices.Equal(list[0].Members, []string{vm.ID}) {
		t.Fatalf("list = %s", body)
	}
	if list[1].ID != a.ID || list[1].Builtin || !slices.Equal(list[1].Members, []string{vm.ID}) || list[2].ID != b.ID || list[2].Members == nil || len(list[2].Members) != 0 {
		t.Fatalf("list = %s", body)
	}
	for _, c := range []struct {
		id   string
		want int
	}{{a.ID, http.StatusConflict}, {"mgmt", http.StatusBadRequest}, {"unknown", http.StatusNotFound}, {b.ID, http.StatusNoContent}} {
		if s, body := doJSON(t, "DELETE", srv.URL+"/api/networks/"+c.id, ""); s != c.want {
			t.Fatalf("delete %s: status = %d body = %s", c.id, s, body)
		}
	}
}

func TestAPISetVMNetworks(t *testing.T) {
	srv, r, nm := apiServerWith(t, testAPIConfig())
	a, err := nm.AddNetwork("a", "")
	if err != nil {
		t.Fatal(err)
	}
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	body := `{"networkIds":["` + a.ID + `"]}`
	if s, out := doJSON(t, "PUT", srv.URL+"/api/vms/"+vm.ID+"/networks", body); s != http.StatusConflict {
		t.Fatalf("status = %d body = %s", s, out)
	}
	s, out := doJSON(t, "PUT", srv.URL+"/api/vms/"+vm.ID+"/networks?restartAllowed=true", body)
	var got VM
	json.Unmarshal(out, &got)
	if s != http.StatusOK || len(got.NICs) != 2 || got.NICs[1].Network != a.ID {
		t.Fatalf("status = %d body = %s", s, out)
	}
}

func TestAPISetVMNetworksRequiresField(t *testing.T) {
	srv, r, nm := apiServerWith(t, testAPIConfig())
	a, err := nm.AddNetwork("a", "")
	if err != nil {
		t.Fatal(err)
	}
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/api/vms/" + vm.ID + "/networks?restartAllowed=true"
	if s, out := doJSON(t, "PUT", url, `{"networkIds":["`+a.ID+`"]}`); s != http.StatusOK {
		t.Fatalf("status = %d body = %s", s, out)
	}
	if s, out := doJSON(t, "PUT", url, `{}`); s != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", s, out)
	}
	if n := len(nm.NICs(vm.ID)); n != 2 {
		t.Fatalf("NICs = %d", n)
	}
	if _, err := r.SetPower(vm.ID, proto.PowerStopped, proto.StopHard); err != nil {
		t.Fatal(err)
	}
	if s, out := doJSON(t, "PUT", srv.URL+"/api/vms/"+vm.ID+"/networks", `{"networkIds":[]}`); s != http.StatusOK {
		t.Fatalf("status = %d body = %s", s, out)
	}
}

func TestAPIPowerEndpoints(t *testing.T) {
	srv, reg, _ := apiServerWith(t, testAPIConfig())
	_, vm := postVM(t, srv, validBody)
	post := func(id, action string) (int, map[string]any) {
		resp, err := http.Post(srv.URL+"/api/vms/"+id+"/"+action, "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	for _, c := range []struct {
		action, power string
		mode          proto.StopMode
	}{
		{"shutdown", "stopped", proto.StopGraceful},
		{"stop", "stopped", proto.StopHard},
		{"start", "running", ""},
	} {
		s, out := post(vm["id"].(string), c.action)
		if s != http.StatusOK || out["power"] != c.power {
			t.Fatalf("%s: status = %d body = %v", c.action, s, out)
		}
		if got := reg.store.VMs()[0].StopMode; got != c.mode {
			t.Fatalf("%s: stop mode = %q", c.action, got)
		}
		if s, _ := post("unknown", c.action); s != http.StatusNotFound {
			t.Fatalf("%s unknown: status = %d", c.action, s)
		}
	}
}

func TestAPIGetVM(t *testing.T) {
	srv := apiServer(t)
	_, vm := postVM(t, srv, validBody)
	s, body := doJSON(t, "GET", srv.URL+"/api/vms/"+vm["id"].(string), "")
	var got map[string]any
	json.Unmarshal(body, &got)
	if s != http.StatusOK || got["id"] != vm["id"] || got["name"] != "dev" || got["state"] != "pending" {
		t.Fatalf("status = %d body = %s", s, body)
	}
	if s, body := doJSON(t, "GET", srv.URL+"/api/vms/nope", ""); s != http.StatusNotFound {
		t.Fatalf("unknown: status = %d body = %s", s, body)
	}
}

func TestAPIGetNetwork(t *testing.T) {
	srv, r, _ := apiServerWith(t, testAPIConfig())
	_, body := doJSON(t, "POST", srv.URL+"/api/networks", `{"name":"a"}`)
	var a Network
	json.Unmarshal(body, &a)
	q := validReq()
	q.NetworkIDs = []string{a.ID}
	vm, err := r.CreateVM(q)
	if err != nil {
		t.Fatal(err)
	}
	s, body := doJSON(t, "GET", srv.URL+"/api/networks/"+a.ID, "")
	var got map[string]any
	json.Unmarshal(body, &got)
	if s != http.StatusOK || got["name"] != "a" || got["subnet"] != "10.210.0.0/24" || got["builtin"] != false {
		t.Fatalf("status = %d body = %s", s, body)
	}
	if m, _ := got["members"].([]any); len(m) != 1 || m[0] != vm.ID {
		t.Fatalf("members = %v", got["members"])
	}
	s, body = doJSON(t, "GET", srv.URL+"/api/networks/mgmt", "")
	json.Unmarshal(body, &got)
	if s != http.StatusOK || got["builtin"] != true {
		t.Fatalf("mgmt: status = %d body = %s", s, body)
	}
	if s, body := doJSON(t, "GET", srv.URL+"/api/networks/nope", ""); s != http.StatusNotFound {
		t.Fatalf("unknown: status = %d body = %s", s, body)
	}
}

func TestAPIDeviceOnHub(t *testing.T) {
	srv, _, _ := apiServerWith(t, testAPIConfig())
	s, body := doJSON(t, "POST", srv.URL+"/api/devices", `{"name":"mac","onHub":true}`)
	var out struct {
		Device struct {
			ID    string `json:"id"`
			OnHub bool   `json:"onHub"`
		} `json:"device"`
		Config string `json:"config"`
	}
	json.Unmarshal(body, &out)
	if s != http.StatusCreated || !out.Device.OnHub || !strings.Contains(out.Config, "Endpoint = 127.0.0.1:51821") {
		t.Fatalf("status = %d body = %s", s, body)
	}
	if s, conf := doJSON(t, "GET", srv.URL+"/api/devices/"+out.Device.ID+"/config", ""); s != http.StatusOK || !strings.Contains(string(conf), "Endpoint = 127.0.0.1:51821") {
		t.Fatalf("download: status = %d config = %s", s, conf)
	}
	if _, list := doJSON(t, "GET", srv.URL+"/api/devices", ""); !strings.Contains(string(list), `"onHub":true`) {
		t.Fatalf("list = %s", list)
	}
}

func TestAPIDeviceDefaultsToClientsEndpoint(t *testing.T) {
	srv := apiServer(t)
	s, body := doJSON(t, "POST", srv.URL+"/api/devices", `{"name":"laptop"}`)
	if s != http.StatusCreated || !strings.Contains(string(body), `Endpoint = vmm.lan:51821`) || strings.Contains(string(body), `"onHub"`) {
		t.Fatalf("status = %d body = %s", s, body)
	}
}

func TestAPINodesLifecycle(t *testing.T) {
	c := testAPIConfig()
	c.Hub.Port = 8080
	srv, r, _ := apiServerWith(t, c)
	if code, b := doJSON(t, "POST", srv.URL+"/api/nodes", `{"id":"Mac X"}`); code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", code, b)
	}
	code, b := doJSON(t, "POST", srv.URL+"/api/nodes", `{"id":"mac-x","provider":"vz"}`)
	var n map[string]any
	json.Unmarshal(b, &n)
	if code != http.StatusCreated || n["id"] != "mac-x" || n["state"] != "pending" {
		t.Fatalf("status = %d body = %s", code, b)
	}
	if _, ok := n["online"]; ok {
		t.Fatalf("online still in JSON: %s", b)
	}
	if code, b := doJSON(t, "POST", srv.URL+"/api/nodes", `{"id":"mac-x","provider":"vz"}`); code != http.StatusConflict {
		t.Fatalf("status = %d body = %s", code, b)
	}

	resp, err := http.Get(srv.URL + "/api/nodes/mac-x/config?host=localhost")
	if err != nil {
		t.Fatal(err)
	}
	var conf struct {
		Hub struct{ URL, Token string }
	}
	json.NewDecoder(resp.Body).Decode(&conf)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" ||
		conf.Hub.URL != "ws://localhost:8080/api/nodes/connect" || conf.Hub.Token != "tok" {
		t.Fatalf("status = %d conf = %+v", resp.StatusCode, conf)
	}
	if code, b := doJSON(t, "GET", srv.URL+"/api/nodes/mac-x/config?host=a:b:c:x", ""); code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", code, b)
	}
	if code, _ := doJSON(t, "GET", srv.URL+"/api/nodes/nope/config", ""); code != http.StatusNotFound {
		t.Fatalf("status = %d", code)
	}

	if resp, _ := postVM(t, srv, validBody); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create vm: %d", resp.StatusCode)
	}
	if code, _ := doJSON(t, "DELETE", srv.URL+"/api/nodes/n1", ""); code != http.StatusConflict {
		t.Fatalf("status = %d", code)
	}
	if code, _ := doJSON(t, "DELETE", srv.URL+"/api/nodes/nope", ""); code != http.StatusNotFound {
		t.Fatalf("status = %d", code)
	}
	if code, _ := doJSON(t, "DELETE", srv.URL+"/api/nodes/mac-x", ""); code != http.StatusNoContent {
		t.Fatalf("status = %d", code)
	}
	if _, ok := nodeByID(r, "mac-x"); ok {
		t.Fatal("node still listed")
	}
}

func TestAPINodesBlockedBeforeSetup(t *testing.T) {
	cfg := testAPIConfig()
	cfg.Clients.Endpoint = ""
	srv, r, _ := apiServerWith(t, cfg)
	if code, _ := doJSON(t, "POST", srv.URL+"/api/nodes", `{"id":"mac-x","provider":"vz"}`); code != http.StatusConflict {
		t.Fatalf("status = %d", code)
	}
	if code, _ := doJSON(t, "DELETE", srv.URL+"/api/nodes/n1", ""); code != http.StatusConflict {
		t.Fatalf("status = %d", code)
	}
	if len(r.Nodes()) != 1 {
		t.Fatalf("nodes = %+v", r.Nodes())
	}
}

func TestAPICreateNodeMalformedJSON(t *testing.T) {
	srv, _, _ := apiServerWith(t, testAPIConfig())
	if code, b := doJSON(t, "POST", srv.URL+"/api/nodes", `{`); code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", code, b)
	}
}

func TestAPISetVMDisksRequiresField(t *testing.T) {
	srv, r, _ := apiServerWith(t, testAPIConfig())
	d := mustDisk(t, r, "data", 1)
	vm, err := r.CreateVM(validReq())
	if err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/api/vms/" + vm.ID + "/disks"
	body := `{"disks":[{"diskId":"` + d.ID + `","mountPath":"/data"}]}`
	if s, out := doJSON(t, "PUT", url, body); s != http.StatusConflict {
		t.Fatalf("status = %d body = %s", s, out)
	}
	if s, out := doJSON(t, "PUT", url+"?restartAllowed=true", `{}`); s != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", s, out)
	}
	s, out := doJSON(t, "PUT", url+"?restartAllowed=true", body)
	var got VM
	json.Unmarshal(out, &got)
	if s != http.StatusOK || len(got.Attachments) != 1 || got.Attachments[0].DiskID != d.ID {
		t.Fatalf("status = %d body = %s", s, out)
	}
}

func TestAPIForceRelease(t *testing.T) {
	srv, r, _ := apiServerWith(t, testAPIConfig())
	d := mustDisk(t, r, "data", 1)
	q := validReq()
	q.Disks = []AttachRequest{{DiskID: d.ID, MountPath: "/data"}}
	vm, err := r.CreateVM(q)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteVM(vm.ID); err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/api/disks/" + d.ID + "/release"
	if s, out := doJSON(t, "POST", url, ""); s != http.StatusBadRequest {
		t.Fatalf("no force: %d %s", s, out)
	}
	if s, out := doJSON(t, "POST", url+"?force=true", ""); s != http.StatusConflict {
		t.Fatalf("node online: %d %s", s, out)
	}
	r.mu.Lock()
	conn := r.nodes["n1"].conn
	r.mu.Unlock()
	r.NodeDisconnected("n1", conn)
	if s, out := doJSON(t, "POST", url+"?force=true", ""); s != http.StatusNoContent {
		t.Fatalf("offline: %d %s", s, out)
	}
	if s, out := doJSON(t, "DELETE", srv.URL+"/api/disks/"+d.ID, ""); s != http.StatusNoContent {
		t.Fatalf("delete: %d %s", s, out)
	}
}

func TestVersionEndpoint(t *testing.T) {
	old := version.Version
	version.Version = "v9.9.9"
	t.Cleanup(func() { version.Version = old })
	// Before setup: install.sh reads the version of a hub that may not be configured yet.
	srv, _, _ := apiServerWith(t, Config{})
	status, body := doJSON(t, "GET", srv.URL+"/api/version", "")
	if status != 200 || strings.TrimSpace(string(body)) != `{"version":"v9.9.9"}` {
		t.Fatalf("status %d body %s", status, body)
	}
}

func TestInstallScriptServed(t *testing.T) {
	srv := httptest.NewServer(InstallScriptHandler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/node/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/x-shellscript" || string(body) != string(InstallScript) || len(body) == 0 {
		t.Fatalf("status %d type %q len %d", resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
	}
}

func TestInstallCommandEndpoint(t *testing.T) {
	srv, r, _ := apiServerWith(t, testAPIConfig())
	if _, err := r.CreateNode("n2", "firecracker"); err != nil {
		t.Fatal(err)
	}
	status, body := doJSON(t, "GET", srv.URL+"/api/nodes/n2/install-command?host=h.lan", "")
	if status != 200 || !strings.HasPrefix(string(body), "curl -fsSL http://h.lan:") || !strings.Contains(string(body), "--provider firecracker") {
		t.Fatalf("status %d body %s", status, body)
	}
	if status, _ := doJSON(t, "POST", srv.URL+"/api/nodes", `{"id":"n3"}`); status != 400 {
		t.Fatalf("create without provider: %d", status)
	}
}
