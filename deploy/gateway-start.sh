#!/bin/sh
set -eu

for mod in openvswitch wireguard; do
  [ -d /sys/module/$mod ] || { echo "the Docker host kernel lacks the $mod module" >&2; exit 1; }
done

[ -f /data/hub.json ] || gateway -init-config /data/hub.json

# a restarted container keeps /var/run, so stale pidfiles would make ovn-ctl skip starting the daemons
rm -rf /var/run/ovn /var/run/openvswitch
mkdir -p /var/run/ovn /var/run/openvswitch /var/log/ovn /var/log/openvswitch

link() { ip link show "$1" >/dev/null 2>&1 || ip link add "$1" type wireguard; }
link wg-underlay
wg set wg-underlay listen-port 51820
ip addr replace 10.198.0.1/24 dev wg-underlay
ip link set wg-underlay up
link wg-clients
wg set wg-clients listen-port 51821
ip addr replace 10.201.0.1/24 dev wg-clients
ip link set wg-clients up

/usr/share/ovn/scripts/ovn-ctl start_northd \
  --db-nb-create-insecure-remote=no --db-sb-create-insecure-remote=no
ovn-nbctl set-connection ptcp:6641:127.0.0.1
ovn-sbctl set-connection ptcp:6642:127.0.0.1 ptcp:6642:10.198.0.1

[ -f /etc/openvswitch/conf.db ] || ovsdb-tool create /etc/openvswitch/conf.db /usr/share/openvswitch/vswitch.ovsschema
ovsdb-server --remote=punix:/var/run/openvswitch/db.sock --pidfile --detach --log-file /etc/openvswitch/conf.db
ovs-vsctl --no-wait init
ovs-vswitchd --pidfile --detach --log-file
echo hub-gw > /etc/openvswitch/system-id.conf
ovs-vsctl set open . external_ids:system-id=hub-gw \
  external_ids:ovn-remote=unix:/var/run/ovn/ovnsb_db.sock \
  external_ids:ovn-encap-type=geneve external_ids:ovn-encap-ip=10.198.0.1 \
  external_ids:ovn-bridge-datapath-type=system
ovn-controller --pidfile --detach --log-file unix:/var/run/openvswitch/db.sock

i=0
until ovs-vsctl br-exists br-int; do
  i=$((i + 1))
  [ $i -le 30 ] || { echo "ovn-controller did not create br-int" >&2; exit 1; }
  sleep 1
done
ovs-vsctl --may-exist add-port br-int gw0 -- set interface gw0 type=internal \
  mac='"02:00:00:c7:00:02"' external_ids:iface-id=gw0
ip addr replace 10.199.0.2/30 dev gw0
ip link set gw0 up
ip route replace 10.200.0.0/16 via 10.199.0.1

rule() { iptables -C "$@" 2>/dev/null || iptables -A "$@"; }
rule INPUT -i wg-clients -d 10.201.0.1 -p udp --dport 53 -j ACCEPT
rule INPUT -i wg-clients -d 10.201.0.1 -p tcp --dport 53 -j ACCEPT
rule INPUT -i wg-clients -j DROP
rule INPUT -i gw0 -j DROP
rule FORWARD -i wg-clients ! -o gw0 -j DROP
# VMs may only answer device connections, never open their own
rule FORWARD -i gw0 -o wg-clients -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
rule FORWARD -i gw0 -j DROP

exec gateway -hub http://127.0.0.1:8080 -token-file /data/hub.json -nb unix:/var/run/ovn/ovnnb_db.sock
