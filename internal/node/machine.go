package node

import (
	"runtime"

	"vm-manager/internal/proto"
)

// MachineInfo describes the host for the hub's node detail page.
func MachineInfo() *proto.Machine {
	return &proto.Machine{
		OS: runtime.GOOS, OSVersion: osVersion(), Arch: runtime.GOARCH,
		CPUModel: cpuModel(), CPUs: runtime.NumCPU(), MemoryMB: totalMemoryMB(),
	}
}
