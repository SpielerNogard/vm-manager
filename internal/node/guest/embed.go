// Package guest holds the integration helpers that VMs install from their cloud-init seed.
package guest

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed integrations.sh vmm-sync git-credential-vmm gh vmm-linear.sh
var files embed.FS

var installs = []struct{ file, dest, mode string }{
	{"git-credential-vmm", "/usr/local/bin/git-credential-vmm", "755"},
	{"gh", "/usr/local/bin/gh", "755"},
	{"vmm-sync", "/usr/local/bin/vmm-sync", "755"},
	{"vmm-linear.sh", "/etc/profile.d/vmm-linear.sh", "644"},
}

const eof = "VMM_GUEST_EOF"

// Script is the seed's vmm-integrations.sh: it writes the helpers, then runs integrations.sh, all as root every boot.
func Script() string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	for _, in := range installs {
		fmt.Fprintf(&b, "cat >%s <<'%s'\n%s%s\nchmod %s %[1]s\n", in.dest, eof, read(in.file), eof, in.mode)
	}
	b.WriteString(read("integrations.sh"))
	return b.String()
}

func read(name string) string {
	b, err := files.ReadFile(name)
	if err != nil {
		panic(err) // embedded above, so only a build mistake gets here
	}
	if strings.Contains(string(b), eof) {
		panic(name + " contains " + eof)
	}
	return string(b)
}
