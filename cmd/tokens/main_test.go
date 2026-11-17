package main

import (
	"context"
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
	go func() { done <- watchHealth(t.Context(), ln.Addr().String(), 10*time.Millisecond, 3) }()
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
	if err := watchHealth(ctx, "127.0.0.1:1", 10*time.Millisecond, 3); err != nil {
		t.Fatalf("got %v", err)
	}
}
