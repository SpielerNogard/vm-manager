# vm-manager

A hub that manages lightweight Linux VMs on nodes. You create VMs on the hub, via the REST API or the web GUI. Each VM then boots on the node you picked and joins an OVN management network, which you reach from your devices over WireGuard.

This is a proof of concept. Nodes are Apple Silicon Macs using Virtualization.framework (`vz` provider), and guests run Ubuntu 24.04.

## Requirements

- Hub stack: Docker with Compose. Supported hosts are OrbStack on macOS and Docker on Linux. The Docker host kernel must have the `openvswitch` and `wireguard` modules loaded (the gateway container fails fast otherwise). Docker Desktop is not supported or tested.
- Build: Go and Node.js/npm.
- Node: Apple Silicon, macOS 14 or later, Go with cgo (Xcode Command Line Tools).
