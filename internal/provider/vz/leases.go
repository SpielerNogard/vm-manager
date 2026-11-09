// Package vz implements node.Provider with Apple's Virtualization.framework.
package vz

import (
	"bufio"
	"errors"
	"io/fs"
	"net"
	"os"
	"strconv"
	"strings"
)

const DefaultLeasesPath = "/var/db/dhcpd_leases"

// LookupIP finds the newest lease for mac; macOS strips leading zeros from octets, so MACs are compared parsed.
func LookupIP(leasesPath, mac string) (string, bool, error) {
	want, err := net.ParseMAC(mac)
	if err != nil {
		return "", false, err
	}
	f, err := os.Open(leasesPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	var bestIP string
	var bestLease uint64
	var ip, hw string
	var lease uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		switch k {
		case "{":
			ip, hw, lease = "", "", 0
		case "ip_address":
			ip = v
		case "hw_address":
			_, hw, _ = strings.Cut(v, ",")
		case "lease":
			lease, _ = strconv.ParseUint(strings.TrimPrefix(v, "0x"), 16, 64)
		case "}":
			if sameMAC(hw, want) && ip != "" && (bestIP == "" || lease > bestLease) {
				bestIP, bestLease = ip, lease
			}
		}
	}
	return bestIP, bestIP != "", sc.Err()
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
