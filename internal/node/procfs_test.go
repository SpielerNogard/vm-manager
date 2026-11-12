package node

import (
	"strings"
	"testing"
)

func TestParseOSRelease(t *testing.T) {
	in := "NAME=\"Debian GNU/Linux\"\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nID=debian\n"
	if got := parseOSRelease(strings.NewReader(in)); got != "Debian GNU/Linux 13 (trixie)" {
		t.Fatalf("got %q", got)
	}
}

func TestParseCPUModel(t *testing.T) {
	in := "processor\t: 0\nvendor_id\t: AuthenticAMD\nmodel name\t: AMD EPYC 7302\nprocessor\t: 1\nmodel name\t: AMD EPYC 7302\n"
	if got := parseCPUModel(strings.NewReader(in)); got != "AMD EPYC 7302" {
		t.Fatalf("got %q", got)
	}
}

func TestParseMemTotalMB(t *testing.T) {
	in := "MemTotal:       16384000 kB\nMemFree:         1000 kB\n"
	if got := parseMemTotalMB(strings.NewReader(in)); got != 16000 {
		t.Fatalf("got %d", got)
	}
}
