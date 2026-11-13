# Networking

## Reaching VMs

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

## Networks

Networks are standalone objects (they can be empty) that give VMs private connectivity to each other, also across nodes (OVN Geneve). Create one with `POST /api/networks` or the GUI, and attach VMs at creation (`networkIds` on `POST /api/vms`) or later with `PUT /api/vms/{id}/networks?restartAllowed=true` and `{"networkIds": [...]}`, which sets the full list (`networkIds` is required, `[]` detaches all; a missing field is a 400). Changing the list restarts a running VM.

| | |
|---|---|
| Subnet | Automatic `/24` from `10.210.0.0/16`, or a custom RFC1918 IPv4 subnet from `/16` to `/28` |
| Overlap | Must not overlap `10.198.0.0/24`, `10.199.0.0/30`, `10.200.0.0/16`, `10.201.0.0/24`, `192.168.64.0/24` or another network |
| Addresses | VMs get stable addresses from `.10` |
| Isolation | VMs reach each other only via shared networks. No router, no internet, no device (WireGuard) access, no routing between networks |
| DNS | `<vm>.<network>.internal` resolves to the VM's IP in that network. Devices resolve these names too but cannot reach the addresses. VMs can also resolve all networks' names but reach only their own |
| Guest | Each network is an extra NIC configured by a netplan entry matched by MAC, DHCP from OVN, no default route. Guest interface names stay kernel names, e.g. `enp0s3` (vz), `ens4` (cloud-hypervisor) or `eth1` (firecracker) |

Guests send only `*.internal` queries to the gateway (`10.201.0.1`, via the management NIC). The management network is built in and cannot be deleted; a network with members cannot be deleted either.
