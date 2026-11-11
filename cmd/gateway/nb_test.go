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

func (f *fakeRun) mutating() []string {
	var m []string
	for _, c := range f.cmds {
		if strings.Contains(c, " lsp-add ") {
			m = append(m, c)
		}
	}
	return m
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
	f := &fakeRun{out: map[string]string{"ovn-nbctl --bare --columns=_uuid find dhcp_options cidr=10.200.0.0/16": "u-1"}}
	if err := reconcileNB(f.run, testState); err != nil {
		t.Fatal(err)
	}
	tx := f.mutating()
	if len(tx) != 1 {
		t.Fatalf("want exactly one mutating invocation, got %d", len(tx))
	}
	for _, want := range []string{"pg-set-ports pg_vms vm1-mgmt", "acl-del pg_vms", "from-lport 1002", "from-lport 1001"} {
		if !strings.Contains(tx[0], want) {
			t.Errorf("transaction missing %q", want)
		}
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
		"ovn-nbctl lsp-list ls-mgmt":                                            "aaa (old-mgmt)\nbbb (mgmt-lr)\nccc (vm1-mgmt)",
		"ovn-nbctl lsp-get-type mgmt-lr":                                        "router",
		"ovn-nbctl --bare --columns=_uuid find dhcp_options cidr=10.200.0.0/16": "u-1",
	}}
	if err := reconcileNB(f.run, testState); err != nil {
		t.Fatal(err)
	}
	if !f.has("lsp-del old-mgmt") {
		t.Error("stale port not deleted")
	}
	if len(f.mutating()) != 1 || !strings.Contains(f.mutating()[0], "lsp-del old-mgmt") {
		t.Error("stale delete not in the single transaction")
	}
	if f.has("lsp-del mgmt-lr") || f.has("lsp-del vm1-mgmt") {
		t.Error("router or desired port deleted")
	}
}

func TestReconcileNBCreatesMissingDHCPOptions(t *testing.T) {
	f := &fakeRun{out: map[string]string{}}
	calls := 0
	run := func(args ...string) (string, error) {
		c := strings.Join(args, " ")
		if strings.Contains(c, "find dhcp_options") {
			calls++
			if calls == 2 {
				f.cmds = append(f.cmds, c)
				return "u-9", nil
			}
		}
		return f.run(args...)
	}
	if err := reconcileNB(run, testState); err != nil {
		t.Fatal(err)
	}
	if !f.has("dhcp-options-create 10.200.0.0/16") || !f.has("lsp-set-dhcpv4-options vm1-mgmt u-9") {
		t.Errorf("dhcp row not created then looked up:\n%s", strings.Join(f.cmds, "\n"))
	}
}

const netID = "n1"

func networkState() hub.GatewayState {
	return hub.GatewayState{GuestMTU: 1300,
		Networks: []hub.GatewayNetwork{{ID: netID, Name: "lab", Subnet: "10.210.0.0/24"}},
		Ports: []hub.GatewayPort{
			{Name: "vm1-mgmt", MAC: "02:0a:c8:00:00:14", IP: "10.200.0.20", Network: "mgmt"},
			{Name: "vm1-n1", MAC: "02:0a:d2:00:00:02", IP: "10.210.0.2", Network: netID}}}
}

func TestReconcileNBCreatesNetworkSwitch(t *testing.T) {
	f := &fakeRun{out: map[string]string{
		"ovn-nbctl --bare --columns=_uuid find dhcp_options cidr=10.200.0.0/16":          "u-1",
		"ovn-nbctl --bare --columns=_uuid find dhcp_options external_ids:vmm-network=n1": "u-n1",
	}}
	if err := reconcileNB(f.run, networkState()); err != nil {
		t.Fatal(err)
	}
	tx := f.mutating()
	if len(tx) != 1 {
		t.Fatalf("want one transaction, got %d", len(tx))
	}
	for _, want := range []string{
		"ls-add ls-net-n1", "lsp-add ls-net-n1 vm1-n1",
		"lsp-set-port-security vm1-n1 02:0a:d2:00:00:02 10.210.0.2",
		"lsp-set-dhcpv4-options vm1-n1 u-n1",
		"dhcp-options-set-options u-n1 server_id=10.210.0.1 server_mac=02:0a:d2:00:00:01 lease_time=3600 mtu=1300 --",
		"pg-set-ports pg_vms vm1-mgmt --",
	} {
		if !strings.Contains(tx[0]+" --", want) {
			t.Errorf("transaction missing %q in %s", want, tx[0])
		}
	}
	if strings.Contains(tx[0], "server_id=10.210.0.1 server_mac=02:0a:d2:00:00:01 lease_time=3600 mtu=1300 router") {
		t.Error("network dhcp has router")
	}
	if strings.Contains(tx[0], "vm1-n1 --") && strings.Contains(tx[0], "pg-set-ports pg_vms vm1-mgmt vm1-n1") {
		t.Error("network port in pg_vms")
	}
}

func TestReconcileNBDeletesStaleNetwork(t *testing.T) {
	f := &fakeRun{out: map[string]string{
		"ovn-nbctl --bare --columns=_uuid find dhcp_options cidr=10.200.0.0/16": "u-1",
		"ovn-nbctl ls-list": "s1 (ls-mgmt)\ns2 (ls-net-old)\ns3 (ls-transit)",
		"ovn-nbctl --format=csv --data=bare --no-headings --columns=_uuid,external_ids list dhcp_options": "u-1,\nu-old,vmm-network=old\n",
	}}
	if err := reconcileNB(f.run, testState); err != nil {
		t.Fatal(err)
	}
	if !f.has("ls-del ls-net-old") || !f.has("dhcp-options-del u-old") {
		t.Errorf("stale network not removed:\n%s", strings.Join(f.cmds, "\n"))
	}
	if f.has("ls-del ls-mgmt") || f.has("ls-del ls-transit") || f.has("dhcp-options-del u-1") {
		t.Error("deleted non-network objects")
	}
}

func TestReconcileNBDeletesStalePortOnNetworkSwitch(t *testing.T) {
	f := &fakeRun{out: map[string]string{
		"ovn-nbctl --bare --columns=_uuid find dhcp_options cidr=10.200.0.0/16":          "u-1",
		"ovn-nbctl --bare --columns=_uuid find dhcp_options external_ids:vmm-network=n1": "u-n1",
		"ovn-nbctl lsp-list ls-net-n1": "aaa (old-n1)\nbbb (vm1-n1)",
	}}
	if err := reconcileNB(f.run, networkState()); err != nil {
		t.Fatal(err)
	}
	if !f.has("lsp-del old-n1") || f.has("lsp-del vm1-n1") {
		t.Errorf("wrong stale handling:\n%s", strings.Join(f.cmds, "\n"))
	}
}

func TestReconcileNBSkipsPortOfUnknownNetwork(t *testing.T) {
	f := &fakeRun{out: map[string]string{"ovn-nbctl --bare --columns=_uuid find dhcp_options cidr=10.200.0.0/16": "u-1",
		"ovn-nbctl lsp-list ls-mgmt": "aaa (ghost-n)"}}
	st := testState
	st.Ports = append(append([]hub.GatewayPort{}, testState.Ports...),
		hub.GatewayPort{Name: "ghost-n", MAC: "02:0a:d2:00:00:09", IP: "10.210.0.9", Network: "gone"})
	if err := reconcileNB(f.run, st); err != nil {
		t.Fatal(err)
	}
	if f.has("lsp-add ls-mgmt ghost-n") || f.has("ghost-n 02:") || f.has("lsp-del ghost-n") || f.has("pg-set-ports pg_vms vm1-mgmt ghost-n") {
		t.Errorf("unknown-network port touched:\n%s", strings.Join(f.cmds, "\n"))
	}
	if !f.has("lsp-add ls-mgmt vm1-mgmt") {
		t.Error("mgmt port not reconciled")
	}
}
