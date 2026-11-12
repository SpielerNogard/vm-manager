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

	"vm-manager/internal/proto"
	"vm-manager/internal/version"
)

func APIHandler(r *Registry, keys *KeyStore, net *NetModel, cfg *ConfigStore) http.Handler {
	mux := http.NewServeMux()
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			want := []byte("Bearer " + cfg.Get().Token)
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
	networkViews := func() []map[string]any {
		members := map[string][]string{}
		for vmID, nics := range net.AllNICs() {
			for _, n := range nics {
				members[n.Network] = append(members[n.Network], vmID)
			}
		}
		view := func(id, name, subnet string, builtin bool) map[string]any {
			m := slices.Sorted(slices.Values(members[id]))
			if m == nil {
				m = []string{}
			}
			return map[string]any{"id": id, "name": name, "subnet": subnet, "builtin": builtin, "members": m}
		}
		out := []map[string]any{view(mgmtNetwork, mgmtNetwork, mgmtSubnet, true)}
		for _, n := range net.Networks() {
			out = append(out, view(n.ID, n.Name, n.Subnet, false))
		}
		return out
	}
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": version.Version})
	})
	mux.HandleFunc("GET /api/networks", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, networkViews()) })
	mux.HandleFunc("GET /api/networks/{id}", func(w http.ResponseWriter, req *http.Request) {
		for _, n := range networkViews() {
			if n["id"] == req.PathValue("id") {
				writeJSON(w, http.StatusOK, n)
				return
			}
		}
		writeError(w, fmt.Errorf("%w: network %q", ErrNotFound, req.PathValue("id")))
	})
	mux.HandleFunc("POST /api/networks", func(w http.ResponseWriter, req *http.Request) {
		var q struct {
			Name   string `json:"name"`
			Subnet string `json:"subnet"`
		}
		if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
			writeError(w, fmt.Errorf("%w: %v", ErrInvalid, err))
			return
		}
		n, err := net.AddNetwork(q.Name, q.Subnet)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, n)
	})
	mux.HandleFunc("DELETE /api/networks/{id}", func(w http.ResponseWriter, req *http.Request) {
		if req.PathValue("id") == mgmtNetwork {
			writeError(w, fmt.Errorf("%w: the mgmt network cannot be deleted", ErrInvalid))
			return
		}
		if err := net.DeleteNetwork(req.PathValue("id")); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/disks", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, r.Disks()) })
	mux.HandleFunc("POST /api/disks", func(w http.ResponseWriter, req *http.Request) {
		var q struct {
			Name   string `json:"name"`
			SizeGB int    `json:"sizeGB"`
		}
		if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
			writeError(w, fmt.Errorf("%w: %v", ErrInvalid, err))
			return
		}
		d, err := r.CreateDisk(q.Name, q.SizeGB)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, d)
	})
	mux.HandleFunc("POST /api/disks/{id}/copy", func(w http.ResponseWriter, req *http.Request) {
		var q struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
			writeError(w, fmt.Errorf("%w: %v", ErrInvalid, err))
			return
		}
		d, warning, err := r.CopyDisk(req.PathValue("id"), q.Name)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, struct {
			Disk    DiskView `json:"disk"`
			Warning string   `json:"warning,omitempty"`
		}{d, warning})
	})
	mux.HandleFunc("PATCH /api/disks/{id}", func(w http.ResponseWriter, req *http.Request) {
		var q struct {
			SizeGB int `json:"sizeGB"`
		}
		if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
			writeError(w, fmt.Errorf("%w: %v", ErrInvalid, err))
			return
		}
		d, err := r.GrowDisk(req.PathValue("id"), q.SizeGB, req.URL.Query().Get("restartAllowed") == "true")
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
	mux.HandleFunc("POST /api/disks/{id}/release", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("force") != "true" {
			writeError(w, fmt.Errorf("%w: a node releases its disks itself; pass force=true only if it will not come back", ErrInvalid))
			return
		}
		if err := r.ForceRelease(req.PathValue("id")); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/disks/{id}", func(w http.ResponseWriter, req *http.Request) {
		if err := r.DeleteDisk(req.PathValue("id")); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/storage", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, r.Storage()) })
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, _ *http.Request) {
		out := []deviceView{}
		for _, d := range net.Devices() {
			out = append(out, viewOf(d))
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /api/devices", func(w http.ResponseWriter, req *http.Request) {
		var q struct {
			Name  string `json:"name"`
			OnHub bool   `json:"onHub"`
		}
		if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
			writeError(w, fmt.Errorf("%w: %v", ErrInvalid, err))
			return
		}
		c := cfg.Get()
		d, err := net.AddDevice(q.Name, q.OnHub)
		if err != nil {
			writeError(w, err)
			return
		}
		conf, err := deviceConfig(r, d, c)
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
		conf, err := deviceConfig(r, net.Devices()[i], cfg.Get())
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, conf)
	})
	mux.HandleFunc("GET /api/nodes", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, r.Nodes()) })
	mux.HandleFunc("POST /api/nodes", func(w http.ResponseWriter, req *http.Request) {
		var q struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
			writeError(w, fmt.Errorf("%w: %v", ErrInvalid, err))
			return
		}
		n, err := r.CreateNode(q.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, n)
	})
	mux.HandleFunc("DELETE /api/nodes/{id}", func(w http.ResponseWriter, req *http.Request) {
		if err := r.DeleteNode(req.PathValue("id")); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/nodes/{id}/config", func(w http.ResponseWriter, req *http.Request) {
		b, err := r.NodeConfig(req.PathValue("id"), req.URL.Query().Get("host"))
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	mux.HandleFunc("GET /api/vms", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, r.VMs()) })
	mux.HandleFunc("GET /api/vms/{id}", func(w http.ResponseWriter, req *http.Request) {
		for _, vm := range r.VMs() {
			if vm.ID == req.PathValue("id") {
				writeJSON(w, http.StatusOK, vm)
				return
			}
		}
		writeError(w, fmt.Errorf("%w: vm %q", ErrNotFound, req.PathValue("id")))
	})
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
	mux.HandleFunc("PUT /api/vms/{id}/networks", func(w http.ResponseWriter, req *http.Request) {
		var q struct {
			NetworkIDs *[]string `json:"networkIds"`
		}
		if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
			writeError(w, fmt.Errorf("%w: %v", ErrInvalid, err))
			return
		}
		if q.NetworkIDs == nil {
			writeError(w, fmt.Errorf("%w: networkIds is required", ErrInvalid))
			return
		}
		vm, err := r.SetVMNetworks(req.PathValue("id"), *q.NetworkIDs, req.URL.Query().Get("restartAllowed") == "true")
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, vm)
	})
	mux.HandleFunc("PUT /api/vms/{id}/disks", func(w http.ResponseWriter, req *http.Request) {
		var q struct {
			Disks *[]AttachRequest `json:"disks"`
		}
		if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
			writeError(w, fmt.Errorf("%w: %v", ErrInvalid, err))
			return
		}
		if q.Disks == nil {
			writeError(w, fmt.Errorf("%w: disks is required", ErrInvalid))
			return
		}
		vm, err := r.SetVMDisks(req.PathValue("id"), *q.Disks, req.URL.Query().Get("restartAllowed") == "true")
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, vm)
	})
	for path, act := range map[string]struct {
		power proto.Power
		mode  proto.StopMode
	}{
		"start":    {proto.PowerRunning, ""},
		"shutdown": {proto.PowerStopped, proto.StopGraceful},
		"stop":     {proto.PowerStopped, proto.StopHard},
	} {
		mux.HandleFunc("POST /api/vms/{id}/"+path, func(w http.ResponseWriter, req *http.Request) {
			vm, err := r.SetPower(req.PathValue("id"), act.power, act.mode)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, vm)
		})
	}
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
	registerSettings(mux, r, cfg, net)
	return setupGuard(cfg, mux)
}

// deviceView is Device without its private key, which only leaves the hub inside a client config.
type deviceView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	IP        string    `json:"ip"`
	PublicKey string    `json:"publicKey"`
	CreatedAt time.Time `json:"createdAt"`
	OnHub     bool      `json:"onHub,omitempty"`
}

func viewOf(d Device) deviceView {
	return deviceView{ID: d.ID, Name: d.Name, IP: d.IP, PublicKey: d.PublicKey, CreatedAt: d.CreatedAt, OnHub: d.OnHub}
}

func deviceConfig(r *Registry, d Device, cfg Config) (string, error) {
	pub, err := r.clientsPublicKey()
	if err != nil {
		return "", err
	}
	endpoint := cfg.Clients.Endpoint
	if d.OnHub {
		// The macOS WireGuard app can't reach the hub machine's own LAN or Tailscale address; loopback works everywhere.
		endpoint = "127.0.0.1"
	}
	return DeviceConfig(d, pub, endpoint, cfg.Clients.Port, cfg.GuestMTU), nil
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
