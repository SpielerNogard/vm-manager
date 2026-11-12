package firecracker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"vm-manager/internal/node"
)

const bootArgs = "console=ttyS0 reboot=k panic=1 root=LABEL=cloudimg-rootfs ro"

// tapName is stable per VM and NIC index and fits IFNAMSIZ; index 0 is eth0.
func tapName(vmID string, i int) string {
	sum := sha256.Sum256([]byte(vmID))
	return "vmm" + hex.EncodeToString(sum[:4]) + strconv.Itoa(i)
}

type fcDrive struct {
	ID       string `json:"drive_id"`
	Path     string `json:"path_on_host"`
	Root     bool   `json:"is_root_device"`
	ReadOnly bool   `json:"is_read_only"`
}

type fcNIC struct {
	ID  string `json:"iface_id"`
	MAC string `json:"guest_mac"`
	Dev string `json:"host_dev_name"`
}

// vmConfig is Firecracker's --config-file; drives holds the host path of each node.AttachOrder disk.
func vmConfig(f node.VMFile, kernel, initrd string, drives []string) ([]byte, error) {
	order := node.AttachOrder(f)
	if len(drives) != len(order) {
		return nil, fmt.Errorf("got %d drive paths for %d disks", len(drives), len(order))
	}
	var ds []fcDrive
	for i, d := range order {
		ds = append(ds, fcDrive{ID: d.Name, Path: drives[i], ReadOnly: d.ReadOnly})
	}
	nics := []fcNIC{{ID: "eth0", MAC: f.MAC, Dev: tapName(f.Spec.ID, 0)}}
	for i, n := range f.Spec.NICs {
		nics = append(nics, fcNIC{ID: "net" + strconv.Itoa(i), MAC: n.MAC, Dev: tapName(f.Spec.ID, i+1)})
	}
	return json.MarshalIndent(map[string]any{
		"boot-source":        map[string]string{"kernel_image_path": kernel, "initrd_path": initrd, "boot_args": bootArgs},
		"drives":             ds,
		"machine-config":     map[string]int{"vcpu_count": f.Spec.CPUs, "mem_size_mib": f.Spec.MemoryMB},
		"network-interfaces": nics,
	}, "", "  ")
}
