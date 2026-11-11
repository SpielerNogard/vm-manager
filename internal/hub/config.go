package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

type Config struct {
	Listen   string `json:"listen"`
	Token    string `json:"token"`
	Underlay struct {
		Port int `json:"port"`
	} `json:"underlay"`
	Clients struct {
		Endpoint string `json:"endpoint"`
		Port     int    `json:"port"`
	} `json:"clients"`
	GuestMTU int `json:"guestMTU"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	c.Listen, c.Underlay.Port, c.Clients.Port, c.GuestMTU = ":8080", 51820, 51821, 1300
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.Token == "" {
		return c, errors.New("config: token is required")
	}
	return c, nil
}
