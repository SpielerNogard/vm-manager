package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

var allow = netip.MustParsePrefix("10.200.0.0/16")

func tokenFn() (string, error) { return "tok", nil }

func get(h http.Handler, path, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestForwardsWithRemoteIP(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tokens/github" || r.URL.Query().Get("ip") != "10.200.0.23" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("unexpected hub request %s auth=%q", r.URL, r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"token":"ghp_x","expiresAt":""}`))
	}))
	defer hub.Close()
	rec := get(newHandler(hub.URL, tokenFn, allow, hub.Client()), "/v1/github/token", "10.200.0.23:4444")
	if rec.Code != 200 || rec.Body.String() != "ghp_x" || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("got %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
}

func TestRejectsOutsideAllow(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("hub must not be called")
	}))
	defer hub.Close()
	rec := get(newHandler(hub.URL, tokenFn, allow, hub.Client()), "/v1/github/token", "192.168.64.5:4444")
	if rec.Code != 403 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestIgnoresForwardedIP(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query()["ip"]; len(got) != 1 || got[0] != "10.200.0.23" {
			t.Errorf("hub saw ip=%v", got)
		}
		w.Write([]byte(`{"token":"ghp_x"}`))
	}))
	defer hub.Close()
	req := httptest.NewRequest(http.MethodGet, "/v1/github/token?ip=10.200.0.99", nil)
	req.RemoteAddr = "10.200.0.23:4444"
	req.Header.Set("X-Forwarded-For", "10.200.0.99")
	rec := httptest.NewRecorder()
	newHandler(hub.URL, tokenFn, allow, hub.Client()).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestInvalidNameSkipsHub(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("hub called with %s", r.URL)
	}))
	defer hub.Close()
	for _, path := range []string{"/v1/a%2Fb/token", "/v1/%2e%2e/token", "/v1/%2e/token", "/v1/GitHub/token", "/v1/a%3Fip=1/token"} {
		if rec := get(newHandler(hub.URL, tokenFn, allow, hub.Client()), path, "10.200.0.23:4444"); rec.Code != 404 {
			t.Errorf("%s: got %d", path, rec.Code)
		}
	}
}

func TestHubTokenError(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("hub must not be called")
	}))
	defer hub.Close()
	failing := func() (string, error) { return "", errors.New("unreadable") }
	if rec := get(newHandler(hub.URL, failing, allow, hub.Client()), "/v1/github/token", "10.200.0.23:4444"); rec.Code != 502 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestEmptyHubTokenIs502(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"token":"","expiresAt":""}`))
	}))
	defer hub.Close()
	if rec := get(newHandler(hub.URL, tokenFn, allow, hub.Client()), "/v1/github/token", "10.200.0.23:4444"); rec.Code != 502 || rec.Body.String() == "" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestOtherHubStatusIs502(t *testing.T) {
	for _, status := range []int{401, 500, 503} {
		hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		rec := get(newHandler(hub.URL, tokenFn, allow, hub.Client()), "/v1/github/token", "10.200.0.23:4444")
		hub.Close()
		if rec.Code != 502 {
			t.Errorf("hub %d: got %d", status, rec.Code)
		}
	}
}

func TestHubClientDoesNotFollowRedirects(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect followed, Authorization=%q", r.Header.Get("Authorization"))
	}))
	defer elsewhere.Close()
	hub := httptest.NewServer(http.RedirectHandler(elsewhere.URL, http.StatusFound))
	defer hub.Close()
	if rec := get(newHandler(hub.URL, tokenFn, allow, hubClient()), "/v1/github/token", "10.200.0.23:4444"); rec.Code != 502 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestParseHubURL(t *testing.T) {
	for _, ok := range []string{"http://host.docker.internal:8080", "https://hub.example"} {
		if err := checkHubURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "host:8080", "ftp://x", "http://", "http//x", "://x"} {
		if err := checkHubURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPassesHubStatus(t *testing.T) {
	for _, status := range []int{403, 404} {
		hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"x"}`, status)
		}))
		rec := get(newHandler(hub.URL, tokenFn, allow, hub.Client()), "/v1/github/token", "10.200.0.23:4444")
		hub.Close()
		if rec.Code != status || rec.Body.Len() != 0 {
			t.Errorf("hub %d: got %d %q", status, rec.Code, rec.Body.String())
		}
	}
}

func TestHubDown(t *testing.T) {
	hub := httptest.NewServer(http.NotFoundHandler())
	url, client := hub.URL, hub.Client()
	hub.Close()
	if rec := get(newHandler(url, tokenFn, allow, client), "/v1/github/token", "10.200.0.23:4444"); rec.Code != 502 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestUnknownPath(t *testing.T) {
	if rec := get(newHandler("http://x", tokenFn, allow, http.DefaultClient), "/other", "10.200.0.23:4444"); rec.Code != 404 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestWatchHealthExits(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- watchHealth(t.Context(), dialCheck(ln.Addr().String(), 10*time.Millisecond), 10*time.Millisecond, 3)
	}()
	time.Sleep(30 * time.Millisecond)
	ln.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchHealth did not return")
	}
}

func TestWatchHealthCancelIsClean(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := watchHealth(ctx, dialCheck("127.0.0.1:1", 10*time.Millisecond), 10*time.Millisecond, 3); err != nil {
		t.Fatalf("got %v", err)
	}
}

func TestWatchHealthResetsAfterSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	results := []error{errDown, errDown, nil, errDown, errDown, nil, errDown, errDown}
	calls := 0
	check := func() error {
		if calls == len(results) {
			cancel()
			return nil
		}
		calls++
		return results[calls-1]
	}
	if err := watchHealth(ctx, check, time.Millisecond, 3); err != nil {
		t.Fatalf("got %v after %d checks", err, calls)
	}
}

var errDown = errors.New("down")
