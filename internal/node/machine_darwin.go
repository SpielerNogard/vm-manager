package node

import "golang.org/x/sys/unix"

func osVersion() string {
	v, _ := unix.Sysctl("kern.osproductversion")
	return v
}

func cpuModel() string {
	v, _ := unix.Sysctl("machdep.cpu.brand_string")
	return v
}
