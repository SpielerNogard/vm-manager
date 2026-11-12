package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// InitOptions are the node.json fields the installer sets; zero values leave a field as it is.
type InitOptions struct {
	Path, HubURL, Token, Name, Provider, UnderlayEndpoint string
	UnderlayMTU, MemoryMB                                 int
}

// InitConfig creates node.json or updates only the given fields, keeping every other key (paths, provider sections) as written.
func InitConfig(o InitOptions, vmsDir, imagesDir string) error {
	connect, err := connectURL(o.HubURL)
	if err != nil {
		return err
	}
	if o.Token == "" || o.Name == "" || o.Provider == "" {
		return errors.New("token, name and provider are required")
	}
	doc := map[string]json.RawMessage{}
	b, err := os.ReadFile(o.Path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%s: %w", o.Path, err)
		}
	case errors.Is(err, fs.ErrNotExist):
		if err := setJSON(doc, "paths", map[string]string{"vms": vmsDir, "images": imagesDir}); err != nil {
			return err
		}
	default:
		return err
	}
	for _, set := range []func() error{
		func() error { return setJSON(doc, "nodeId", o.Name) },
		func() error { return setJSON(doc, "provider", o.Provider) },
		func() error { return setNested(doc, "hub", map[string]any{"url": connect, "token": o.Token}) },
		func() error {
			u := map[string]any{}
			if o.UnderlayEndpoint != "" {
				u["endpoint"] = o.UnderlayEndpoint
			}
			if o.UnderlayMTU != 0 {
				u["mtu"] = o.UnderlayMTU
			}
			return setNested(doc, "underlay", u)
		},
		func() error {
			if o.MemoryMB == 0 {
				return nil
			}
			return setNested(doc, "capacity", map[string]any{"memoryMB": o.MemoryMB})
		},
	} {
		if err := set(); err != nil {
			return err
		}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.Path), 0o755); err != nil {
		return err
	}
	// writeFileAtomic creates the file 0600: node.json holds the hub token.
	return writeFileAtomic(o.Path, append(out, '\n'))
}

// connectURL turns the hub's http(s)://host:port into the node's websocket URL.
func connectURL(hub string) (string, error) {
	u, err := url.Parse(hub)
	if err != nil || u.Host == "" || u.Port() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("hub %q: want http://host:port", hub)
	}
	scheme := "ws"
	if u.Scheme == "https" {
		scheme = "wss"
	}
	return scheme + "://" + u.Host + "/api/nodes/connect", nil
}

func setJSON(doc map[string]json.RawMessage, key string, v any) error {
	b, err := json.Marshal(v)
	doc[key] = b
	return err
}

// setNested sets keys inside the object doc[key], creating it if needed and keeping its other keys.
func setNested(doc map[string]json.RawMessage, key string, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	obj := map[string]json.RawMessage{}
	if raw, ok := doc[key]; ok {
		if err := json.Unmarshal(raw, &obj); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	for k, v := range values {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		obj[k] = b
	}
	return setJSON(doc, key, obj)
}

// ReadToken reads the hub token from r (the installer pipes it in so it never appears on a command line).
func ReadToken(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", errors.New("no token on stdin")
	}
	return t, nil
}
