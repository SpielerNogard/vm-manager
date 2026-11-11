package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vm-manager/internal/hub"
)

func TestValidateState(t *testing.T) {
	good := hub.GatewayState{UnderlayKey: "a", ClientsKey: "b", GuestMTU: 1300}
	if err := validateState(good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []hub.GatewayState{{}, {UnderlayKey: "a", ClientsKey: "b"}, {UnderlayKey: "a", GuestMTU: 1300}, {ClientsKey: "b", GuestMTU: 1300}} {
		if validateState(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestApplySyncsWGWhenNBFails(t *testing.T) {
	var wg int
	run := func(args ...string) (string, error) {
		if args[0] == "wg" {
			wg++
			return "", nil
		}
		return "", errTest
	}
	err := apply(run, hub.GatewayState{UnderlayKey: "a", ClientsKey: "b", GuestMTU: 1300})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
	if wg != 2 {
		t.Fatalf("wg syncs %d", wg)
	}
}

type testErr struct{}

func (testErr) Error() string { return "boom" }

var errTest = testErr{}

func TestCallHubAcceptsAny2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/status" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	if _, err := callHub(srv.Client(), srv.URL, "tok", "POST", "/status", nil); err != nil {
		t.Fatalf("204: %v", err)
	}
	if b, err := callHub(srv.Client(), srv.URL, "tok", "GET", "/state", nil); err != nil || string(b) != "ok" {
		t.Fatalf("200: %q, %v", b, err)
	}
	if _, err := callHub(srv.Client(), srv.URL, "bad", "GET", "/state", nil); err == nil {
		t.Fatal("401 accepted")
	}
}

func TestCallHubRereadsTokenOn401(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		seen = append(seen, tok)
		if tok != "new" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "hub.json")
	if err := os.WriteFile(file, []byte(`{"token":"new"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	token := "old"
	b, err := callHubRereadingToken(srv.Client(), srv.URL, file, &token, "GET", "/state", nil)
	if err != nil || string(b) != "ok" {
		t.Fatalf("got %q, %v", b, err)
	}
	if strings.Join(seen, ",") != "old,new" || token != "new" {
		t.Errorf("seen %v, token %q", seen, token)
	}
}

func TestReadToken(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hub.json")
	if _, err := readToken(file); err == nil {
		t.Error("missing file accepted")
	}
	for _, content := range []string{`{}`, `not json`} {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readToken(file); err == nil {
			t.Errorf("%q accepted", content)
		}
	}
	if err := os.WriteFile(file, []byte(`{"token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, err := readToken(file); err != nil || tok != "t" {
		t.Errorf("got %q, %v", tok, err)
	}
}

func TestWaitForToken(t *testing.T) {
	file := filepath.Join(t.TempDir(), "hub.json")
	writeErr := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		writeErr <- os.WriteFile(file, []byte(`{"token":"late"}`), 0o600)
	}()
	tokCh := make(chan string, 1)
	go func() { tokCh <- waitForToken(file) }()
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
	select {
	case tok := <-tokCh:
		if tok != "late" {
			t.Errorf("token = %q", tok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForToken did not return")
	}
}

func TestCallHubRereadingTokenDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	token := "old"
	if _, err := callHubRereadingToken(srv.Client(), srv.URL, filepath.Join(t.TempDir(), "none"), &token, "GET", "/state", nil); err == nil {
		t.Fatal("want error")
	}
	if calls != 1 || token != "old" {
		t.Errorf("calls = %d, token = %q", calls, token)
	}
}
