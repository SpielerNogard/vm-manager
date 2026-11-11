package hub

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

type settingsView struct {
	Config
	SetupComplete bool `json:"setupComplete"`
	Devices       int  `json:"devices"`
}

type settingsPatch struct {
	Clients *struct {
		Endpoint *string `json:"endpoint"`
	} `json:"clients"`
	Token    *string `json:"token"`
	GuestMTU *int    `json:"guestMTU"`
}

var hostName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)

func validEndpoint(s string) error {
	if net.ParseIP(s) != nil || (len(s) <= 253 && hostName.MatchString(s)) {
		return nil
	}
	return fmt.Errorf("%w: clients.endpoint must be a host name or IP without scheme or port", ErrInvalid)
}

func validToken(s string) error {
	if len(s) < 16 || strings.ContainsFunc(s, unicode.IsSpace) {
		return fmt.Errorf("%w: token must be at least 16 characters without whitespace", ErrInvalid)
	}
	return nil
}

func registerSettings(mux *http.ServeMux, cfg *ConfigStore, nm *NetModel) {
	respond := func(w http.ResponseWriter, c Config) {
		writeJSON(w, http.StatusOK, settingsView{c, c.Clients.Endpoint != "", len(nm.Devices())})
	}
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, _ *http.Request) { respond(w, cfg.Get()) })
	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, req *http.Request) {
		var p settingsPatch
		dec := json.NewDecoder(req.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			writeError(w, fmt.Errorf("%w: %v", ErrInvalid, err))
			return
		}
		c, err := cfg.Update(func(c *Config) error {
			if p.Clients != nil && p.Clients.Endpoint != nil {
				if err := validEndpoint(*p.Clients.Endpoint); err != nil {
					return err
				}
				c.Clients.Endpoint = *p.Clients.Endpoint
			}
			if p.Token != nil {
				if err := validToken(*p.Token); err != nil {
					return err
				}
				c.Token = *p.Token
			}
			if p.GuestMTU != nil {
				if *p.GuestMTU < 576 || *p.GuestMTU > 9000 {
					return fmt.Errorf("%w: guestMTU must be between 576 and 9000", ErrInvalid)
				}
				c.GuestMTU = *p.GuestMTU
			}
			return nil
		})
		if err != nil {
			writeError(w, err)
			return
		}
		respond(w, c)
	})
	mux.HandleFunc("POST /api/settings/token", func(w http.ResponseWriter, _ *http.Request) {
		t, err := newToken()
		if err != nil {
			writeError(w, err)
			return
		}
		c, err := cfg.Update(func(c *Config) error { c.Token = t; return nil })
		if err != nil {
			writeError(w, err)
			return
		}
		respond(w, c)
	})
}

// setupGuard blocks mutations until the client endpoint is set; nodes and gateway must keep working.
func setupGuard(cfg *ConfigStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		mutating := req.Method != http.MethodGet && req.Method != http.MethodHead && req.Method != http.MethodOptions
		exempt := p == "/api/settings" || strings.HasPrefix(p, "/api/settings/") || strings.HasPrefix(p, "/api/gateway/") || p == "/api/nodes/connect"
		if mutating && strings.HasPrefix(p, "/api/") && !exempt && cfg.Get().Clients.Endpoint == "" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "setup incomplete: set the client endpoint in settings"})
			return
		}
		next.ServeHTTP(w, req)
	})
}
