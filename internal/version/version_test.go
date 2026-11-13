package version

import "testing"

func TestOlder(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v0.3.0", "v0.4.0", true}, {"v0.4.0", "v0.4.0", false}, {"v0.4.0", "v0.3.0", false},
		{"v0.9.0", "v0.10.0", true}, {"dev", "v0.4.0", false}, {"v0.4.0", "dev", false},
		{"", "v0.4.0", false}, {"0.3.0", "v0.4.0", false},
	} {
		if got := Older(tc.a, tc.b); got != tc.want {
			t.Errorf("Older(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestReleased(t *testing.T) {
	for v, want := range map[string]bool{"v0.4.0": true, "dev": false, "": false, "0.4.0": false, "latest": false, "v1": false} {
		if got := Released(v); got != want {
			t.Errorf("Released(%q) = %v, want %v", v, got, want)
		}
	}
}
