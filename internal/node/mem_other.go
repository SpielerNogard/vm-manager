//go:build !darwin

package node

func totalMemoryMB() int { return 0 }
