package node

import "golang.org/x/sys/unix"

func totalMemoryMB() int {
	b, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return int(b >> 20)
}
