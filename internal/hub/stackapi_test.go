package hub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vm-manager/internal/proto"
)

// stackAPI serves the API of a hub with the given stack (nil: Compose mode); a non-nil stack runs with a recording runner.
func stackAPI(t *testing.T, managed bool) (*httptest.Server, *ConfigStore, *Registry, *stackRunner) {
	t.Helper()
	setHubVersion(t, "v0.5.0")
	cfg := newTestConfig(t, testAPIConfig())
	nm := newTestNet(t)
	r := NewRegistry(nm, newTestStore(t), newTestDisks(t), cfg)
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz", CPUs: 4, MemoryMB: 8192}, &fakeNode{})
	keys, err := LoadKeyStore(t.TempDir() + "/keys.json")
	if err != nil {
		t.Fatal(err)
	}
	var s *Stack
	runner := &stackRunner{}
	if managed {
		s = NewStack(t.TempDir(), cfg)
		s.run, s.Retry = runner.run, 10*time.Millisecond
		s.Storage = func() bool { return cfg.Get().Storage.Local }
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go s.Run(ctx)
		waitFor(t, "the stack to run", func() bool { return s.State().State == "running" })
	}
	srv := httptest.NewServer(APIHandler(r, keys, nm, cfg, NewUpdates(), s))
	t.Cleanup(srv.Close)
	return srv, cfg, r, runner
}

func TestSettingsStorageLocal(t *testing.T) {
	srv, cfg, _, runner := stackAPI(t, true)
	code, body := doJSON(t, "PUT", srv.URL+"/api/settings", `{"storage":{"local":true}}`)
	var out struct {
		Storage      struct{ Local bool }
		StackManaged bool
	}
	json.Unmarshal(body, &out)
	if code != 200 || !out.Storage.Local || !out.StackManaged || !cfg.Get().Storage.Local {
		t.Fatalf("status %d body %s", code, body)
	}
	waitFor(t, "a build with the storage profile", func() bool { return runner.count("--profile storage build") == 1 })
}

func TestSettingsStorageLocalRefusedWithDisks(t *testing.T) {
	srv, cfg, r, _ := stackAPI(t, true)
	if code, body := doJSON(t, "PUT", srv.URL+"/api/settings", `{"storage":{"local":true}}`); code != 200 {
		t.Fatalf("status %d body %s", code, body)
	}
	r.StorageConnected(&fakeStorage{})
	d := mustDisk(t, r, "data", 1)
	createWithDisks(t, r, "a", AttachRequest{DiskID: d.ID, MountPath: "/data"})
	code, body := doJSON(t, "PUT", srv.URL+"/api/settings", `{"storage":{"local":false}}`)
	if code != 409 || !strings.Contains(string(body), "disk") {
		t.Errorf("status %d body %s, want 409 naming the disks", code, body)
	}
	if !cfg.Get().Storage.Local {
		t.Error("the storage server was switched off under an attached disk")
	}
}

func TestSettingsStorageLocalNeedsStack(t *testing.T) {
	srv, _, _, _ := stackAPI(t, false)
	code, body := doJSON(t, "PUT", srv.URL+"/api/settings", `{"storage":{"local":true}}`)
	if code != 409 || !strings.Contains(string(body), "Compose") {
		t.Errorf("status %d body %s, want 409 naming Compose", code, body)
	}
	_, body = doJSON(t, "GET", srv.URL+"/api/settings", "")
	if !strings.Contains(string(body), `"stackManaged":false`) {
		t.Errorf("settings = %s", body)
	}
}

func TestUpdatesStackState(t *testing.T) {
	srv, _, _, runner := stackAPI(t, true)
	stackOf := func() StackState {
		t.Helper()
		_, body := doJSON(t, "GET", srv.URL+"/api/updates", "")
		var st UpdateStatus
		if err := json.Unmarshal(body, &st); err != nil {
			t.Fatal(err)
		}
		return st.Stack
	}
	if st := stackOf(); st != (StackState{Managed: true, State: "running"}) {
		t.Errorf("stack = %+v", st)
	}
	runner.mu.Lock()
	runner.fail = map[string]string{"build": "boom"}
	runner.mu.Unlock()
	before := runner.count(" build")
	if code, body := doJSON(t, "POST", srv.URL+"/api/updates/stack/retry", ""); code != 202 {
		t.Fatalf("retry: status %d body %s", code, body)
	}
	waitFor(t, "a failed rebuild", func() bool { st := stackOf(); return st.State == "failed" && strings.Contains(st.Error, "boom") })
	if runner.count(" build") <= before {
		t.Error("retry did not build")
	}
}

func TestUpdatesStackStateCompose(t *testing.T) {
	srv, _, _, _ := stackAPI(t, false)
	_, body := doJSON(t, "GET", srv.URL+"/api/updates", "")
	if !strings.Contains(string(body), `"stack":{"managed":false}`) {
		t.Errorf("updates = %s", body)
	}
	if code, _ := doJSON(t, "POST", srv.URL+"/api/updates/stack/retry", ""); code != 409 {
		t.Errorf("retry without a managed stack: status %d, want 409", code)
	}
}
