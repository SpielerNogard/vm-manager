package hub

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"
)

func APIHandler(r *Registry, keys *KeyStore, net *NetModel, cfg Config) http.Handler {
	mux := http.NewServeMux()
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		want := []byte("Bearer " + cfg.Token)
		return func(w http.ResponseWriter, req *http.Request) {
			if subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), want) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			h(w, req)
		}
	}
	mux.HandleFunc("GET /api/gateway/state", authed(func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, r.GatewayState()) }))
	mux.HandleFunc("POST /api/gateway/status", authed(func(w http.ResponseWriter, req *http.Request) {
		var s GatewayStatus
		if err := json.NewDecoder(req.Body).Decode(&s); err != nil {
			writeError(w, fmt.Errorf("%w: %v", ErrInvalid, err))
			return
		}
		r.SetGatewayStatus(s)
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /api/networks", func(w http.ResponseWriter, _ *http.Request) {
		members := 0
		for _, nics := range net.AllNICs() {
			members += len(nics)
		}
		writeJSON(w, http.StatusOK, []map[string]any{{"name": mgmtNetwork, "subnet": mgmtSubnet, "members": members}})
	})
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, _ *http.Request) {
		out := []deviceView{}
		for _, d := range net.Devices() {
			out = append(out, viewOf(d))
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /api/devices", func(w http.ResponseWriter, req *http.Request) {
		var q struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
			writeError(w, fmt.Errorf("%w: %v", ErrInvalid, err))
			return
		}
		if cfg.Clients.Endpoint == "" {
			writeError(w, fmt.Errorf("%w: set clients.endpoint in the hub config before adding devices", ErrConflict))
			return
		}
		d, err := net.AddDevice(q.Name)
		if err != nil {
			writeError(w, err)
			return
		}
		conf, err := deviceConfig(r, d, cfg)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"device": viewOf(d), "config": conf})
	})
	mux.HandleFunc("DELETE /api/devices/{id}", func(w http.ResponseWriter, req *http.Request) {
		if err := net.DeleteDevice(req.PathValue("id")); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/devices/{id}/config", func(w http.ResponseWriter, req *http.Request) {
		i := slices.IndexFunc(net.Devices(), func(d Device) bool { return d.ID == req.PathValue("id") })
		if i < 0 {
			writeError(w, fmt.Errorf("%w: device", ErrNotFound))
			return
		}
		conf, err := deviceConfig(r, net.Devices()[i], cfg)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, conf)
	})
	mux.HandleFunc("GET /api/nodes", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, r.Nodes()) })
	mux.HandleFunc("GET /api/vms", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, r.VMs()) })
	mux.HandleFunc("POST /api/vms", func(w http.ResponseWriter, req *http.Request) {
		var q CreateVMRequest
		if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
			writeError(w, fmt.Errorf("%w: %v", ErrInvalid, err))
			return
		}
		var err error
		if q.SSHPublicKeys, err = keys.PublicKeys(q.SSHKeyIDs); err != nil {
			writeError(w, err)
			return
		}
		vm, err := r.CreateVM(q)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, vm)
	})
	mux.HandleFunc("DELETE /api/vms/{id}", func(w http.ResponseWriter, req *http.Request) {
		if err := r.DeleteVM(req.PathValue("id")); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/keys", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, keys.List()) })
	mux.HandleFunc("POST /api/keys", func(w http.ResponseWriter, req *http.Request) {
		var q struct {
			Name      string `json:"name"`
			PublicKey string `json:"publicKey"`
		}
		if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
			writeError(w, fmt.Errorf("%w: %v", ErrInvalid, err))
			return
		}
		k, err := keys.Add(q.Name, q.PublicKey)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, k)
	})
	mux.HandleFunc("DELETE /api/keys/{id}", func(w http.ResponseWriter, req *http.Request) {
		if err := keys.Delete(req.PathValue("id")); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// deviceView is Device without its private key, which only leaves the hub inside a client config.
type deviceView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	IP        string    `json:"ip"`
	PublicKey string    `json:"publicKey"`
	CreatedAt time.Time `json:"createdAt"`
}

func viewOf(d Device) deviceView {
	return deviceView{ID: d.ID, Name: d.Name, IP: d.IP, PublicKey: d.PublicKey, CreatedAt: d.CreatedAt}
}

func deviceConfig(r *Registry, d Device, cfg Config) (string, error) {
	pub, err := r.clientsPublicKey()
	if err != nil {
		return "", err
	}
	return DeviceConfig(d, pub, cfg.Clients.Endpoint, cfg.Clients.Port, cfg.GuestMTU), nil
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrConflict):
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
