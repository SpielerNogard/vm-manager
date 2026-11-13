package linuxvm

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// TapName is stable per VM and NIC index and fits IFNAMSIZ; index 0 is eth0.
func TapName(vmID string, i int) string {
	sum := sha256.Sum256([]byte(vmID))
	return "vmm" + hex.EncodeToString(sum[:4]) + strconv.Itoa(i)
}
