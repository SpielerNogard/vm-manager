package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteHubConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.json")
	env := map[string]string{"HUB_TOKEN": `to"k\en`, "CLIENTS_ENDPOINT": `vmm"lan`, "UNDERLAY_PORT": "52820"}
	if err := writeHubConfig(path, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Listen   string `json:"listen"`
		Token    string `json:"token"`
		Underlay struct {
			Port int `json:"port"`
		} `json:"underlay"`
		Clients struct {
			Endpoint string `json:"endpoint"`
			Port     int    `json:"port"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	if cfg.Listen != ":8080" || cfg.Token != env["HUB_TOKEN"] || cfg.Underlay.Port != 52820 || cfg.Clients.Endpoint != env["CLIENTS_ENDPOINT"] || cfg.Clients.Port != 51821 {
		t.Fatalf("cfg = %+v", cfg)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode())
	}
}

func TestWriteHubConfigRejectsBadInput(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"no token":     {},
		"port letters": {"HUB_TOKEN": "t", "UNDERLAY_PORT": "51820,"},
		"port range":   {"HUB_TOKEN": "t", "CLIENTS_PORT": "70000"},
	} {
		path := filepath.Join(t.TempDir(), "hub.json")
		if err := writeHubConfig(path, func(k string) string { return env[k] }); err == nil {
			t.Errorf("%s: no error", name)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s: file written", name)
		}
	}
}
