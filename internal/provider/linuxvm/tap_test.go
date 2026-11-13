package linuxvm

import "testing"

func TestTapNameLength(t *testing.T) {
	a, b := TapName("0123456789abcdef0123456789abcdef0123", 9), TapName("x123456789abcdef0123456789abcdef0123", 9)
	if len(a) > 15 || a == b || a != TapName("0123456789abcdef0123456789abcdef0123", 9) {
		t.Fatalf("tap names %q %q", a, b)
	}
}
