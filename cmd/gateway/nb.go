package main

import (
	"errors"
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
	read := func(args ...string) (string, error) {
		out, err := run(append([]string{"ovn-nbctl"}, args...)...)
		if err != nil {
			return "", fmt.Errorf("ovn-nbctl %s: %w", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(out), nil
	}
	findDHCP := func() (string, error) {
		out, err := read("--bare", "--columns=_uuid", "find", "dhcp_options", "cidr=10.200.0.0/16")
		return strings.Join(strings.Fields(out), ""), err
	}
	dhcp, err := findDHCP()
	if err != nil {
		return err
	}
	if dhcp == "" {
		if _, err := read("dhcp-options-create", "10.200.0.0/16"); err != nil {
			return err
		}
		if dhcp, err = findDHCP(); err != nil {
			return err
		}
		if dhcp == "" {
			return errors.New("dhcp options row missing after create")
		}
	}
	if _, err := read("--may-exist", "ls-add", "ls-mgmt"); err != nil {
		return err
	}
	lspList, err := read("lsp-list", "ls-mgmt")
	if err != nil {
		return err
	}
	pgFound, err := read("--bare", "--columns=_uuid", "find", "port_group", "name=pg_vms")
	if err != nil {
		return err
	}

	want := map[string]bool{}
	var names []string
	for _, p := range s.Ports {
		want[p.Name] = true
		names = append(names, p.Name)
	}
	var stale []string
	for _, line := range strings.Split(lspList, "\n") {
		i, j := strings.Index(line, "("), strings.LastIndex(line, ")")
		if i < 0 || j < i {
			continue
		}
		name := line[i+1 : j]
		if want[name] {
			continue
		}
		typ, err := read("lsp-get-type", name)
		if err != nil {
			return err
		}
		if typ == "" {
			stale = append(stale, name)
		}
	}

	// One transaction so a new port is never visible without port security, port group membership and the default-drop ACL.
	m := "--may-exist"
	cmds := [][]string{
		{m, "ls-add", "ls-mgmt"},
		{m, "lr-add", "lr-gw"},
		{m, "lrp-add", "lr-gw", "lrp-mgmt", "02:00:00:c8:00:01", "10.200.0.1/16"},
		{m, "lsp-add", "ls-mgmt", "mgmt-lr"},
		{"lsp-set-type", "mgmt-lr", "router"},
		{"lsp-set-addresses", "mgmt-lr", "router"},
		{"lsp-set-options", "mgmt-lr", "router-port=lrp-mgmt"},
		{m, "ls-add", "ls-transit"},
		{m, "lrp-add", "lr-gw", "lrp-transit", "02:00:00:c7:00:01", "10.199.0.1/30"},
		{m, "lsp-add", "ls-transit", "transit-lr"},
		{"lsp-set-type", "transit-lr", "router"},
		{"lsp-set-addresses", "transit-lr", "router"},
		{"lsp-set-options", "transit-lr", "router-port=lrp-transit"},
		{m, "lsp-add", "ls-transit", "gw0"},
		{"lsp-set-addresses", "gw0", "02:00:00:c7:00:02 10.199.0.2"},
		{"lrp-set-gateway-chassis", "lrp-transit", "hub-gw", "1"},
		{m, "lr-route-add", "lr-gw", "10.201.0.0/24", "10.199.0.2"},
		{"dhcp-options-set-options", dhcp, "server_id=10.200.0.1", "server_mac=02:00:00:c8:00:01", "lease_time=3600",
			"router=10.200.0.1", fmt.Sprintf("mtu=%d", s.GuestMTU), "classless_static_route={10.201.0.0/24,10.200.0.1}"},
	}
	for _, p := range s.Ports {
		addr := p.MAC + " " + p.IP
		cmds = append(cmds,
			[]string{m, "lsp-add", "ls-mgmt", p.Name},
			[]string{"lsp-set-addresses", p.Name, addr},
			[]string{"lsp-set-port-security", p.Name, addr},
			[]string{"lsp-set-dhcpv4-options", p.Name, dhcp})
	}
	if pgFound == "" {
		cmds = append(cmds, []string{"pg-add", "pg_vms"})
	}
	if len(names) > 0 {
		cmds = append(cmds, append([]string{"pg-set-ports", "pg_vms"}, names...))
	}
	for _, n := range stale {
		cmds = append(cmds, []string{"lsp-del", n})
	}
	cmds = append(cmds,
		[]string{"acl-del", "pg_vms"},
		[]string{"acl-add", "pg_vms", "from-lport", "1002", aclAllow, "allow"},
		[]string{"acl-add", "pg_vms", "from-lport", "1001", aclDrop, "drop"})

	args := []string{"ovn-nbctl"}
	for i, c := range cmds {
		if i > 0 {
			args = append(args, "--")
		}
		args = append(args, c...)
	}
	if _, err := run(args...); err != nil {
		return fmt.Errorf("ovn-nbctl transaction: %w", err)
	}
	return nil
}
