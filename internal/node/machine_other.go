//go:build !darwin

package node

func osVersion() string { return "" }
func cpuModel() string  { return "" }
