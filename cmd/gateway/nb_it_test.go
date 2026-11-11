package main

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"vm-manager/internal/hub"
)

// Opt-in: VMM_NB_IT=<container> runs reconcileNB against ovn-nbctl inside that container.
func TestReconcileNBRealNB(t *testing.T) {
	ctr := os.Getenv("VMM_NB_IT")
	if ctr == "" {
		t.Skip("VMM_NB_IT not set")
	}
	run := func(args ...string) (string, error) {
		out, err := exec.Command("docker", append([]string{"exec", ctr}, args...)...).CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("%w: %s", err, out)
		}
		return string(out), nil
	}
	port := func(n int) hub.GatewayPort {
		return hub.GatewayPort{Name: fmt.Sprintf("vm%d-mgmt", n), MAC: fmt.Sprintf("02:0a:c8:00:00:%02x", 20+n), IP: fmt.Sprintf("10.200.0.%d", 20+n)}
	}
	for _, ports := range [][]hub.GatewayPort{{port(1)}, {port(1)}, {port(1), port(2)}, {port(2)}} {
		if err := reconcileNB(run, hub.GatewayState{GuestMTU: 1300, Ports: ports}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range [][]string{{"show"}, {"acl-list", "pg_vms"}, {"list", "dhcp_options"}, {"list", "port_group"}} {
		out, _ := run(append([]string{"ovn-nbctl"}, c...)...)
		t.Logf("ovn-nbctl %v\n%s", c, out)
	}
}
