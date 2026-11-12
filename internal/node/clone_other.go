//go:build !darwin && !linux

package node

func cloneFile(src, dst string) error { return copyFile(src, dst) }
