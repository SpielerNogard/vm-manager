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
make stack   # docker compose -f deploy/compose.yaml up -d --build
```

No config is needed. On first start the hub creates `/data/hub.json` in the `data` volume with a random node token and an empty client endpoint. Open `http://<hub>:8080`: every page except **Settings** redirects there until the client endpoint is set (the API answers POST/PUT/DELETE with 409 meanwhile, except settings, node transport and gateway routes).

Settings page:

| Tab | |
|---|---|
| Networking | Client endpoint (host name or IP, no scheme or port) that devices connect to. Ports and listen address are shown read-only |
| Security | Node token: show, copy, regenerate or set (at least 16 characters) |
| Guests | Guest mgmt MTU (576-9000), delivered by OVN DHCP |

Changes apply without a restart; the gateway re-reads the token automatically.

`deploy/.env` is optional and only sets the published ports (and the SSH dir); the hub writes the ports into `hub.json` on start:

| Variable | Default | |
|---|---|---|
| `HUB_PORT` | `8080` | Published hub API/GUI port (tcp) |
| `UNDERLAY_PORT` | `51820` | Published WireGuard port for nodes (udp) |
| `CLIENTS_PORT` | `51821` | Published WireGuard port for devices (udp) |
| `HUB_SSH_DIR` | `~/.ssh` | Directory with the default SSH public keys offered as hub keys |

To change a port, set it in `deploy/.env` and run `docker compose -f deploy/compose.yaml up -d`. Existing device configs and the nodes' `underlay.endpoint` must be updated to the new ports as well.

Ports to open on the hub host: nodes need tcp `HUB_PORT` and udp `UNDERLAY_PORT`; devices need udp `CLIENTS_PORT` at the client endpoint. To run the hub without the stack, copy `hub.example.json` to `~/.vm-manager/hub.json` and start `./bin/hub`. Without the gateway container, VMs get no network.

Add a node: on the **Nodes** page click **Add node** (name: lowercase letters, digits, hyphens). The node appears as `pending` and its detail page shows its `node.json` with copy and download. The hub address in it is editable and defaults to the client endpoint; use `localhost` on the hub's own Mac, or a LAN IP if nodes reach the hub differently. Save it as `~/.vm-manager/node.json` on the node and start it:

```sh
./bin/node   # config defaults to ~/.vm-manager/node.json
```

Node states are `pending` (added, never connected), `online` and `offline`. Nodes without VMs can be deleted in any state; a deleted node that keeps running reconnects and appears again. Unknown nodes can also register themselves with the token. For a manual config copy `node.example.json` and set `hub.url` (`ws://<hub>:<HUB_PORT>/api/nodes/connect`), the same token and paths.

A loopback `hub.url` works for a node on the same Mac as the hub: the vz provider rewrites it to the vmnet gateway for the helper VM. If nodes reach the hub under another address (for example over Tailscale), set `underlay.endpoint` to `<host>:<UNDERLAY_PORT>`, using the published host port. By default the node uses the host of `hub.url` and the hub's `underlay.port`. Behind Tailscale also add `"underlay": {"mtu": 1200}` to `node.json` and set the guest MTU to 1140 or less in Settings. Guest mgmt NICs are IPv4-only (IPv6 is disabled on them), so the DHCP MTU is not raised to 1280.

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

The device config points DNS at the gateway, which resolves `*.mgmt.internal` and forwards other names upstream. Adding devices requires the client endpoint (Settings).

Client notes:
- For a device on the hub machine itself, tick **This device is the hub machine** (preselected when the GUI is opened via `localhost`), or pass `"onHub": true`. Its config then uses `127.0.0.1:<CLIENTS_PORT>` as endpoint, because the WireGuard app on macOS doesn't reach the machine's own Tailscale or LAN address (on Linux either works). Such a device doesn't need the client endpoint.
- On macOS the WireGuard app uses the tunnel DNS only as a supplemental resolver for split tunnels, so `*.internal` doesn't resolve. Route the zone to the gateway once:

  ```sh
  sudo sh -c 'mkdir -p /etc/resolver && echo "nameserver 10.201.0.1" > /etc/resolver/internal'
  ```

### Networks

Networks are standalone objects (they can be empty) that give VMs private connectivity to each other, also across nodes (OVN Geneve). Create one with `POST /api/networks` or the GUI, and attach VMs at creation (`networkIds` on `POST /api/vms`) or later with `PUT /api/vms/{id}/networks?restartAllowed=true` and `{"networkIds": [...]}`, which sets the full list (`networkIds` is required, `[]` detaches all; a missing field is a 400). Changing the list restarts a running VM.

| | |
|---|---|
| Subnet | Automatic `/24` from `10.210.0.0/16`, or a custom RFC1918 IPv4 subnet from `/16` to `/28` |
| Overlap | Must not overlap `10.198.0.0/24`, `10.199.0.0/30`, `10.200.0.0/16`, `10.201.0.0/24`, `192.168.64.0/24` or another network |
| Addresses | VMs get stable addresses from `.10` |
| Isolation | VMs reach each other only via shared networks. No router, no internet, no device (WireGuard) access, no routing between networks |
| DNS | `<vm>.<network>.internal` resolves to the VM's IP in that network. Devices resolve these names too but cannot reach the addresses. VMs can also resolve all networks' names but reach only their own |
| Guest | Each network is an extra NIC configured by a netplan entry matched by MAC, DHCP from OVN, no default route. Guest interface names stay kernel names like `enp0s3` |

Guests send only `*.internal` queries to the gateway (`10.201.0.1`, via the management NIC). The management network is built in and cannot be deleted; a network with members cannot be deleted either.

On first use, the node downloads the Ubuntu cloud image into `paths.images`, which takes a few minutes. On first boot, cloud-init formats each data disk as ext4, labels it with its `name`, and mounts it at `mountPath`. The mount is owned by `ubuntu`. System paths such as `/etc`, `/usr`, `/home/ubuntu` or `/var` are rejected as mount paths.

VMs created before this version used per-VM SSH ports and must be recreated. `sshPort` and `sshPorts` are gone.

Upgrade note: VMs created before the hub became the source of truth are deleted when their node first connects. Recreate them. Upgrade the hub and all nodes together; old and new versions don't work with each other. VMs created before network support may need one extra reboot after their first network change before the new NIC comes up (cloud-init applies the boot-time network update only from the next boot).

## Config

**Hub** (`-config`, default `~/.vm-manager/hub.json`; `-ssh-dir`, default `~/.ssh`). Created on first start and edited through Settings:

| Key | Default | |
|---|---|---|
| `token` | random | Shared secret with nodes |
| `listen` | `:8080` | HTTP listen address |
| `underlay.port` | `51820` | Published underlay WireGuard port, handed to nodes |
| `clients.endpoint` | empty | Address devices connect to; the GUI stays locked until set |
| `clients.port` | `51821` | Published clients WireGuard port |
| `guestMTU` | `1300` | Guest mgmt MTU, delivered by OVN DHCP |

**Node:** the required keys are `hub.url`, `hub.token`, `provider` (`vz`), `paths.vms` and `paths.images`. `nodeId` is optional and defaults to the hostname. Set it explicitly: if it changes (or the hostname changes while it is unset), the hub refuses the node because its VMs belong to the old ID. The node keeps its VMs and retries. `capacity.cpus`/`memoryMB` are optional and default to the hardware, minus 4 GB of RAM. `underlay.endpoint` overrides the hub's WireGuard endpoint (default: the host of `hub.url` plus the hub's underlay port). `underlay.mtu` is the `wg-underlay` MTU (default 1420). The node's WireGuard chassis key is stored in `<paths.vms>/chassis.key`.

Provider settings go in a section named after the provider. For `vz` these are `vz.imageURL` and `vz.leasesPath`.

## API

| Method | Path | |
|---|---|---|
| GET | `/api/nodes` | Nodes with provider, capacity, `state` (`pending`/`online`/`offline`), machine info, chassis state, and their config file without the hub token |
| POST | `/api/nodes` | Add a node (`{"id"}`), state `pending` |
| DELETE | `/api/nodes/{id}` | Delete a node without VMs |
| GET | `/api/nodes/{id}/config?host=` | The node's `node.json`; `host` overrides the hub address (default: client endpoint) |
| GET | `/api/settings` | Client endpoint, ports, listen address, guest MTU, node token |
| PUT | `/api/settings` | Update client endpoint and guest MTU |
| POST | `/api/settings/token` | Regenerate the node token, or set it (`{"token"}`, at least 16 characters) |
| GET | `/api/vms` | VMs with state, desired power and IP |
| GET | `/api/vms/{id}` | One VM, same fields as in the list; 404 if unknown |
| POST | `/api/vms` | Create a VM (`nodeId`, `name`, `cpus`, `memoryMB`, `diskGB`, `dataDisks` (`name`, `mountPath`, `sizeGB`), `sshKeyIds`, optional `networkIds`). Also works for an offline node: the VM shows `pending` until the node connects |
| DELETE | `/api/vms/{id}` | Stop the VM and delete its disks. Also works for an offline node: the VM is removed from the hub at once and the node deletes it when it reconnects |
| PUT | `/api/vms/{id}/networks?restartAllowed=true` | Set the VM's full network list (`{"networkIds": [...]}`, required; `[]` detaches all). A VM whose desired power is running is restarted and needs `restartAllowed=true`, else 409. Stopped VMs need no flag |
| POST | `/api/vms/{id}/start` | Power the VM on |
| POST | `/api/vms/{id}/shutdown` | ACPI shutdown, hard power-off after 10 s |
| POST | `/api/vms/{id}/stop` | Immediate hard power-off |
| GET | `/api/keys` | Stored SSH public keys with type and fingerprint |
| POST | `/api/keys` | Store a key (`name`, `publicKey`) |
| DELETE | `/api/keys/{id}` | Remove a stored key. VMs that already have it keep it |
| GET | `/api/networks` | Networks with subnet and members (VM IDs), management network first, marked builtin |
| GET | `/api/networks/{id}` | One network, same fields as in the list (`mgmt` included); 404 if unknown |
| POST | `/api/networks` | Create a network (`name`, optional `subnet`) |
| DELETE | `/api/networks/{id}` | Delete a network. 409 while it has members; the management network cannot be deleted |
| GET | `/api/devices` | Client devices |
| POST | `/api/devices` | Add a device (`name`, optional `onHub` for a device on the hub machine), returns the device and its WireGuard config |
| DELETE | `/api/devices/{id}` | Remove a device |
| GET | `/api/devices/{id}/config` | Download the device's WireGuard config |
| GET | `/api/gateway/state` | Internal, token-protected: desired network state for the gateway |
| POST | `/api/gateway/status` | Internal, token-protected: gateway status report |

## Behaviour

- The hub keeps SSH keys in `keys.json`, the network model (WireGuard keys, VM NICs, chassis, devices) in `network.json`, and VMs and nodes in `vms.json`, all next to its config file. The hub is the source of truth for VMs.
- The hub also lists its own user's default public keys (`~/.ssh/id_ed25519.pub`, `id_ecdsa.pub`, `id_rsa.pub`, …), read on each start. They cannot be deleted through the API and are preselected in the GUI.
- On connect and on every change, the hub pushes the desired VMs and their power state to the node. The node converges to it: it deletes VMs the hub does not know, creates missing ones and applies power. A VM that should run is restarted with backoff, and goes to `error` after repeated quick failures.
- The node stores each VM in `paths.vms/<id>/vm.json`. If a provisioned VM's directory is missing, it shows `error` ("VM directory missing"), and a start rebuilds it blank.
- Create, delete and power changes for an offline node are accepted and shown as `pending` until the node connects.
- Nodes join the gateway over a WireGuard underlay (`10.198.0.0/24`); OVN Geneve tunnels run on top of it. VMs get a management NIC on `10.200.0.0/16`, devices get addresses in `10.201.0.0/24`.
- The hub is meant for a trusted home network. Node↔hub traffic is plain `ws://` with a shared token, and the GUI and API, including device creation, have no user auth.
- WireGuard devices can only reach the VMs' management IPs and the gateway DNS. The hub API and the OVN databases are firewalled from them.
- The hub stores device private keys, so configs can be downloaded again.
- VMs cannot reach each other on the management network; they talk only via shared networks (see Networks). The vz NAT NIC (`eth0`, internet) is still shared.
