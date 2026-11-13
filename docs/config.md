# Config

**Hub** (`-config`, default `~/.vm-manager/hub.json`; `-ssh-dir`, default `~/.ssh`). Created on first start and edited through Settings:

| Key | Default | |
|---|---|---|
| `token` | random | Shared secret with nodes |
| `listen` | `:8080` | HTTP listen address |
| `hub.port` | port of `listen`, or `HUB_PORT` | Published HTTP port, used in generated `node.json` |
| `underlay.port` | `51820` | Published underlay WireGuard port, handed to nodes |
| `clients.endpoint` | empty | Address devices connect to; the GUI stays locked until set |
| `clients.port` | `51821` | Published clients WireGuard port |
| `guestMTU` | `1300` | Guest mgmt MTU, delivered by OVN DHCP |
| `storage.endpoint` | empty | `host:port` nodes use for NBD; empty means `clients.endpoint` with `storage.port` |
| `storage.port` | `10809`, or `STORAGE_PORT` | Port of the default storage endpoint |

**Node:** the required keys are `hub.url`, `hub.token`, `provider` (`vz` on macOS, `firecracker` or `cloud-hypervisor` on Linux), `paths.vms` and `paths.images`. `nodeId` is optional and defaults to the hostname. Set it explicitly: if it changes (or the hostname changes while it is unset), the hub refuses the node because its VMs belong to the old ID. The node keeps its VMs and retries. `capacity.cpus`/`memoryMB` are optional and default to the hardware, minus 4 GB of RAM. `underlay.endpoint` overrides the hub's WireGuard endpoint (default: the host of `hub.url` plus the hub's underlay port). `underlay.mtu` is the `wg-underlay` MTU (default 1420). The node's WireGuard chassis key is stored in `<paths.vms>/chassis.key`.

Provider settings go in a section named after the provider. For `vz` these are `vz.imageURL` and `vz.leasesPath`; for `firecracker` it is `firecracker.imageURL` (default: the Ubuntu 24.04 amd64 cloud image); for `cloud-hypervisor` they are `cloud-hypervisor.imageURL` (same default) and `cloud-hypervisor.firmware` (path to the edk2 `CLOUDHV.fd`, default `/usr/local/share/cloud-hypervisor/CLOUDHV.fd`).
