package vz

import (
	"path/filepath"
	"testing"
)

func TestLookupIP(t *testing.T) {
	ip, ok, err := LookupIP("testdata/dhcpd_leases", "a2:b3:0c:04:05:06")
	if err != nil || !ok || ip != "192.168.64.7" {
		t.Fatalf("ip=%q ok=%v err=%v", ip, ok, err)
	}
	ip, ok, err = LookupIP("testdata/dhcpd_leases", "DE:AD:BE:EF:00:01")
	if err != nil || !ok || ip != "192.168.64.9" {
		t.Fatalf("uppercase: ip=%q ok=%v err=%v", ip, ok, err)
	}
	if _, ok, err := LookupIP("testdata/dhcpd_leases", "02:00:00:00:00:99"); ok || err != nil {
		t.Fatalf("unknown mac: ok=%v err=%v", ok, err)
	}
	if _, ok, err := LookupIP(filepath.Join(t.TempDir(), "missing"), "a2:b3:0c:04:05:06"); ok || err != nil {
		t.Fatalf("missing file: ok=%v err=%v", ok, err)
	}
}
