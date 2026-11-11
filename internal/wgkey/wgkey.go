// Package wgkey generates WireGuard X25519 key pairs in WireGuard's base64 format.
package wgkey

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

func Generate() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()), base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

func Public(priv string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(priv)
	if err != nil {
		return "", fmt.Errorf("wireguard private key: %w", err)
	}
	k, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return "", fmt.Errorf("wireguard private key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}
