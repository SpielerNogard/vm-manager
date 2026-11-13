#!/bin/sh
# vm-manager hub installer: the hub runs on the host and manages gateway and storage in Docker; re-running updates and keeps all data.
set -eu

RELEASES=https://github.com/SpielerNogard/vm-manager/releases
LABEL=dev.vm-manager.hub

usage() {
	echo "usage: install-hub.sh [--version vX.Y.Z]" >&2
	exit 2
}
die() {
	echo "install-hub.sh: $*" >&2
	exit 1
}
say() { echo "==> $*"; }

VERSION=''
while [ $# -gt 0 ]; do
	case $1 in
	--version)
		[ $# -ge 2 ] || usage
		VERSION=$2
		shift 2
		;;
	*) usage ;;
	esac
done

command -v docker >/dev/null 2>&1 || die "Docker is required: the hub runs its gateway and storage server in containers"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is required (docker compose version failed)"

OS=$(uname -s)
ARCH=$(uname -m)
case "$OS/$ARCH" in
Linux/x86_64) PLATFORM=linux-amd64 ;;
Linux/aarch64 | Linux/arm64) PLATFORM=linux-arm64 ;;
Darwin/arm64) PLATFORM=darwin-arm64 ;;
Darwin/*) die "the hub needs an Apple Silicon Mac, this one is $ARCH" ;;
Linux/*) die "the hub needs an amd64 or arm64 Linux host, this one is $ARCH" ;;
*) die "$OS is not supported (Linux or macOS)" ;;
esac

if [ -n "$VERSION" ]; then
	BASE=${VMM_RELEASE_URL:-$RELEASES/download/$VERSION}
else
	BASE=${VMM_RELEASE_URL:-$RELEASES/latest/download}
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
fetch() { curl -fsSL --connect-timeout 10 --max-time 600 -o "$TMP/$1" "$BASE/$1" || die "downloading $1 from $BASE failed"; }
sha256_check() {
	if command -v sha256sum >/dev/null; then sha256sum -c "$@"; else shasum -a 256 -c "$@"; fi
}

BIN=vm-manager-hub-$PLATFORM
say "downloading the hub for $PLATFORM"
fetch SHA256SUMS
fetch "$BIN"
grep "  $BIN\$" "$TMP/SHA256SUMS" >"$TMP/sums" || die "$BIN is missing from SHA256SUMS"
(cd "$TMP" && sha256_check sums >/dev/null) || die "checksum mismatch in the downloaded hub"

# listen_port prints the port of "listen" in hub.json $1, or 8080 before the first start.
listen_port() {
	port=$($2 sed -n 's/.*"listen" *: *"[^"]*:\([0-9][0-9]*\)".*/\1/p' "$1" 2>/dev/null | head -1)
	echo "${port:-8080}"
}

# wait_running waits until the hub answers on port $1 and prints its version.
wait_running() {
	i=0
	while [ $i -lt 30 ]; do
		if answer=$(curl -fsS --connect-timeout 2 "http://127.0.0.1:$1/api/version" 2>/dev/null); then
			printf '%s' "$answer" | sed -n 's/.*"version" *: *"\([^"]*\)".*/\1/p'
			return 0
		fi
		i=$((i + 1))
		sleep 1
	done
	return 1
}

install_linux() {
	SUDO=''
	[ "$(id -u)" = 0 ] || SUDO=sudo
	# root's ~/.ssh is usually empty: the hub's default keys are those of the user who ran the installer.
	ssh_home=$HOME
	[ -z "${SUDO_USER:-}" ] || ssh_home=$(getent passwd "$SUDO_USER" | cut -d: -f6)
	data=/var/lib/vm-manager-hub

	say "loading the kernel modules the gateway needs"
	$SUDO modprobe -a openvswitch wireguard
	printf 'openvswitch\nwireguard\n' | $SUDO tee /etc/modules-load.d/vm-manager-hub.conf >/dev/null

	say "installing the hub"
	$SUDO install -m 0755 "$TMP/$BIN" /usr/local/bin/vm-manager-hub
	$SUDO mkdir -p "$data"
	$SUDO tee /etc/systemd/system/vm-manager-hub.service >/dev/null <<EOF
[Unit]
Description=vm-manager hub
Wants=network-online.target
After=network-online.target docker.service

[Service]
ExecStart=/usr/local/bin/vm-manager-hub -config $data/hub.json -ssh-dir $ssh_home/.ssh -stack
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
	$SUDO systemctl daemon-reload
	$SUDO systemctl enable vm-manager-hub >/dev/null 2>&1
	$SUDO systemctl restart vm-manager-hub
	sleep 1
	PORT=$(listen_port "$data/hub.json" "$SUDO")
	RUNNING=$(wait_running "$PORT") || {
		$SUDO journalctl -u vm-manager-hub -n 20 -o cat >&2 || true
		die "the hub did not answer on port $PORT within 30 s (journalctl -u vm-manager-hub); if the port is taken, change \"listen\" in $data/hub.json and restart the service"
	}
}

install_macos() {
	data=$HOME/.vm-manager-hub
	mkdir -p "$data/bin" "$HOME/Library/LaunchAgents"
	say "installing the hub to $data/bin"
	install -m 0755 "$TMP/$BIN" "$data/bin/vm-manager-hub"
	plist=$HOME/Library/LaunchAgents/$LABEL.plist
	# launchd's default PATH does not contain docker.
	cat >"$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>$LABEL</string>
	<key>ProgramArguments</key>
	<array><string>$data/bin/vm-manager-hub</string><string>-config</string><string>$data/hub.json</string><string>-stack</string></array>
	<key>EnvironmentVariables</key>
	<dict><key>PATH</key><string>$PATH</string></dict>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardOutPath</key><string>$data/hub.log</string>
	<key>StandardErrorPath</key><string>$data/hub.log</string>
</dict>
</plist>
EOF
	uid=$(id -u)
	launchctl bootout "gui/$uid/$LABEL" 2>/dev/null || true
	# bootout returns before the old instance is gone; bootstrap fails until then.
	i=0
	until launchctl bootstrap "gui/$uid" "$plist" 2>/dev/null; do
		i=$((i + 1))
		[ $i -lt 10 ] || die "launchctl bootstrap $plist failed"
		sleep 1
	done
	sleep 1
	PORT=$(listen_port "$data/hub.json" "")
	RUNNING=$(wait_running "$PORT") || {
		tail -n 20 "$data/hub.log" >&2 || true
		die "the hub did not answer on port $PORT within 30 s (log: $data/hub.log); if the port is taken, change \"listen\" in $data/hub.json and run: launchctl kickstart -k gui/$uid/$LABEL"
	}
}

case $PLATFORM in
linux-*) install_linux ;;
darwin-arm64) install_macos ;;
esac
say "hub $RUNNING is running: open http://$(hostname):$PORT (containers are built on the first start; see Settings > Updates)"
