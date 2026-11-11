package main

import (
	"strings"
	"testing"

	"vm-manager/internal/hub"
)

type fakeRun struct {
	cmds []string
	out  map[string]string
}

func (f *fakeRun) run(args ...string) (string, error) {
	c := strings.Join(args, " ")
	f.cmds = append(f.cmds, c)
	return f.out[c], nil
}

func (f *fakeRun) has(sub string) bool {
	for _, c := range f.cmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

var testState = hub.GatewayState{GuestMTU: 1300, Ports: []hub.GatewayPort{
	{Name: "vm1-mgmt", MAC: "02:0a:c8:00:00:14", IP: "10.200.0.20", Hostname: "dev.mgmt.internal"}}}

func TestReconcileNBCreatesTopologyAndACLs(t *testing.T) {
	f := &fakeRun{out: map[string]string{"ovn-nbctl dhcp-options-create 10.200.0.0/16": "u-1"}}
	if err := reconcileNB(f.run, testState); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"lsp-add ls-mgmt vm1-mgmt",
		"lsp-set-port-security vm1-mgmt 02:0a:c8:00:00:14 10.200.0.20",
		"classless_static_route={10.201.0.0/24,10.200.0.1}",
		"mtu=1300",
		"lsp-set-dhcpv4-options vm1-mgmt u-1",
		"acl-add pg_vms from-lport 1002 inport == @pg_vms && (ip4.dst == 10.200.0.1 || ip4.dst == 10.201.0.0/24 || arp.tpa == 10.200.0.1 || (udp.src == 68 && udp.dst == 67)) allow",
		"acl-add pg_vms from-lport 1001 inport == @pg_vms drop",
		"lrp-set-gateway-chassis lrp-transit hub-gw",
		"lr-route-add lr-gw 10.201.0.0/24 10.199.0.2",
	} {
		if !f.has(want) {
			t.Errorf("missing command containing %q in:\n%s", want, strings.Join(f.cmds, "\n"))
		}
	}
}

func TestReconcileNBDeletesStalePorts(t *testing.T) {
	f := &fakeRun{out: map[string]string{
		"ovn-nbctl lsp-list ls-mgmt":                  "aaa (old-mgmt)\nbbb (mgmt-lr)\nccc (vm1-mgmt)",
		"ovn-nbctl lsp-get-type mgmt-lr":              "router",
		"ovn-nbctl dhcp-options-create 10.200.0.0/16": "u-1",
	}}
	if err := reconcileNB(f.run, testState); err != nil {
		t.Fatal(err)
	}
	if !f.has("lsp-del old-mgmt") {
		t.Error("stale port not deleted")
	}
	if f.has("lsp-del mgmt-lr") || f.has("lsp-del vm1-mgmt") {
		t.Error("router or desired port deleted")
	}
}
