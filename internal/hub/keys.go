package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

var keyTypes = []string{
	"ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
	"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com",
}

type SSHKey struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PublicKey   string `json:"publicKey"`
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	// Hub keys come from the hub machine's ~/.ssh; they are read on start and never written to the file.
	Hub bool `json:"hub,omitempty"`
}

// KeyStore keeps SSH public keys in a JSON file so they survive hub restarts.
type KeyStore struct {
	mu      sync.Mutex
	path    string
	hubKeys []SSHKey
	keys    []SSHKey
}

func LoadKeyStore(path string) (*KeyStore, error) {
	s := &KeyStore{path: path, hubKeys: []SSHKey{}, keys: []SSHKey{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.keys); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// HubKeyFiles are the public keys ssh itself uses by default, inside sshDir.
func HubKeyFiles(sshDir string) []string {
	var out []string
	for _, n := range []string{"id_rsa", "id_ecdsa", "id_ecdsa_sk", "id_ed25519", "id_ed25519_sk"} {
		out = append(out, filepath.Join(sshDir, n+".pub"))
	}
	return out
}

// LoadHubKeys adds the readable files as undeletable hub keys and drops stored copies of them.
func (s *KeyStore) LoadHubKeys(files []string) ([]SSHKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range files {
		b, err := os.ReadFile(f)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		k, err := newKey(filepath.Base(f), string(b))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		k.ID, k.Hub = "hub-"+strings.TrimSuffix(filepath.Base(f), ".pub"), true
		s.hubKeys = append(s.hubKeys, k)
	}
	kept := slices.DeleteFunc(slices.Clone(s.keys), func(k SSHKey) bool { return s.hubKey(k.Fingerprint) })
	if len(kept) == len(s.keys) {
		return s.hubKeys, nil
	}
	if err := s.save(kept); err != nil {
		return nil, err
	}
	s.keys = kept
	return s.hubKeys, nil
}

func (s *KeyStore) hubKey(fingerprint string) bool {
	return slices.ContainsFunc(s.hubKeys, func(h SSHKey) bool { return h.Fingerprint == fingerprint })
}

func (s *KeyStore) List() []SSHKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Concat(s.hubKeys, s.keys)
}

func newKey(name, publicKey string) (SSHKey, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return SSHKey{}, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	typ, blob, err := parsePublicKey(publicKey)
	if err != nil {
		return SSHKey{}, err
	}
	sum := sha256.Sum256(blob)
	return SSHKey{
		ID: newID(), Name: name, Type: typ,
		PublicKey:   strings.Join(strings.Fields(publicKey), " "),
		Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]),
	}, nil
}

func (s *KeyStore) Add(name, publicKey string) (SSHKey, error) {
	k, err := newKey(name, publicKey)
	if err != nil {
		return SSHKey{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range slices.Concat(s.hubKeys, s.keys) {
		if e.Fingerprint == k.Fingerprint {
			return SSHKey{}, fmt.Errorf("%w: key is already stored as %q", ErrConflict, e.Name)
		}
	}
	if err := s.save(append(slices.Clone(s.keys), k)); err != nil {
		return SSHKey{}, err
	}
	s.keys = append(s.keys, k)
	return k, nil
}

func (s *KeyStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.ContainsFunc(s.hubKeys, func(k SSHKey) bool { return k.ID == id }) {
		return fmt.Errorf("%w: key %q comes from the hub machine and cannot be deleted", ErrConflict, id)
	}
	i := slices.IndexFunc(s.keys, func(k SSHKey) bool { return k.ID == id })
	if i < 0 {
		return fmt.Errorf("%w: key %q", ErrNotFound, id)
	}
	keys := slices.Delete(slices.Clone(s.keys), i, i+1)
	if err := s.save(keys); err != nil {
		return err
	}
	s.keys = keys
	return nil
}

// PublicKeys resolves key IDs to their public keys, in the given order.
func (s *KeyStore) PublicKeys(ids []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := slices.Concat(s.hubKeys, s.keys)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		i := slices.IndexFunc(all, func(k SSHKey) bool { return k.ID == id })
		if i < 0 {
			return nil, fmt.Errorf("%w: unknown SSH key %q", ErrInvalid, id)
		}
		out = append(out, all[i].PublicKey)
	}
	return out, nil
}

// save writes via a temp file and rename so a crash never leaves a half-written file.
func (s *KeyStore) save(keys []SSHKey) error {
	b, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// parsePublicKey checks an authorized_keys line: the declared type must be known and match the type inside the blob.
func parsePublicKey(line string) (typ string, blob []byte, err error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return "", nil, fmt.Errorf("%w: public key must look like \"ssh-ed25519 AAAA… comment\"", ErrInvalid)
	}
	typ = fields[0]
	if !slices.Contains(keyTypes, typ) {
		return "", nil, fmt.Errorf("%w: unsupported key type %q", ErrInvalid, typ)
	}
	blob, err = base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return "", nil, fmt.Errorf("%w: public key is not valid base64", ErrInvalid)
	}
	if len(blob) < 4 {
		return "", nil, fmt.Errorf("%w: public key is too short", ErrInvalid)
	}
	n := binary.BigEndian.Uint32(blob)
	if uint64(len(blob)) < 4+uint64(n) || !bytes.Equal(blob[4:4+n], []byte(typ)) {
		return "", nil, fmt.Errorf("%w: public key data does not match type %q", ErrInvalid, typ)
	}
	return typ, blob, nil
}
