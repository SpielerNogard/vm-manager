package wgkey

import (
	"encoding/base64"
	"testing"
)

func TestGenerateAndPublic(t *testing.T) {
	priv, pub, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{priv, pub} {
		if b, err := base64.StdEncoding.DecodeString(k); err != nil || len(b) != 32 {
			t.Fatalf("key %q does not decode to 32 bytes", k)
		}
	}
	if got, err := Public(priv); err != nil || got != pub {
		t.Fatalf("Public = %q, %v; want %q", got, err, pub)
	}
	priv2, _, _ := Generate()
	if priv == priv2 {
		t.Fatal("two keys are identical")
	}
	if _, err := Public("not base64!"); err == nil {
		t.Fatal("want error for invalid key")
	}
}
