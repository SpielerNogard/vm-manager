package vz

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestLookupIP(t *testing.T) {
	ip, _, ok, err := LookupLease("testdata/dhcpd_leases", "a2:b3:0c:04:05:06")
	if err != nil || !ok || ip != "192.168.64.7" {
		t.Fatalf("ip=%q ok=%v err=%v", ip, ok, err)
	}
	ip, _, ok, err = LookupLease("testdata/dhcpd_leases", "DE:AD:BE:EF:00:01")
	if err != nil || !ok || ip != "192.168.64.9" {
		t.Fatalf("uppercase: ip=%q ok=%v err=%v", ip, ok, err)
	}
	if _, _, ok, err := LookupLease("testdata/dhcpd_leases", "02:00:00:00:00:99"); ok || err != nil {
		t.Fatalf("unknown mac: ok=%v err=%v", ok, err)
	}
	if _, _, ok, err := LookupLease(filepath.Join(t.TempDir(), "missing"), "a2:b3:0c:04:05:06"); ok || err != nil {
		t.Fatalf("missing file: ok=%v err=%v", ok, err)
	}
}

func TestWaitForNewLeaseIgnoresPreviousBoot(t *testing.T) {
	_, prev, _, err := LookupLease("testdata/dhcpd_leases", "a2:b3:0c:04:05:06")
	if err != nil || prev != 0x6730a1ff {
		t.Fatalf("prev = %x err %v", prev, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if ip, err := waitForLease(ctx, "testdata/dhcpd_leases", "a2:b3:0c:04:05:06", prev, 10*time.Millisecond); err == nil {
		t.Fatalf("got stale lease ip %q", ip)
	}
	ip, err := waitForLease(context.Background(), "testdata/dhcpd_leases", "a2:b3:0c:04:05:06", prev-1, 10*time.Millisecond)
	if err != nil || ip != "192.168.64.7" {
		t.Fatalf("ip=%q err=%v", ip, err)
	}
}
