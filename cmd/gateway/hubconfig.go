package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// writeHubConfig writes the hub's first-start config from the compose environment.
func writeHubConfig(path string, getenv func(string) string) error {
	token := getenv("HUB_TOKEN")
	if token == "" {
		return errors.New("HUB_TOKEN is required on first start")
	}
	port := func(key string, def int) (int, error) {
		v := getenv(key)
		if v == "" {
			return def, nil
		}
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return 0, fmt.Errorf("%s=%q is not a port number", key, v)
		}
		return p, nil
	}
	// the ports are the published host ports; inside the container WireGuard always uses 51820/51821
	underlay, err := port("UNDERLAY_PORT", 51820)
	if err != nil {
		return err
	}
	clients, err := port("CLIENTS_PORT", 51821)
	if err != nil {
		return err
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
	cfg.Listen, cfg.Token, cfg.Underlay.Port = ":8080", token, underlay
	cfg.Clients.Endpoint, cfg.Clients.Port = getenv("CLIENTS_ENDPOINT"), clients
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
