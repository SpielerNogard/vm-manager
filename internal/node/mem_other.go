//go:build !darwin && !linux

package node

func totalMemoryMB() int { return 0 }
