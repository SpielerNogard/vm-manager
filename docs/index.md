# vm-manager

A hub that manages lightweight Linux VMs on nodes. You create VMs on the hub, via the REST API or the web GUI. Each VM then boots on the node you picked and joins an OVN management network, which you reach from your devices over WireGuard.

This is a proof of concept. Nodes are Apple Silicon Macs using Virtualization.framework (`vz` provider) or amd64 Linux hosts with KVM (`firecracker` or `cloud-hypervisor` provider), and guests run Ubuntu 24.04.

## Requirements

- Hub stack: Docker with Compose. Supported hosts are OrbStack on macOS and Docker on Linux. The Docker host kernel must have the `openvswitch` and `wireguard` modules loaded (the gateway container fails fast otherwise). Docker Desktop is not supported or tested.
- Build: Go and Node.js/npm.
- Node: an Apple Silicon Mac with macOS 14 or later (building it needs Go with cgo, i.e. the Xcode Command Line Tools), or an amd64 Linux host with KVM (Debian 13 or Ubuntu 24.04).
