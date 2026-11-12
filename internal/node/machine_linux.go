package node

import (
	"io"
	"os"
)

func readProc[T any](path string, parse func(io.Reader) T) T {
	f, err := os.Open(path)
	if err != nil {
		var zero T
		return zero
	}
	defer f.Close()
	return parse(f)
}

func osVersion() string  { return readProc("/etc/os-release", parseOSRelease) }
func cpuModel() string   { return readProc("/proc/cpuinfo", parseCPUModel) }
func totalMemoryMB() int { return readProc("/proc/meminfo", parseMemTotalMB) }
