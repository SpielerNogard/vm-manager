//go:build !darwin && !linux

package node

func osVersion() string { return "" }
func cpuModel() string  { return "" }
