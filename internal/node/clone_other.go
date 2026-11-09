//go:build !darwin

package node

func cloneFile(src, dst string) error { return copyFile(src, dst) }
