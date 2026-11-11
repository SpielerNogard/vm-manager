package hub

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testKey         = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIO8jQbx5o/2uWCv+t8mWSfYyBcRdBAOQ6PyTAX1/QwXq test@vmm"
	testFingerprint = "SHA256:DaWEr3KCEzBPKTL6rd29EpyOVBYyqmIcf4ygx3QXzhU"
	otherKey        = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINfsvMFrp4YLja7MpF9y1Bl+B7skjHWKHqpOLGKt2qU/ other"
)

func TestKeyStoreAddPersistsAndDeletes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "keys.json")
	s, err := LoadKeyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.Add(" laptop ", "  "+strings.ReplaceAll(testKey, " ", "   ")+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if k.Name != "laptop" || k.PublicKey != testKey || k.Type != "ssh-ed25519" || k.Fingerprint != testFingerprint {
		t.Fatalf("key = %+v", k)
	}

	reloaded, err := LoadKeyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.List(); len(got) != 1 || got[0] != k {
		t.Fatalf("reloaded = %+v", got)
	}
	if pks, err := reloaded.PublicKeys([]string{k.ID}); err != nil || len(pks) != 1 || pks[0] != testKey {
		t.Fatalf("PublicKeys = %v, %v", pks, err)
	}

	if err := reloaded.Delete(k.ID); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Delete(k.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
	if again, _ := LoadKeyStore(path); len(again.List()) != 0 {
		t.Fatalf("after delete = %+v", again.List())
	}
}

func TestKeyStoreRejects(t *testing.T) {
	s, _ := LoadKeyStore(filepath.Join(t.TempDir(), "keys.json"))
	if _, err := s.Add("a", testKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("b", testKey); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate err = %v", err)
	}
	blob := strings.Fields(testKey)[1]
	for name, key := range map[string]string{
		"no blob":       "ssh-ed25519",
		"unknown type":  "ssh-dss " + blob,
		"bad base64":    "ssh-ed25519 !!!",
		"type mismatch": "ssh-rsa " + blob,
		"short":         "ssh-ed25519 AAA=",
		"private key":   "-----BEGIN OPENSSH PRIVATE KEY-----",
	} {
		if _, err := s.Add("x", key); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := s.Add(" ", strings.Replace(testKey, "test@vmm", "other", 1)); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty name: err = %v", err)
	}
	if _, err := s.PublicKeys([]string{"nope"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown id: err = %v", err)
	}
}

func TestAPIKeys(t *testing.T) {
	srv := apiServer(t)
	post := func(body string) int {
		resp, err := http.Post(srv.URL+"/api/keys", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if s := post(`{"name":"dup","publicKey":"` + testKey + `"}`); s != http.StatusConflict {
		t.Fatalf("duplicate status = %d", s)
	}
	if s := post(`{"name":"bad","publicKey":"nope"}`); s != http.StatusBadRequest {
		t.Fatalf("invalid status = %d", s)
	}
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/keys/k1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	if r, out := postVM(t, srv, validBody); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with deleted key: status = %d body = %v", r.StatusCode, out)
	}
}

func TestHubKeyFilesDir(t *testing.T) {
	files := HubKeyFiles("/x")
	if len(files) != 5 || files[3] != "/x/id_ed25519.pub" || files[0] != "/x/id_rsa.pub" {
		t.Fatalf("files = %v", files)
	}
}

func TestKeyStoreHubKeys(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519.pub"), []byte(testKey+"\n"), 0o600)
	os.WriteFile(filepath.Join(home, ".ssh", "other.pub"), []byte(otherKey), 0o600)

	path := filepath.Join(dir, "keys.json")
	s, _ := LoadKeyStore(path)
	if _, err := s.Add("copy", testKey); err != nil {
		t.Fatal(err)
	}

	s, _ = LoadKeyStore(path)
	hubKeys, err := s.LoadHubKeys(HubKeyFiles(filepath.Join(home, ".ssh")))
	if err != nil {
		t.Fatal(err)
	}
	want := SSHKey{ID: "hub-id_ed25519", Name: "id_ed25519.pub", PublicKey: testKey, Type: "ssh-ed25519", Fingerprint: testFingerprint, Hub: true}
	if len(hubKeys) != 1 || hubKeys[0] != want {
		t.Fatalf("hub keys = %+v", hubKeys)
	}
	// The stored copy is dropped, so the key is listed once, and other.pub is not a default key.
	if got := s.List(); len(got) != 1 || got[0] != want {
		t.Fatalf("list = %+v", got)
	}
	if again, _ := LoadKeyStore(path); len(again.List()) != 0 {
		t.Fatalf("stored copy kept: %+v", again.List())
	}
	if _, err := s.Add("dup", testKey); !errors.Is(err, ErrConflict) {
		t.Fatalf("add hub key again: err = %v", err)
	}
	if err := s.Delete("hub-id_ed25519"); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete hub key: err = %v", err)
	}
	if pks, err := s.PublicKeys([]string{"hub-id_ed25519"}); err != nil || pks[0] != testKey {
		t.Fatalf("PublicKeys = %v, %v", pks, err)
	}
}
