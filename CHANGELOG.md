## v0.2.0 (2026-11-12)

### Feat

- one-line node install with hypervisor choice in the GUI (#16)

## v0.1.0 (2026-11-12)

### BREAKING CHANGE

- VMs that exist on a node but are unknown to the hub,
including all VMs created before this version, are deleted with their
disks on the node's first connect.
- POST /api/vms takes sshKeyIds instead of sshPublicKey and dataDisks [{name, mountPath, sizeGB}] instead of dataDisksGB.

### Feat

- firecracker provider for Linux nodes (#14)
- standalone disks on a storage server, attached over NBD (#12)
- bring settings page, hub devices and single-object GETs to main (#11)
- VM networks for VM-to-VM traffic (#6)
- **hub**: make the hub the source of truth, add VM power control (#5)
- ovn management network with wireguard access to vms (#3)
- rework GUI in Plinth style, add SSH keys, named data disks and node details (#2)
- hub and vz nodes for lightweight dev VMs (PoC) (#1)


- release pipeline with commitizen, zizmor and PR checks (#15)
