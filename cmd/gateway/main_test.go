package main

import (
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
