package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
