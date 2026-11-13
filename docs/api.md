# API

| Method | Path | |
|---|---|---|
| GET | `/api/nodes` | Nodes with provider, capacity, `state` (`pending`/`online`/`offline`), machine info, chassis state, their config file without the hub token, `version` (absent for nodes that report none), `updatable` (installed by the installer), `updateAvailable` (older than the hub), `updating` and `updateError` (why the last update failed, with the end of the installer output) |
| POST | `/api/nodes` | Add a node (`{"id", "provider"}`, provider `vz`, `firecracker` or `cloud-hypervisor`), state `pending` |
| DELETE | `/api/nodes/{id}` | Delete a node without VMs |
| POST | `/api/nodes/{id}/update` | Update the node to the hub's version; its VMs restart with it. 204 once the node has the command. 409 if the node is offline, reports no version, is not older than the hub, was not installed by the installer, has an update running, or the hub is a development build |
| GET | `/api/nodes/{id}/config?host=` | The node's `node.json`; `host` overrides the hub address (default: client endpoint) |
| GET | `/api/nodes/{id}/install-command?host=` | The one-line install command for the node (text); `host` as above |
| GET | `/api/version` | The hub's version (`{"version"}`), no token needed |
| GET | `/node/install.sh` | The node installer script, no token needed |
| GET | `/api/updates` | Update status: `version` (hub), `gateway`, `storage`, `latest` (latest release), `checkedAt`, `checkError`, `updateAvailable`, and `hubUpdate` (`manual`: upgrade the stack by hand; `none`: development build, no check) |
| POST | `/api/updates/check` | Ask GitHub for the latest release now; returns the update status, with a failed check in `checkError`. 409 on a development build |
| GET | `/api/settings` | Client endpoint, storage endpoint, ports, listen address, guest MTU, node token, plus `setupComplete`, `devices` (device count), `storageEndpoint` (effective) and `vmsWithDisks` |
| PUT | `/api/settings` | Partial update of `clients.endpoint`, `storage.endpoint` (`host:port` or empty), `token` (≥16 chars, no whitespace), `guestMTU` (576–9000); 400 on invalid; returns settings |
| POST | `/api/settings/token` | Regenerate the node token (random); returns settings |
| GET | `/api/vms` | VMs with state, desired power and IP |
| GET | `/api/vms/{id}` | One VM, same fields as in the list; 404 if unknown |
| POST | `/api/vms` | Create a VM (`nodeId`, `name`, `cpus`, `memoryMB`, `diskGB`, `sshKeyIds`, optional `networkIds`, optional `disks` (`diskId`, `mountPath`)). Also works for an offline node: the VM shows `pending` until the node connects |
| DELETE | `/api/vms/{id}` | Delete the VM and its root disk; attached disks are kept and become `releasing`. The node shuts a VM with disks down gracefully first (hard power-off after 10 s). Also works for an offline node: the VM is removed from the hub at once and the node deletes it when it reconnects |
| PUT | `/api/vms/{id}/networks?restartAllowed=true` | Set the VM's full network list (`{"networkIds": [...]}`, required; `[]` detaches all). A VM whose desired power is running is restarted and needs `restartAllowed=true`, else 409. Stopped VMs need no flag |
| PUT | `/api/vms/{id}/disks?restartAllowed=true` | Set the VM's full disk list (`{"disks": [{"diskId", "mountPath"}]}`, required; `[]` detaches all). A running VM is restarted and needs `restartAllowed=true`, else 409. Removed disks become `releasing` |
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
| GET | `/api/disks` | Disks with state (`creating`, `ready`, `releasing`, `missing`), size, allocated bytes and attachment |
| POST | `/api/disks` | Create a disk (`name`, `sizeGB`) |
| POST | `/api/disks/{id}/copy` | Copy a disk to a new one (`name`, same size). Returns `{"disk", "warning"}`; the warning is set if the source is attached or releasing, as the copy is then only crash-consistent. 409 if the source is `missing` |
| PATCH | `/api/disks/{id}?restartAllowed=true` | Grow a disk (`sizeGB`, must be larger; no shrinking). An attached running VM is restarted to see the new size and needs `restartAllowed=true`, else 409 |
| POST | `/api/disks/{id}/release?force=true` | Force-end a release that the node cannot confirm. Only while the node is offline (else 409); `force=true` is required. Anything the node still writes to the disk is lost |
| DELETE | `/api/disks/{id}` | Delete a disk. 409 while it is attached, releasing, or the source of a pending copy |
| GET | `/api/storage` | Storage server state: `online`, `endpoint`, `version`, and `orphans` (files on the server that no disk refers to) |
| GET | `/api/devices` | Client devices |
| POST | `/api/devices` | Add a device (`name`, optional `onHub` for a device on the hub machine), returns the device and its WireGuard config |
| DELETE | `/api/devices/{id}` | Remove a device |
| GET | `/api/devices/{id}/config` | Download the device's WireGuard config |
| GET | `/api/gateway/state` | Internal, token-protected: desired network state for the gateway |
| POST | `/api/gateway/status` | Internal, token-protected: gateway status report |
