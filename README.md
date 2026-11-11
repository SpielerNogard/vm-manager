# vm-manager

A hub that manages lightweight Linux VMs on nodes. You create VMs on the hub, via the REST API or the web GUI. Each VM then boots on the node you picked and joins an OVN management network, which you reach from your devices over WireGuard.

This is a proof of concept. Nodes are Apple Silicon Macs using Virtualization.framework (`vz` provider), and guests run Ubuntu 24.04.

## Requirements

- Hub stack: Docker with Compose. Supported hosts are OrbStack on macOS and Docker on Linux. The Docker host kernel must have the `openvswitch` and `wireguard` modules loaded (the gateway container fails fast otherwise). Docker Desktop is not supported or tested.
- Build: Go and Node.js/npm.
- Node: Apple Silicon, macOS 13 or later, Go with cgo (Xcode Command Line Tools).

## Build

```sh
make web hub node   # node binary is ad-hoc signed with the virtualization entitlement
make test
```

`make node` also builds `bin/netagent-linux-arm64`, which must stay next to the `node` binary: the node boots a small helper VM (`<paths.vms>/_netvm`) that runs it.

## Run

Hub and gateway run as a compose stack (netns holder, OVN/WireGuard gateway, hub):

```sh
cp deploy/.env.example deploy/.env   # set HUB_TOKEN and CLIENTS_ENDPOINT
make stack                           # docker compose -f deploy/compose.yaml up -d --build
```

| Variable | Default | |
|---|---|---|
| `HUB_TOKEN` | none | Shared hub/node token. Only read on first start, when `/data/hub.json` does not exist yet |
| `CLIENTS_ENDPOINT` | none | Address devices use to reach the clients WireGuard port |
| `HUB_PORT` | `8080` | Published hub API/GUI port (tcp) |
| `UNDERLAY_PORT` | `51820` | Published WireGuard port for nodes (udp) |
| `CLIENTS_PORT` | `51821` | Published WireGuard port for devices (udp) |
| `HUB_SSH_DIR` | `~/.ssh` | Directory with the default SSH public keys offered as hub keys |

The hub config lives in the `data` volume as `/data/hub.json`, written by the gateway on first start from these variables. Editing `.env` later does not change it. To change hub settings afterwards (`guestMTU`, `clients.endpoint`, ports), copy the file out, edit it and copy it back (the gateway image has no editor), then restart the hub:

```sh
docker compose -f deploy/compose.yaml cp gateway:/data/hub.json hub.json
$EDITOR hub.json
docker compose -f deploy/compose.yaml cp hub.json gateway:/data/hub.json
docker compose -f deploy/compose.yaml restart hub
```

Ports to open on the hub host: nodes need tcp `HUB_PORT` and udp `UNDERLAY_PORT`; devices need udp `CLIENTS_PORT` at `CLIENTS_ENDPOINT`. To run the hub without the stack, copy `hub.example.json` to `~/.vm-manager/hub.json` and start `./bin/hub`. Without the gateway container, VMs get no network.

Node (config defaults to `~/.vm-manager/node.json`):

```sh
cp node.example.json ~/.vm-manager/node.json  # set hub.url, the same token, paths
./bin/node
```

Set `hub.url` to `ws://<hub>:<HUB_PORT>/api/nodes/connect`. A loopback `hub.url` works for a node on the same Mac as the hub: the vz provider rewrites it to the vmnet gateway for the helper VM. If nodes reach the hub under another address (for example over Tailscale), set `underlay.endpoint` to `<host>:<UNDERLAY_PORT>`, using the published host port. By default the node uses the host of `hub.url` and the hub's `underlay.port`. Behind Tailscale also set `underlay.mtu` to 1200 and the hub's `guestMTU` to 1140 or less (edit `/data/hub.json` as above).

Open `http://<hub>:8080`, add your SSH public key under **SSH-Keys** (the hub's own default keys are already there), then pick a node and create a VM. You can also use the API:

```sh
curl -X POST http://<hub>:8080/api/keys -d '{"name": "laptop", "publicKey": "'"$(cat ~/.ssh/id_ed25519.pub)"'"}'
# → {"id": "<key-id>", ...}
curl -X POST http://<hub>:8080/api/vms -d '{
  "nodeId": "mac-mini-1", "name": "dev", "cpus": 2, "memoryMB": 2048,
  "diskGB": 20, "dataDisks": [{"name": "data", "mountPath": "/data", "sizeGB": 10}], "sshKeyIds": ["<key-id>"]
}'
```

### Reaching VMs

VMs sit on the management network `10.200.0.0/16`, named `<vm-name>.mgmt.internal`. To reach them, add a device (GUI, or `POST /api/devices` with `name`), which returns a WireGuard config (also downloadable again via `GET /api/devices/{id}/config` or the GUI). Import it into a WireGuard client, then, when the VM is `running`:

```sh
ssh ubuntu@dev.mgmt.internal
```

The device config points DNS at the gateway, which resolves `*.mgmt.internal` and forwards other names upstream. Adding devices requires `clients.endpoint` (`CLIENTS_ENDPOINT`).

On first use, the node downloads the Ubuntu cloud image into `paths.images`, which takes a few minutes. On first boot, cloud-init formats each data disk as ext4, labels it with its `name`, and mounts it at `mountPath`. The mount is owned by `ubuntu`. System paths such as `/etc`, `/usr`, `/home/ubuntu` or `/var` are rejected as mount paths.

VMs created before this version used per-VM SSH ports and must be recreated. `sshPort` and `sshPorts` are gone.

## Config

**Hub** (`-config`, default `~/.vm-manager/hub.json`; `-ssh-dir`, default `~/.ssh`):

| Key | Default | |
|---|---|---|
| `token` | required | Shared secret with nodes |
| `listen` | `:8080` | HTTP listen address |
| `underlay.port` | `51820` | Published underlay WireGuard port, handed to nodes |
| `clients.endpoint` | none | Address devices connect to; required to add devices |
| `clients.port` | `51821` | Published clients WireGuard port |
| `guestMTU` | `1300` | Guest mgmt MTU, delivered by OVN DHCP |

**Node:** the required keys are `hub.url`, `hub.token`, `provider` (`vz`), `paths.vms` and `paths.images`. `nodeId` is optional and defaults to the hostname. `capacity.cpus`/`memoryMB` are optional and default to the hardware, minus 4 GB of RAM. `underlay.endpoint` overrides the hub's WireGuard endpoint (default: the host of `hub.url` plus the hub's underlay port). `underlay.mtu` is the `wg-underlay` MTU (default 1420). The node's WireGuard chassis key is stored in `<paths.vms>/chassis.key`.

Provider settings go in a section named after the provider. For `vz` these are `vz.imageURL` and `vz.leasesPath`.

## API

| Method | Path | |
|---|---|---|
| GET | `/api/nodes` | Nodes with provider, capacity, online state, machine info, chassis state, and their config file without the hub token |
| GET | `/api/vms` | VMs with state and IP |
| POST | `/api/vms` | Create a VM (`nodeId`, `name`, `cpus`, `memoryMB`, `diskGB`, `dataDisks` (`name`, `mountPath`, `sizeGB`), `sshKeyIds`) |
| DELETE | `/api/vms/{id}` | Stop the VM and delete its disks |
| GET | `/api/keys` | Stored SSH public keys with type and fingerprint |
| POST | `/api/keys` | Store a key (`name`, `publicKey`) |
| DELETE | `/api/keys/{id}` | Remove a stored key. VMs that already have it keep it |
| GET | `/api/networks` | The management network with subnet and member count |
| GET | `/api/devices` | Client devices |
| POST | `/api/devices` | Add a device (`name`), returns the device and its WireGuard config |
| DELETE | `/api/devices/{id}` | Remove a device |
| GET | `/api/devices/{id}/config` | Download the device's WireGuard config |
| GET | `/api/gateway/state` | Internal, token-protected: desired network state for the gateway |
| POST | `/api/gateway/status` | Internal, token-protected: gateway status report |

## Behaviour

- The hub keeps SSH keys in `keys.json` and the network model (WireGuard keys, VM NICs, chassis, devices) in `network.json`, both next to its config file. Everything else is in memory.
- The hub also lists its own user's default public keys (`~/.ssh/id_ed25519.pub`, `id_ecdsa.pub`, `id_rsa.pub`, …), read on each start. They cannot be deleted through the API and are preselected in the GUI. After a hub restart, nodes report their VMs again.
- The node stores each VM in `paths.vms/<id>/vm.json`. After a node restart, the node reboots its VMs from their existing disks.
- Nodes join the gateway over a WireGuard underlay (`10.198.0.0/24`); OVN Geneve tunnels run on top of it. VMs get a management NIC on `10.200.0.0/16`, devices get addresses in `10.201.0.0/24`.
- The hub is meant for a trusted home network. Node↔hub traffic is plain `ws://` with a shared token, and the GUI and API, including device creation, have no user auth.
- WireGuard devices can only reach the VMs' management IPs and the gateway DNS. The hub API and the OVN databases are firewalled from them.
- The hub stores device private keys, so configs can be downloaded again.
- VMs are isolated from each other on the management network only. The vz NAT NIC (`eth0`, internet) is still shared.
