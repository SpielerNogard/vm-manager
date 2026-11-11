//go:build !linux

package main

import (
	"fmt"
	"os"
)

// netagent runs inside the Linux helper VM; this stub keeps `go build ./...` working on the Mac.
func main() {
	fmt.Fprintln(os.Stderr, "netagent only runs on Linux")
	os.Exit(1)
}
