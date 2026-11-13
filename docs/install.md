# Install

## Releases

Every push to `main` with `feat`/`fix`/breaking commits gets a release: CI bumps the version with Commitizen, updates `CHANGELOG.md`, tags `vX.Y.Z` and publishes all binaries (`vm-manager-hub`, `vm-manager-node`, `gateway`, `storage`, `netagent` per OS/arch, plus `SHA256SUMS`) on the [releases page](https://github.com/SpielerNogard/vm-manager/releases). Before 1.0, breaking changes bump the minor version. Every pull request checks its commits with `cz check`, so a message that is not a conventional commit fails CI. The release workflow runs the tests before tagging. If a release build still fails after the tag was pushed, re-run the failed jobs of that workflow run; if the code needs a fix, delete the orphan tag (`git push origin :refs/tags/vX.Y.Z`) and the next release-worthy commit on `main` releases a new version.

## Build

```sh
make web hub node   # bin/hub and bin/vm-manager-node; the node is ad-hoc signed with the virtualization entitlement
make test
make web dist dist-darwin checksums VERSION=v0.0.0   # all release binaries into dist/, as CI does
```

Builds outside a release report version `dev`; `GET /api/version` shows the hub's.

`make node-linux` builds `bin/vm-manager-node-linux-amd64` for Linux nodes (Firecracker or Cloud Hypervisor).

`make node` also builds `bin/netagent-linux-arm64`, which must stay next to the `vm-manager-node` binary: the node boots a small helper VM (`<paths.vms>/_netvm`) that runs it.

## Run

There are two ways to run the hub:

| | On the host | In Docker Compose |
|---|---|---|
| Hub | binary on the host, run as a service | container |
| Gateway | container, started and rebuilt by the hub | container |
| Storage server (optional) | container, started and rebuilt by the hub (**Settings › Networking**) | container, compose profile `storage` |
| Data | directory on the host | Docker volume `data` |
| Hub update | one click in **Settings › Updates** | by hand, see [Upgrading](#upgrading) |
| Node update | one click per node | one click per node |

### On the host

The machine needs Docker with Compose v2 (Linux amd64/arm64, or an Apple Silicon Mac). Run:

```sh
curl -fsSL https://github.com/SpielerNogard/vm-manager/releases/latest/download/install-hub.sh | sh
```

The installer downloads the hub of the latest release (verified against `SHA256SUMS`; `--version vX.Y.Z` picks another one), installs it as a service and starts it. On Linux it runs as root or with sudo and also loads the kernel modules the gateway needs (`openvswitch`, `wireguard`). Running it again updates the hub and keeps all data.

| | Linux | macOS |
|---|---|---|
| Binary | `/usr/local/bin/vm-manager-hub` | `~/.vm-manager-hub/bin/vm-manager-hub` |
| Data | `/var/lib/vm-manager-hub` | `~/.vm-manager-hub` |
| Service | systemd `vm-manager-hub` (`journalctl -u vm-manager-hub`) | launchd agent `dev.vm-manager.hub` (log `~/.vm-manager-hub/hub.log`) |

On its first start the hub builds the gateway image from the binaries of its own release and starts it; **Settings › Updates** shows whether the containers run, and the error with a **Retry** if not (the hub also retries every minute, for example when Docker starts after the hub). The hub controls Docker, which amounts to root on the host.

The hub listens on port 8080; to change it, set `"listen"` in `hub.json` in the data directory and restart the service. The published WireGuard and storage ports come from `hub.json` as well (`underlay.port`, `clients.port`, `storage.port`). The containers reach the hub on the host: on a Linux host whose firewall drops incoming traffic, allow the hub port for the Docker bridge. The hub's default SSH keys are those of the user who ran the installer.

Everything below about setup, settings and nodes applies to both ways.

### In Docker Compose

Hub and gateway run as a compose stack (netns holder, OVN/WireGuard gateway, hub). The images are built from the binaries of the release named in `deploy/.env`:

```sh
cp deploy/.env.example deploy/.env   # set VMM_VERSION, e.g. v0.1.0
docker compose -f deploy/compose.yaml up -d --build
```

For development, `make stack` builds the same images from the working tree instead (`VMM_BIN=source`, version `dev`). The image build needs BuildKit (default since Docker 23).

On a Linux Docker host, load the kernel modules the gateway needs first: `modprobe -a openvswitch wireguard` (persist them in `/etc/modules-load.d/`); Docker Desktop on the Mac has them.

No config is needed. On first start the hub creates `/data/hub.json` in the `data` volume with a random node token and an empty client endpoint. Open `http://<hub>:8080`: every page except **Settings** redirects there until the client endpoint is set (the API answers POST/PUT/DELETE with 409 meanwhile, except settings, node transport and gateway routes).

Settings page:

| Tab | |
|---|---|
| Networking | Client endpoint (host name or IP, no scheme or port) that devices connect to. Storage endpoint (`host:port`) nodes use for disks; empty means the client endpoint with the storage port. Ports and listen address are shown read-only |
| Security | Node token: show, copy, regenerate or set (at least 16 characters) |
| Guests | Guest mgmt MTU (576-9000), delivered by OVN DHCP |
| Updates | Versions of hub, gateway and storage, the latest release, and how to update |

Changes apply without a restart; the gateway re-reads the token automatically.

`deploy/.env` sets the release (`VMM_VERSION`), the published ports and the SSH dir; the hub writes the ports into `hub.json` on start:

| Variable | Default | |
|---|---|---|
| `VMM_VERSION` | none, required to build the images | Release whose binaries the images use, e.g. `v0.1.0` |
| `VMM_RELEASE_URL` | the GitHub release of `VMM_VERSION` | Base URL of the release files, for a mirror |
| `HUB_PORT` | `8080` | Published hub API/GUI port (tcp) |
| `UNDERLAY_PORT` | `51820` | Published WireGuard port for nodes (udp) |
| `CLIENTS_PORT` | `51821` | Published WireGuard port for devices (udp) |
| `HUB_SSH_DIR` | `~/.ssh` | Directory with the default SSH public keys offered as hub keys |
| `STORAGE_PORT` | `10809` | Published NBD port of the storage server (tcp); also the port of the default storage endpoint |

To change a port, set it in `deploy/.env` and run `docker compose -f deploy/compose.yaml up -d`. Existing device configs and the nodes' `underlay.endpoint` must be updated to the new ports as well.

Ports to open on the hub host: nodes need tcp `HUB_PORT` and udp `UNDERLAY_PORT`; devices need udp `CLIENTS_PORT` at the client endpoint; nodes need tcp `STORAGE_PORT` if you run the storage server. To run the hub without the stack, start `./bin/hub`; it creates `~/.vm-manager/hub.json` on first start (random token, empty client endpoint) and you finish setup in the GUI. Without the gateway container, VMs get no network.

Add a node: on the **Nodes** page click **Add node**, enter a name (lowercase letters, digits, hyphens) and the hypervisor: `vz` for an Apple Silicon Mac, `firecracker` or `cloud-hypervisor` for an amd64 Linux host with KVM (Debian 13 or Ubuntu 24.04). The node appears as `pending`; its detail page shows the install command. The hub address in it is editable and defaults to the client endpoint; use `localhost` on the hub's own Mac, or a LAN IP if nodes reach the hub differently. Run the command on the machine (on Linux as root or a user with sudo):

```sh
curl -fsSL http://<hub>:8080/node/install.sh | sh -s -- --hub http://<hub>:8080 --token <token> --name <name> --provider firecracker
```

The installer downloads the node of the hub's release (verified against `SHA256SUMS`), writes the config, prepares the host and starts the node as a service; it is `online` when the command finishes. Running it again updates the node and keeps its config, chassis key, images and VMs; the VMs restart with the node. Optional arguments: `--memory-mb N` (needed on hosts with 4 GB of RAM or less), `--underlay-endpoint host:port`, `--underlay-mtu N`, and `--version vX.Y.Z` (required if the hub itself is a `dev` build). `--token-stdin` reads the token from the first line of stdin instead of `--token`; it needs the script as a file, not piped into `sh`.

| | Linux (`firecracker`, `cloud-hypervisor`) | macOS (`vz`) |
|---|---|---|
| Binary | `/usr/local/bin/vm-manager-node` | `~/.vm-manager/bin/vm-manager-node` |
| Config | `/etc/vm-manager/node.json` | `~/.vm-manager/node.json` |
| Data | `/var/lib/vm-manager/{vms,images}` | `~/.vm-manager/{vms,images}` |
| Service | systemd `vm-manager-node` (`journalctl -u vm-manager-node`) | launchd agent `dev.vm-manager.node` (log `~/.vm-manager/node.log`) |

On macOS, a hub in the LAN needs Local Network access for `vm-manager-node` (System Settings → Privacy & Security → Local Network); macOS asks on the first connection. Later updates keep the access, because the binary carries a fixed signing identifier; the first update from v0.4.0 or older asks once more.

The detail page also shows the plain `node.json` under **Manual setup**. Save it as `~/.vm-manager/node.json` on the node and start it:

```sh
./bin/vm-manager-node   # config defaults to ~/.vm-manager/node.json
```

Node states are `pending` (added, never connected), `online` and `offline`. Nodes without VMs can be deleted in any state; a deleted node that keeps running reconnects and appears again. Unknown nodes can also register themselves with the token. For a manual config copy `node.example.json` and set `hub.url` (`ws://<hub>:<HUB_PORT>/api/nodes/connect`), the same token and paths.

A loopback `hub.url` works for a node on the same Mac as the hub: the vz provider rewrites it to the vmnet gateway for the helper VM. If nodes reach the hub under another address (for example over Tailscale), set `underlay.endpoint` to `<host>:<UNDERLAY_PORT>`, using the published host port. By default the node uses the host of `hub.url` and the hub's `underlay.port`. Behind Tailscale also add `"underlay": {"mtu": 1200}` to `node.json` and set the guest MTU to 1140 or less in Settings. Guest mgmt NICs are IPv4-only (IPv6 is disabled on them), so the DHCP MTU is not raised to 1280.

Open `http://<hub>:8080`, add your SSH public key under **SSH-Keys** (the hub's own default keys are already there), then pick a node and create a VM. You can also use the API:

```sh
curl -X POST http://<hub>:8080/api/keys -d '{"name": "laptop", "publicKey": "'"$(cat ~/.ssh/id_ed25519.pub)"'"}'
# → {"id": "<key-id>", ...}
curl -X POST http://<hub>:8080/api/vms -d '{
  "nodeId": "mac-mini-1", "name": "dev", "cpus": 2, "memoryMB": 2048,
  "diskGB": 20, "sshKeyIds": ["<key-id>"]
}'
```

## Linux node (Firecracker, Cloud Hypervisor)

Linux nodes are amd64 hosts with KVM (Debian 13 or Ubuntu 24.04) using the `firecracker` or `cloud-hypervisor` provider; both share the host setup below and differ only in the VMM. OVS and OVN run on the host itself, there is no helper VM. The installer does all of this. To set the host up by hand instead, as root:

```sh
apt-get install -y openvswitch-switch ovn-host wireguard-tools qemu-utils nftables
curl -fsSL https://github.com/firecracker-microvm/firecracker/releases/download/v1.18.0/firecracker-v1.18.0-x86_64.tgz | tar -xz -C /tmp
install /tmp/release-v1.18.0-x86_64/firecracker-v1.18.0-x86_64 /usr/local/bin/firecracker
install /tmp/release-v1.18.0-x86_64/jailer-v1.18.0-x86_64 /usr/local/bin/jailer
```

For `cloud-hypervisor` install the VMM and its edk2 firmware instead of Firecracker:

```sh
curl -fsSL -o /usr/local/bin/cloud-hypervisor https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v54.1/cloud-hypervisor-static
chmod 0755 /usr/local/bin/cloud-hypervisor
mkdir -p /usr/local/share/cloud-hypervisor
curl -fsSL -o /usr/local/share/cloud-hypervisor/CLOUDHV.fd https://github.com/cloud-hypervisor/edk2/releases/download/ch-97eeb7b09/CLOUDHV.fd
```

Then, for both:

```sh
modprobe nbd && echo nbd > /etc/modules-load.d/nbd.conf

# NAT bridge for the VMs' first NIC (internet); the node assigns 192.168.64.2-254 statically
cat > /etc/systemd/network/vmm0.netdev <<'EOT'
[NetDev]
Name=vmm0
Kind=bridge
EOT
cat > /etc/systemd/network/vmm0.network <<'EOT'
[Match]
Name=vmm0
[Link]
RequiredForOnline=no
[Network]
Address=192.168.64.1/24
ConfigureWithoutCarrier=yes
EOT
systemctl enable --now systemd-networkd && networkctl reload
echo net.ipv4.ip_forward=1 > /etc/sysctl.d/90-vmm.conf && sysctl --system
mkdir -p /etc/nftables.d
cat > /etc/nftables.d/vm-manager.nft <<'EOT'
table ip vmm {}
flush table ip vmm
table ip vmm {
  chain post {
    type nat hook postrouting priority 100;
    ip saddr 192.168.64.0/24 ip daddr != 192.168.64.0/24 masquerade
  }
}
EOT
grep -qs 'include "/etc/nftables.d/\*.nft"' /etc/nftables.conf || echo 'include "/etc/nftables.d/*.nft"' >> /etc/nftables.conf
systemctl enable nftables && nft -f /etc/nftables.d/vm-manager.nft   # restarting nftables would flush Docker's rules
```

Copy `bin/vm-manager-node-linux-amd64` to `/usr/local/bin/vm-manager-node` and use a `node.json` with `"provider": "firecracker"` or `"cloud-hypervisor"` (see `node.firecracker.example.json` and `node.cloud-hypervisor.example.json`; `capacity.memoryMB` defaults to the host memory minus 4 GB). The node runs as root because it creates taps, NBD devices and the OVN chassis. It refuses to start if the VMM (for `firecracker` also its `jailer`), `qemu-nbd`, `/dev/kvm`, the `vmm0` bridge or Open vSwitch are missing.

Differences to vz nodes: a VM's NAT NIC gets a static address on `vmm0` instead of DHCP. On `firecracker` nodes, guest NICs are named `eth0`, `eth1`, ... instead of `enp0s1`, .... Firecracker has no ACPI power-off, so after a `poweroff` inside the guest the node ends the halted Firecracker process. Each Firecracker runs under the jailer: chrooted into a jail below `<paths.vms>/<vm>/jail` and as its own unprivileged UID and GID (70000 plus the last octet of the VM's NAT address, so 70002 to 70254 must be unused on the host), not as root. `paths.vms` must not be on a filesystem mounted `nodev`, and the node sets each VM directory to mode 0700.

Differences of `cloud-hypervisor` nodes to Firecracker nodes: guest NICs get PCI names such as `ens3`, `ens4` instead of `eth0`, `eth1`, ...; **Shut down** reaches the guest as an ACPI power-button press; and a reboot inside the guest keeps the VM running (the VMM resets in place) instead of restarting its process. As on Firecracker, a kernel panic restarts the VM (here the node watches the console for it). Switching an existing Firecracker host to `cloud-hypervisor` is unsupported: use a fresh host or delete its VMs first.

## First start

On first use, the node downloads the Ubuntu cloud image into `paths.images`, which takes a few minutes. VMs created before this version used per-VM SSH ports and must be recreated. `sshPort` and `sshPorts` are gone.

## Upgrading

The hub works with older nodes, so upgrade the hub first and each node afterwards. **Settings › Updates** shows the latest release and the versions of hub, gateway and storage.

1. Hub: update the checkout to the release (`git fetch --tags && git checkout vX.Y.Z`; the images are built from its `deploy/` files), set `VMM_VERSION` in `deploy/.env` to the same release and run `docker compose -f deploy/compose.yaml up -d --build`.
2. Nodes: every node older than the hub shows **update available**. Click **Update node** on its page, or re-run its install command. The node's VMs restart. A node is never newer than its hub.

A node that was started by hand, without the installer, has no **Update node**; install it with its install command once. Linux nodes newer than v0.5.0 need `qemu-utils` (the installer adds it; on a host set up by hand run `apt-get install qemu-utils` before replacing the binary).

Upgrading to v0.5.0 from an older release is still one joint upgrade: older versions do not work with newer ones and have no **Update node**. Upgrade the hub as above, then re-run the install command on every node. The release assets were renamed (`vm-manager-node-*`, `vm-manager-hub-*`), so the checkout must be updated before the build, and a macOS node asks for Local Network access once more, now as `vm-manager-node`.

Local data disks (`dataDisks`) were removed without migration: recreate the affected VMs and move their data to standalone disks. VMs created before the hub became the source of truth are deleted when their node first connects; recreate them. VMs created before network support may need one extra reboot after their first network change before the new NIC comes up (cloud-init applies the boot-time network update only from the next boot).
