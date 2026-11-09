package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

func APIHandler(r *Registry, keys *KeyStore) http.Handler {
	mux := http.NewServeMux()
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
