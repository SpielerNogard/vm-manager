# vm-manager

A hub that manages lightweight Linux VMs on nodes. You create VMs on the hub, via the REST API or the web GUI. Each VM then boots on the node you picked and joins an OVN management network, which you reach from your devices over WireGuard.

This is a proof of concept. Nodes are Apple Silicon Macs using Virtualization.framework (`vz` provider) or amd64 Linux hosts with KVM (`firecracker` or `cloud-hypervisor` provider), and guests run Ubuntu 24.04.

**Documentation:** <https://spielernogard.github.io/vm-manager/>
