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
	SSHPorts struct {
		Min int `json:"min"`
		Max int `json:"max"`
	} `json:"sshPorts"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	c.Listen, c.SSHPorts.Min, c.SSHPorts.Max = ":8080", 22000, 22999
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	switch {
	case c.Token == "":
		return c, errors.New("config: token is required")
	case c.SSHPorts.Min < 1 || c.SSHPorts.Min > c.SSHPorts.Max || c.SSHPorts.Max > 65535:
		return c, fmt.Errorf("config: invalid sshPorts range %d-%d", c.SSHPorts.Min, c.SSHPorts.Max)
	}
	return c, nil
}
