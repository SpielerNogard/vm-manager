package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vm-manager/internal/proto"
)

// releaseServer answers like the GitHub latest-release endpoint with whatever *answer holds; status 0 means 200.
func releaseServer(t *testing.T, status *int, answer *string) (*Updates, *atomic.Int32, *http.Header) {
	t.Helper()
	var calls atomic.Int32
	var header http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		header = req.Header.Clone()
		if *status != 0 {
			w.WriteHeader(*status)
		}
		w.Write([]byte(*answer))
	}))
	t.Cleanup(srv.Close)
	u := NewUpdates()
	u.URL, u.MinInterval = srv.URL, 0
	return u, &calls, &header
}

func TestCheckFindsNewer(t *testing.T) {
	setHubVersion(t, "v0.4.0")
	status, answer := 0, `{"tag_name":"v0.5.0"}`
	u, _, header := releaseServer(t, &status, &answer)
	r, _ := newTestRegistry(t)
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := u.Status(r, nil)
	if st.Version != "v0.4.0" || st.Latest != "v0.5.0" || !st.UpdateAvailable || st.CheckedAt == nil || st.CheckError != "" || st.HubUpdate != "manual" {
		t.Errorf("status = %+v", st)
	}
	if header.Get("User-Agent") != "vm-manager" || header.Get("Accept") != "application/vnd.github+json" {
		t.Errorf("request headers = %v", *header)
	}
}

func TestCheckNotNewer(t *testing.T) {
	setHubVersion(t, "v0.4.0")
	for _, tag := range []string{"v0.4.0", "v0.3.0"} {
		status, answer := 0, `{"tag_name":"`+tag+`"}`
		u, _, _ := releaseServer(t, &status, &answer)
		r, _ := newTestRegistry(t)
		if err := u.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		if st := u.Status(r, nil); st.UpdateAvailable || st.Latest != tag {
			t.Errorf("latest %s: status = %+v", tag, st)
		}
	}
}

func TestCheckErrorKeepsLatest(t *testing.T) {
	setHubVersion(t, "v0.4.0")
	status, answer := 0, `{"tag_name":"v0.5.0"}`
	u, _, _ := releaseServer(t, &status, &answer)
	r, _ := newTestRegistry(t)
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, answer = http.StatusForbidden, `{"message":"API rate limit exceeded"}`
	err := u.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want one naming status 403", err)
	}
	if st := u.Status(r, nil); st.Latest != "v0.5.0" || st.CheckError == "" || !st.UpdateAvailable {
		t.Errorf("status = %+v", st)
	}
}

func TestCheckRejectsBadTag(t *testing.T) {
	setHubVersion(t, "v0.4.0")
	for _, body := range []string{`{"tag_name":"nightly"}`, `{}`, `not json`} {
		status, answer := 0, body
		u, _, _ := releaseServer(t, &status, &answer)
		r, _ := newTestRegistry(t)
		if err := u.Check(context.Background()); err == nil {
			t.Errorf("%s: no error", body)
		}
		if st := u.Status(r, nil); st.Latest != "" || st.CheckError == "" || st.UpdateAvailable {
			t.Errorf("%s: status = %+v", body, st)
		}
	}
}

func updatesServer(t *testing.T, c Config, u *Updates) (*httptest.Server, *Registry) {
	t.Helper()
	cfg := newTestConfig(t, c)
	nm := newTestNet(t)
	r := NewRegistry(nm, newTestStore(t), newTestDisks(t), cfg)
	keys, err := LoadKeyStore(t.TempDir() + "/keys.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(APIHandler(r, keys, nm, cfg, u, nil))
	t.Cleanup(srv.Close)
	return srv, r
}

func TestDevBuild(t *testing.T) {
	setHubVersion(t, "dev")
	status, answer := 0, `{"tag_name":"v0.5.0"}`
	u, calls, _ := releaseServer(t, &status, &answer)
	u.Run(context.Background())
	srv, r := updatesServer(t, testAPIConfig(), u)
	if st := u.Status(r, nil); st.HubUpdate != "none" || st.UpdateAvailable {
		t.Errorf("status = %+v", st)
	}
	resp, err := http.Post(srv.URL+"/api/updates/check", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("check on a dev build: status %d, want 409", resp.StatusCode)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("a dev build asked for releases %d times", n)
	}
}

func TestUpdatesAPI(t *testing.T) {
	setHubVersion(t, "v0.4.0")
	status, answer := 0, `{"tag_name":"v0.5.0"}`
	u, _, _ := releaseServer(t, &status, &answer)
	// The client endpoint is unset: checking for updates must work before setup is finished.
	srv, r := updatesServer(t, freshConfig(), u)
	r.SetGatewayStatus(GatewayStatus{Version: "v0.4.0"})
	r.StorageConnected(&fakeStorage{})
	r.SetStorageVersion("v0.3.0")
	r.NodeConnected(proto.Register{NodeID: "n1", Provider: "vz"}, &fakeNode{})

	get := func(method, path string) UpdateStatus {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status %d", method, path, resp.StatusCode)
		}
		var st UpdateStatus
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := get("GET", "/api/updates"); st.Gateway != "v0.4.0" || st.Storage != "v0.3.0" || st.Latest != "" || st.UpdateAvailable {
		t.Errorf("before the check: %+v", st)
	}
	if st := get("POST", "/api/updates/check"); st.Latest != "v0.5.0" || !st.UpdateAvailable {
		t.Errorf("after the check: %+v", st)
	}
	status = http.StatusInternalServerError
	if st := get("POST", "/api/updates/check"); st.CheckError == "" || st.Latest != "v0.5.0" {
		t.Errorf("after a failed check: %+v", st)
	}
}

func TestCheckNowIsThrottled(t *testing.T) {
	setHubVersion(t, "v0.4.0")
	status, answer := 0, `{"tag_name":"v0.5.0"}`
	u, calls, _ := releaseServer(t, &status, &answer)
	u.MinInterval = time.Minute
	srv, _ := updatesServer(t, testAPIConfig(), u)
	for range 3 {
		resp, err := http.Post(srv.URL+"/api/updates/check", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("three clicks asked GitHub %d times, want 1", n)
	}
}
