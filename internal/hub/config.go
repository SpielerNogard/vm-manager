package hub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strconv"
	"sync"
)

type Config struct {
	Listen string `json:"listen"`
	Token  string `json:"token"`
	Hub    struct {
		Port int `json:"port"`
	} `json:"hub"`
	Underlay struct {
		Port int `json:"port"`
	} `json:"underlay"`
	Clients struct {
		Endpoint string `json:"endpoint"`
		Port     int    `json:"port"`
	} `json:"clients"`
	GuestMTU int `json:"guestMTU"`
	Storage  struct {
		// Endpoint is the host:port nodes use for NBD; empty means clients.endpoint with Port.
		Endpoint string `json:"endpoint,omitempty"`
		Port     int    `json:"port"`
	} `json:"storage"`
}

// StorageEndpoint is the host:port nodes reach the storage server at, or "" before setup.
func (c Config) StorageEndpoint() string {
	if c.Storage.Endpoint != "" {
		return c.Storage.Endpoint
	}
	if c.Clients.Endpoint == "" {
		return ""
	}
	return net.JoinHostPort(c.Clients.Endpoint, strconv.Itoa(c.Storage.Port))
}

// ConfigStore owns hub.json; readers call Get at use time so edits apply live.
type ConfigStore struct {
	mu   sync.Mutex
	path string
	cfg  Config
	// storageAdded is set when hub.json had no storage section, so an older endpoint can be carried over.
	storageAdded bool
}

func LoadOrCreateConfig(path string, getenv func(string) string) (*ConfigStore, error) {
	var c Config
	c.Listen, c.Underlay.Port, c.Clients.Port, c.GuestMTU = ":8080", 51820, 51821, 1300
	changed := false
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if c.Token, err = newToken(); err != nil {
			return nil, err
		}
		changed = true
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, invalidConfigErr(path, err)
		}
		if c.Token == "" {
			return nil, invalidConfigErr(path, errors.New("token is required"))
		}
	}
	storageAdded := c.Storage.Port == 0
	if storageAdded {
		c.Storage.Port, changed = 10809, true
	}
	if c.Hub.Port == 0 {
		c.Hub.Port = 8080
		if _, p, err := net.SplitHostPort(c.Listen); err == nil {
			if n, err := strconv.Atoi(p); err == nil && n > 0 {
				c.Hub.Port = n
			}
		}
		changed = true
	}
	for _, e := range []struct {
		key string
		dst *int
	}{{"HUB_PORT", &c.Hub.Port}, {"UNDERLAY_PORT", &c.Underlay.Port}, {"CLIENTS_PORT", &c.Clients.Port}, {"STORAGE_PORT", &c.Storage.Port}} {
		v := getenv(e.key)
		if v == "" {
			continue
		}
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("config: invalid %s %q", e.key, v)
		}
		if *e.dst != p {
			*e.dst, changed = p, true
		}
	}
	if changed {
		if err := saveJSON(path, c); err != nil {
			return nil, err
		}
	}
	return &ConfigStore{path: path, cfg: c, storageAdded: storageAdded}, nil
}

func invalidConfigErr(path string, err error) error {
	return fmt.Errorf("hub.json at %s is invalid (%w); fix or delete it to have a new one created", path, err)
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *ConfigStore) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Update applies f to a copy and swaps it in only after it is persisted.
func (s *ConfigStore) Update(f func(c *Config) error) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	if err := f(&c); err != nil {
		return s.cfg, err
	}
	if err := saveJSON(s.path, c); err != nil {
		return s.cfg, err
	}
	s.cfg = c
	return c, nil
}

// MigrateStorageEndpoint keeps the endpoint a storage agent advertised before it moved into hub.json, so attachment URLs do not change.
func MigrateStorageEndpoint(cfg *ConfigStore, disks *DiskStore) error {
	old := disks.Endpoint()
	if !cfg.storageAdded || old == "" || cfg.Get().StorageEndpoint() == old {
		return nil
	}
	_, err := cfg.Update(func(c *Config) error { c.Storage.Endpoint = old; return nil })
	return err
}
