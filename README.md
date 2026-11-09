# vm-manager

A hub that manages lightweight Linux VMs on nodes. You create VMs on the hub, via the REST API or the web GUI. Each VM then boots on the node you picked, and you SSH into it through the hub.

This is a proof of concept. Nodes are Apple Silicon Macs using Virtualization.framework (`vz` provider), and guests run Ubuntu 24.04.

## Requirements

- Hub: Go and Node.js/npm to build. It runs on any OS.
- Node: Apple Silicon, macOS 13 or later, Go with cgo (Xcode Command Line Tools).

## Build

```sh
make web hub node   # node binary is ad-hoc signed with the virtualization entitlement
make test
```

## Run

Hub (config defaults to `~/.vm-manager/hub.json`):

```sh
cp hub.example.json ~/.vm-manager/hub.json   # set a token
./bin/hub
```

Node (config defaults to `~/.vm-manager/node.json`):

```sh
cp node.example.json ~/.vm-manager/node.json  # set hub.url, the same token, paths
./bin/node
```

Open `http://<hub>:8080`, pick a node, and create a VM. You can also use the API:

```sh
curl -X POST http://<hub>:8080/api/vms -d '{
  "nodeId": "mac-mini-1", "name": "dev", "cpus": 2, "memoryMB": 2048,
  "diskGB": 20, "dataDisksGB": [10], "sshPublicKey": "'"$(cat ~/.ssh/id_ed25519.pub)"'"
}'
```

When the VM is `running`:

```sh
ssh -p <sshPort> ubuntu@<hub>
```

On first use, the node downloads the Ubuntu cloud image into `paths.images`, which takes a few minutes. Data disks are attached as raw block devices (`/dev/vdb`, …) and are not formatted.

## Config

**Hub:** `listen` (default `:8080`), `token` (required), and `sshPorts.min`/`max` (default 22000–22999). The hub opens one SSH port per VM.

**Node:** the required keys are `hub.url`, `hub.token`, `provider` (`vz`), `paths.vms` and `paths.images`. `nodeId` is optional and defaults to the hostname. `capacity.cpus`/`memoryMB` are optional and default to the hardware, minus 4 GB of RAM.

Provider settings go in a section named after the provider. For `vz` these are `vz.imageURL` and `vz.leasesPath`.

## API

| Method | Path | |
|---|---|---|
| GET | `/api/nodes` | Nodes with provider, capacity, and online state |
| GET | `/api/vms` | VMs with state, IP, and SSH port |
| POST | `/api/vms` | Create a VM (`nodeId`, `name`, `cpus`, `memoryMB`, `diskGB`, `dataDisksGB`, `sshPublicKey`) |
| DELETE | `/api/vms/{id}` | Stop the VM and delete its disks |

## Behaviour

- The hub keeps state in memory. After a hub restart, nodes report their VMs again and keep their SSH ports where possible.
- The node stores each VM in `paths.vms/<id>/vm.json`. After a node restart, the node reboots its VMs from their existing disks.
- The hub is meant for a trusted home network. Node↔hub traffic is plain `ws://` with a shared token, and the GUI and API have no user auth.
