#!/bin/sh
# vm-manager node installer served by the hub (command on the node's GUI page); re-running updates and keeps config, keys and VMs.
set -eu

FIRECRACKER_VERSION=v1.18.0
CLOUD_HYPERVISOR_VERSION=v54.1
CLOUD_HYPERVISOR_SHA256=f9ffb9646783233d1f0fb09e77db1fca66499d1041e63bc011b90683c0d49933
CLOUDHV_EDK2_TAG=ch-97eeb7b09
CLOUDHV_SHA256=dc2fc8f0e43b96712d9fccc52a3a590769606412b3e1bc911d217addd3bef624
RELEASES=https://github.com/SpielerNogard/vm-manager/releases/download
LABEL=dev.vm-manager.node

usage() {
	cat >&2 <<'EOF'
usage: install.sh --hub http://HOST:PORT --token TOKEN|--token-stdin --name NAME --provider vz|firecracker|cloud-hypervisor
                  [--version vX.Y.Z] [--underlay-endpoint HOST:PORT] [--underlay-mtu N] [--memory-mb N]
EOF
	exit 2
}
die() {
	echo "install.sh: $*" >&2
	exit 1
}
say() { echo "==> $*"; }

HUB='' TOKEN='' NAME='' PROVIDER='' VERSION='' UNDERLAY_ENDPOINT='' UNDERLAY_MTU='' MEMORY_MB=''
while [ $# -gt 0 ]; do
	case $1 in
	--hub | --token | --name | --provider | --version | --underlay-endpoint | --underlay-mtu | --memory-mb)
		[ $# -ge 2 ] || usage
		case $1 in
		--hub) HUB=${2%/} ;;
		--token) TOKEN=$2 ;;
		--name) NAME=$2 ;;
		--provider) PROVIDER=$2 ;;
		--version) VERSION=$2 ;;
		--underlay-endpoint) UNDERLAY_ENDPOINT=$2 ;;
		--underlay-mtu) UNDERLAY_MTU=$2 ;;
		--memory-mb) MEMORY_MB=$2 ;;
		esac
		shift 2
		;;
	--token-stdin)
		# The node updates itself this way: a token on the command line would show in process lists.
		IFS= read -r TOKEN || true
		shift
		;;
	*) usage ;;
	esac
done
missing=''
[ -n "$HUB" ] || missing="$missing --hub"
[ -n "$TOKEN" ] || missing="$missing --token"
[ -n "$NAME" ] || missing="$missing --name"
[ -n "$PROVIDER" ] || missing="$missing --provider"
if [ -n "$missing" ]; then
	for m in $missing; do echo "install.sh: $m is required" >&2; done
	usage
fi
case $HUB in
http://*:[0-9]* | https://*:[0-9]*) ;;
*) die "--hub must be http://HOST:PORT, got '$HUB'" ;;
esac

OS=$(uname -s)
ARCH=$(uname -m)
case "$OS/$ARCH/$PROVIDER" in
Linux/x86_64/firecracker | Linux/x86_64/cloud-hypervisor) PLATFORM=linux-amd64 ;;
Darwin/arm64/vz) PLATFORM=darwin-arm64 ;;
Linux/*/firecracker | Linux/*/cloud-hypervisor) die "$PROVIDER nodes need an amd64 (x86_64) Linux host, this one is $ARCH" ;;
Darwin/*/vz) die "vz nodes need an Apple Silicon Mac, this one is $ARCH" ;;
*) die "provider '$PROVIDER' is not supported on $OS (Linux: firecracker or cloud-hypervisor, macOS: vz)" ;;
esac

if [ -z "$VERSION" ]; then
	answer=$(curl -fsS --connect-timeout 10 "$HUB/api/version" 2>/dev/null) || die "cannot reach the hub at $HUB"
	VERSION=$(printf '%s' "$answer" | sed -n 's/.*"version" *: *"\([^"]*\)".*/\1/p')
	[ -n "$VERSION" ] || die "unexpected answer from $HUB/api/version"
	[ "$VERSION" != dev ] || die "the hub at $HUB is a development build; pass --version vX.Y.Z to install a released node"
fi
BASE=${VMM_RELEASE_URL:-$RELEASES/$VERSION}

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
# A stalled download must end: a node update started from the hub waits for this script.
dl() { curl -fsSL --connect-timeout 10 --max-time 600 "$@"; }
fetch() { dl -o "$TMP/$1" "$BASE/$1" || die "downloading $1 of $VERSION failed"; }
sha256_check() {
	if command -v sha256sum >/dev/null; then sha256sum -c "$@"; else shasum -a 256 -c "$@"; fi
}

say "downloading node $VERSION for $PLATFORM"
FILES="vm-manager-node-$PLATFORM"
[ "$PLATFORM" = linux-amd64 ] || FILES="$FILES netagent-linux-arm64"
fetch SHA256SUMS
for f in $FILES; do
	fetch "$f"
	grep "  $f\$" "$TMP/SHA256SUMS" >>"$TMP/sums" || die "$f is missing from SHA256SUMS of $VERSION"
done
(cd "$TMP" && sha256_check sums >/dev/null) || die "checksum mismatch in the downloaded files"

# node_init runs the given `node init` command with the installer's options; the token goes in on stdin, as sudo logs command lines.
node_init() {
	set -- "$@" -hub "$HUB" -token-stdin -name "$NAME" -provider "$PROVIDER"
	[ -z "$UNDERLAY_ENDPOINT" ] || set -- "$@" -underlay-endpoint "$UNDERLAY_ENDPOINT"
	[ -z "$UNDERLAY_MTU" ] || set -- "$@" -underlay-mtu "$UNDERLAY_MTU"
	[ -z "$MEMORY_MB" ] || set -- "$@" -memory-mb "$MEMORY_MB"
	printf '%s\n' "$TOKEN" | "$@"
}

# wait_connected waits until the node logs a hub connection after byte offset $2 of log $1.
wait_connected() {
	i=0
	while [ $i -lt 30 ]; do
		if tail -c +"$(($2 + 1))" "$1" 2>/dev/null | grep -q "connected to hub"; then return 0; fi
		i=$((i + 1))
		sleep 1
	done
	return 1
}

node_journal() {
	if [ -n "$cursor" ]; then
		$SUDO journalctl -u vm-manager-node --after-cursor "$cursor" "$@" 2>/dev/null
	else
		$SUDO journalctl -u vm-manager-node "$@" 2>/dev/null
	fi
}

install_linux() {
	SUDO=''
	[ "$(id -u)" = 0 ] || SUDO=sudo
	[ -c /dev/kvm ] || die "/dev/kvm is missing: $PROVIDER needs KVM (enable virtualization or nested virtualization)"

	say "installing packages"
	$SUDO apt-get update -qq
	$SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq openvswitch-switch ovn-host wireguard-tools qemu-utils nftables >/dev/null

	if [ "$PROVIDER" = cloud-hypervisor ]; then
		fwfile=/usr/local/share/cloud-hypervisor/CLOUDHV.fd
		if [ "$(cloud-hypervisor --version 2>/dev/null | head -1)" != "cloud-hypervisor $CLOUD_HYPERVISOR_VERSION" ] ||
			! echo "$CLOUDHV_SHA256  $fwfile" | sha256_check - >/dev/null 2>&1; then
			say "installing Cloud Hypervisor $CLOUD_HYPERVISOR_VERSION"
			ch=https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/$CLOUD_HYPERVISOR_VERSION/cloud-hypervisor-static
			fw=https://github.com/cloud-hypervisor/edk2/releases/download/$CLOUDHV_EDK2_TAG/CLOUDHV.fd
			if ! { dl -o "$TMP/cloud-hypervisor-static" "$ch" && dl -o "$TMP/CLOUDHV.fd" "$fw"; }; then
				die "downloading Cloud Hypervisor failed"
			fi
			printf '%s  cloud-hypervisor-static\n%s  CLOUDHV.fd\n' "$CLOUD_HYPERVISOR_SHA256" "$CLOUDHV_SHA256" >"$TMP/ch.sha256"
			(cd "$TMP" && sha256_check ch.sha256 >/dev/null) || die "Cloud Hypervisor checksum mismatch"
			$SUDO install -m 0755 "$TMP/cloud-hypervisor-static" /usr/local/bin/cloud-hypervisor
			$SUDO install -D -m 0644 "$TMP/CLOUDHV.fd" "$fwfile"
		fi
	elif [ "$(firecracker --version 2>/dev/null | head -1)" != "Firecracker $FIRECRACKER_VERSION" ] ||
		[ "$(jailer --version 2>/dev/null | head -1)" != "Jailer $FIRECRACKER_VERSION" ]; then
		say "installing Firecracker $FIRECRACKER_VERSION"
		fc=firecracker-$FIRECRACKER_VERSION-x86_64.tgz
		url=https://github.com/firecracker-microvm/firecracker/releases/download/$FIRECRACKER_VERSION
		if ! { dl -o "$TMP/$fc" "$url/$fc" && dl -o "$TMP/$fc.sha256.txt" "$url/$fc.sha256.txt"; }; then
			die "downloading Firecracker failed"
		fi
		(cd "$TMP" && sha256_check "$fc.sha256.txt" >/dev/null) || die "Firecracker checksum mismatch"
		tar -xzf "$TMP/$fc" -C "$TMP"
		$SUDO install -m 0755 "$TMP/release-$FIRECRACKER_VERSION-x86_64/firecracker-$FIRECRACKER_VERSION-x86_64" /usr/local/bin/firecracker
		$SUDO install -m 0755 "$TMP/release-$FIRECRACKER_VERSION-x86_64/jailer-$FIRECRACKER_VERSION-x86_64" /usr/local/bin/jailer
	fi

	say "preparing the host network (bridge vmm0, NAT for 192.168.64.0/24)"
	$SUDO modprobe nbd
	echo nbd | $SUDO tee /etc/modules-load.d/vm-manager.conf >/dev/null
	printf '[NetDev]\nName=vmm0\nKind=bridge\n' | $SUDO tee /etc/systemd/network/vmm0.netdev >/dev/null
	printf '[Match]\nName=vmm0\n[Link]\nRequiredForOnline=no\n[Network]\nAddress=192.168.64.1/24\nConfigureWithoutCarrier=yes\n' |
		$SUDO tee /etc/systemd/network/vmm0.network >/dev/null
	$SUDO systemctl enable --now systemd-networkd >/dev/null 2>&1
	$SUDO networkctl reload
	echo net.ipv4.ip_forward=1 | $SUDO tee /etc/sysctl.d/90-vm-manager.conf >/dev/null
	$SUDO sysctl -q -w net.ipv4.ip_forward=1
	$SUDO mkdir -p /etc/nftables.d
	# Declaring then flushing the table makes loading idempotent and leaves other tables (e.g. Docker's) alone.
	$SUDO tee /etc/nftables.d/vm-manager.nft >/dev/null <<'EOF'
table ip vmm {}
flush table ip vmm
table ip vmm {
  chain post {
    type nat hook postrouting priority 100;
    ip saddr 192.168.64.0/24 ip daddr != 192.168.64.0/24 masquerade
  }
}
EOF
	grep -qs 'include "/etc/nftables.d/\*.nft"' /etc/nftables.conf ||
		echo 'include "/etc/nftables.d/*.nft"' | $SUDO tee -a /etc/nftables.conf >/dev/null
	$SUDO systemctl enable nftables >/dev/null 2>&1
	$SUDO nft -f /etc/nftables.d/vm-manager.nft

	say "installing the node"
	$SUDO install -m 0755 "$TMP/vm-manager-node-linux-amd64" /usr/local/bin/vm-manager-node
	node_init $SUDO /usr/local/bin/vm-manager-node init -config /etc/vm-manager/node.json
	$SUDO tee /etc/systemd/system/vm-manager-node.service >/dev/null <<'EOF'
[Unit]
Description=vm-manager node
Wants=network-online.target
After=network-online.target openvswitch-switch.service ovn-controller.service docker.service

[Service]
ExecStartPre=-/bin/sh -c 'iptables -L DOCKER-USER -n >/dev/null 2>&1 || exit 0; iptables -C DOCKER-USER -i vmm0 -j ACCEPT 2>/dev/null || iptables -I DOCKER-USER -i vmm0 -j ACCEPT; iptables -C DOCKER-USER -o vmm0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || iptables -I DOCKER-USER -o vmm0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT'
ExecStart=/usr/local/bin/vm-manager-node -config /etc/vm-manager/node.json
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
	# Only log lines after this cursor belong to the restarted node.
	cursor=$($SUDO journalctl -u vm-manager-node -n 0 --show-cursor 2>/dev/null | sed -n 's/^-- cursor: //p')
	$SUDO systemctl daemon-reload
	$SUDO systemctl enable vm-manager-node >/dev/null 2>&1
	$SUDO systemctl restart vm-manager-node
	i=0
	while [ $i -lt 30 ]; do
		if node_journal -o cat | grep -q "connected to hub"; then return 0; fi
		i=$((i + 1))
		sleep 1
	done
	logs=$(node_journal -n 20 -o cat || true)
	echo "$logs" >&2
	case $logs in *capacity.memoryMB*) die "the node needs --memory-mb on this host (it has 4 GB of RAM or less); the service keeps retrying until you re-run the installer with it" ;; esac
	die "the node did not connect to $HUB within 30 s (journalctl -u vm-manager-node); the service keeps retrying, re-run the installer after fixing the cause"
}

install_macos() {
	dir=$HOME/.vm-manager
	mkdir -p "$dir/bin" "$HOME/Library/LaunchAgents"
	say "installing the node to $dir/bin"
	install -m 0755 "$TMP/vm-manager-node-darwin-arm64" "$dir/bin/vm-manager-node"
	install -m 0755 "$TMP/netagent-linux-arm64" "$dir/bin/netagent-linux-arm64"
	node_init "$dir/bin/vm-manager-node" init -config "$dir/node.json"
	plist=$HOME/Library/LaunchAgents/$LABEL.plist
	cat >"$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>$LABEL</string>
	<key>ProgramArguments</key>
	<array><string>$dir/bin/vm-manager-node</string><string>-config</string><string>$dir/node.json</string></array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardOutPath</key><string>$dir/node.log</string>
	<key>StandardErrorPath</key><string>$dir/node.log</string>
</dict>
</plist>
EOF
	offset=$(wc -c <"$dir/node.log" 2>/dev/null || echo 0)
	uid=$(id -u)
	launchctl bootout "gui/$uid/$LABEL" 2>/dev/null || true
	# bootout returns before the old instance is gone; bootstrap fails until then.
	i=0
	until launchctl bootstrap "gui/$uid" "$plist" 2>/dev/null; do
		i=$((i + 1))
		[ $i -lt 10 ] || die "launchctl bootstrap $plist failed"
		sleep 1
	done
	# Releases before the rename installed bin/node; launchd now runs the new binary.
	rm -f "$dir/bin/node"
	wait_connected "$dir/node.log" "$offset" || {
		tail -n 20 "$dir/node.log" >&2
		die "the node did not connect to $HUB within 30 s (log: $dir/node.log); for a hub in the LAN, allow Local Network access for 'vm-manager-node' in System Settings"
	}
}

case $PLATFORM in
linux-amd64) install_linux ;;
darwin-arm64) install_macos ;;
esac
say "node $NAME installed ($VERSION); it appears on the hub as online"
