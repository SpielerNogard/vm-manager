package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

type StoredDisk struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	SizeGB    int       `json:"sizeGB"`
	CopyFrom  string    `json:"copyFrom,omitempty"` // cleared once the storage agent reports the file
	CreatedAt time.Time `json:"createdAt"`
	// ReleasingOn is the node that must confirm it stopped using the disk before it can be attached again.
	ReleasingOn string `json:"releasingOn,omitempty"`
}

type diskState struct {
	Disks      []StoredDisk `json:"disks"`
	Tombstones []string     `json:"tombstones"`
	Endpoint   string       `json:"endpoint"`
}

// DiskStore holds the hub's standalone disks, deleted-disk tombstones and the storage endpoint.
type DiskStore struct {
	mu         sync.Mutex
	path       string
	disks      map[string]StoredDisk
	tombstones map[string]struct{}
	endpoint   string
}

func LoadDiskStore(path string) (*DiskStore, error) {
	s := &DiskStore{path: path, disks: map[string]StoredDisk{}, tombstones: map[string]struct{}{}}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	var st diskState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, d := range st.Disks {
		s.disks[d.ID] = d
	}
	for _, id := range st.Tombstones {
		s.tombstones[id] = struct{}{}
	}
	s.endpoint = st.Endpoint
	return s, nil
}

func (s *DiskStore) sortedDisks() []StoredDisk {
	out := make([]StoredDisk, 0, len(s.disks))
	for _, d := range s.disks {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b StoredDisk) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (s *DiskStore) sortedTombstones() []string {
	return slices.Sorted(maps.Keys(s.tombstones))
}

// mutate applies f and rolls the state back if saving fails.
func (s *DiskStore) mutate(f func()) error {
	prevDisks, prevTombs, prevEndpoint := maps.Clone(s.disks), maps.Clone(s.tombstones), s.endpoint
	f()
	st := diskState{Disks: s.sortedDisks(), Tombstones: s.sortedTombstones(), Endpoint: s.endpoint}
	if err := saveJSON(s.path, st); err != nil {
		s.disks, s.tombstones, s.endpoint = prevDisks, prevTombs, prevEndpoint
		return err
	}
	return nil
}

func (s *DiskStore) Disks() []StoredDisk {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedDisks()
}

func (s *DiskStore) Get(id string) (StoredDisk, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.disks[id]
	return d, ok
}

func (s *DiskStore) Put(d StoredDisk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(func() { s.disks[d.ID] = d })
}

func (s *DiskStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(func() {
		delete(s.disks, id)
		s.tombstones[id] = struct{}{}
	})
}

func (s *DiskStore) Tombstones() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedTombstones()
}

func (s *DiskStore) ClearTombstone(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(func() { delete(s.tombstones, id) })
}

func (s *DiskStore) Endpoint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endpoint
}

func (s *DiskStore) SetEndpoint(ep string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutate(func() { s.endpoint = ep })
}
