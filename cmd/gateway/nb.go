package main

import (
	"fmt"
	"strings"

	"vm-manager/internal/hub"
)

type runner func(args ...string) (string, error)

const (
	aclAllow = "inport == @pg_vms && (ip4.dst == 10.200.0.1 || ip4.dst == 10.201.0.0/24 || arp.tpa == 10.200.0.1 || (udp.src == 68 && udp.dst == 67))"
	aclDrop  = "inport == @pg_vms"
)

func reconcileNB(run runner, s hub.GatewayState) error {
	var err error
	nb := func(args ...string) string {
		if err != nil {
			return ""
		}
		var out string
		out, err = run(append([]string{"ovn-nbctl"}, args...)...)
		if err != nil {
			err = fmt.Errorf("ovn-nbctl %s: %w", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(out)
	}
	m := "--may-exist"
	nb(m, "ls-add", "ls-mgmt")
	nb(m, "lr-add", "lr-gw")
	nb(m, "lrp-add", "lr-gw", "lrp-mgmt", "02:00:00:c8:00:01", "10.200.0.1/16")
	nb(m, "lsp-add", "ls-mgmt", "mgmt-lr")
	nb("lsp-set-type", "mgmt-lr", "router")
	nb("lsp-set-addresses", "mgmt-lr", "router")
	nb("lsp-set-options", "mgmt-lr", "router-port=lrp-mgmt")
	nb(m, "ls-add", "ls-transit")
	nb(m, "lrp-add", "lr-gw", "lrp-transit", "02:00:00:c7:00:01", "10.199.0.1/30")
	nb(m, "lsp-add", "ls-transit", "transit-lr")
	nb("lsp-set-type", "transit-lr", "router")
	nb("lsp-set-addresses", "transit-lr", "router")
	nb("lsp-set-options", "transit-lr", "router-port=lrp-transit")
	nb(m, "lsp-add", "ls-transit", "gw0")
	nb("lsp-set-addresses", "gw0", "02:00:00:c7:00:02 10.199.0.2")
	nb("lrp-set-gateway-chassis", "lrp-transit", "hub-gw", "1")
	nb(m, "lr-route-add", "lr-gw", "10.201.0.0/24", "10.199.0.2")

	dhcp := nb("--bare", "--columns=_uuid", "find", "dhcp_options", "cidr=10.200.0.0/16")
	if dhcp == "" {
		dhcp = nb("dhcp-options-create", "10.200.0.0/16")
	}
	nb("dhcp-options-set-options", dhcp, "server_id=10.200.0.1", "server_mac=02:00:00:c8:00:01", "lease_time=3600",
		"router=10.200.0.1", fmt.Sprintf("mtu=%d", s.GuestMTU), "classless_static_route={10.201.0.0/24,10.200.0.1}")

	want := map[string]bool{}
	var names []string
	for _, p := range s.Ports {
		want[p.Name] = true
		names = append(names, p.Name)
		addr := p.MAC + " " + p.IP
		nb(m, "lsp-add", "ls-mgmt", p.Name)
		nb("lsp-set-addresses", p.Name, addr)
		nb("lsp-set-port-security", p.Name, addr)
		nb("lsp-set-dhcpv4-options", p.Name, dhcp)
	}
	for _, line := range strings.Split(nb("lsp-list", "ls-mgmt"), "\n") {
		i, j := strings.Index(line, "("), strings.LastIndex(line, ")")
		if i < 0 || j < i {
			continue
		}
		name := line[i+1 : j]
		if !want[name] && nb("lsp-get-type", name) == "" {
			nb("lsp-del", name)
		}
	}

	if nb("--bare", "--columns=_uuid", "find", "port_group", "name=pg_vms") == "" {
		nb("pg-add", "pg_vms")
	}
	if len(names) > 0 {
		nb(append([]string{"pg-set-ports", "pg_vms"}, names...)...)
	}
	nb("acl-del", "pg_vms")
	nb("acl-add", "pg_vms", "from-lport", "1002", aclAllow, "allow")
	nb("acl-add", "pg_vms", "from-lport", "1001", aclDrop, "drop")
	return err
}
