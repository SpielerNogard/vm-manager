// Package vz implements node.Provider with Apple's Virtualization.framework.
package vz

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const DefaultLeasesPath = "/var/db/dhcpd_leases"

// LookupLease finds the newest lease (by expiry) for mac; macOS strips leading zeros from octets, so MACs are compared parsed.
func LookupLease(leasesPath, mac string) (ip string, lease uint64, ok bool, err error) {
	want, err := net.ParseMAC(mac)
	if err != nil {
		return "", 0, false, err
	}
	f, err := os.Open(leasesPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	defer f.Close()

	var bestIP string
	var bestLease uint64
	var curIP, hw string
	var cur uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		switch k {
		case "{":
			curIP, hw, cur = "", "", 0
		case "ip_address":
			curIP = v
		case "hw_address":
			_, hw, _ = strings.Cut(v, ",")
		case "lease":
			cur, _ = strconv.ParseUint(strings.TrimPrefix(v, "0x"), 16, 64)
		case "}":
			if sameMAC(hw, want) && curIP != "" && (bestIP == "" || cur > bestLease) {
				bestIP, bestLease = curIP, cur
			}
		}
	}
	return bestIP, bestLease, bestIP != "", sc.Err()
}

// waitForLease polls until mac has a lease newer than after, so a previous boot's lease is not mistaken for this one.
func waitForLease(ctx context.Context, leasesPath, mac string, after uint64, every time.Duration) (string, error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		ip, lease, ok, err := LookupLease(leasesPath, mac)
		if err != nil {
			return "", err
		}
		if ok && lease > after {
			return ip, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-t.C:
		}
	}
}

func sameMAC(s string, want net.HardwareAddr) bool {
	parts := strings.Split(s, ":")
	if len(parts) != len(want) {
		return false
	}
	for i, p := range parts {
		b, err := strconv.ParseUint(p, 16, 8)
		if err != nil || byte(b) != want[i] {
			return false
		}
	}
	return true
}
