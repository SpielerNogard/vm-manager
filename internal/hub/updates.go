package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"vm-manager/internal/version"
)

const (
	releaseAPI    = "https://api.github.com/repos/SpielerNogard/vm-manager/releases/latest"
	checkInterval = 6 * time.Hour
)

// Updates remembers the latest release GitHub reports; it lives in memory only.
type Updates struct {
	URL string
	// MinInterval is how soon after a check an on-demand check asks again; GitHub allows 60 requests an hour.
	MinInterval time.Duration
	client      *http.Client

	mu        sync.Mutex
	latest    string
	checkedAt time.Time
	err       string
}

type UpdateStatus struct {
	Version         string     `json:"version"`
	Gateway         string     `json:"gateway,omitempty"`
	Storage         string     `json:"storage,omitempty"`
	Latest          string     `json:"latest,omitempty"`
	CheckedAt       *time.Time `json:"checkedAt,omitempty"`
	CheckError      string     `json:"checkError,omitempty"`
	UpdateAvailable bool       `json:"updateAvailable"`
	// HubUpdate is "none" on a dev build and "manual" otherwise: the operator upgrades the stack.
	HubUpdate string     `json:"hubUpdate"`
	Stack     StackState `json:"stack"`
}

func NewUpdates() *Updates {
	return &Updates{URL: releaseAPI, MinInterval: 30 * time.Second, client: &http.Client{Timeout: 15 * time.Second}}
}

// Check asks for the latest release once; a failure is recorded and keeps the last known release.
func (u *Updates) Check(ctx context.Context) error {
	latest, err := u.fetch(ctx)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.checkedAt, u.err = time.Now().UTC(), ""
	if err != nil {
		u.err = err.Error()
		return err
	}
	u.latest = latest
	return nil
}

// CheckNow is Check for a click: it does not ask again within MinInterval of the last check.
func (u *Updates) CheckNow(ctx context.Context) {
	u.mu.Lock()
	recent := !u.checkedAt.IsZero() && time.Since(u.checkedAt) < u.MinInterval
	u.mu.Unlock()
	if !recent {
		u.Check(ctx)
	}
}

func (u *Updates) fetch(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "vm-manager")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release check: %s answered %s", u.URL, resp.Status)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return "", fmt.Errorf("release check: %w", err)
	}
	if !version.Released(rel.TagName) {
		return "", fmt.Errorf("release check: latest release has tag %q, want vX.Y.Z", rel.TagName)
	}
	return rel.TagName, nil
}

// Run checks now and every checkInterval until ctx ends; a dev build has no version to compare and never asks.
func (u *Updates) Run(ctx context.Context) {
	if !version.Released(version.Version) {
		return
	}
	for {
		if err := u.Check(ctx); err != nil && ctx.Err() == nil {
			log.Print(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(checkInterval):
		}
	}
}

func (u *Updates) Status(r *Registry, s *Stack) UpdateStatus {
	st := UpdateStatus{Version: version.Version, Gateway: r.GatewayVersion(), Storage: r.Storage().Version, HubUpdate: "none", Stack: s.State()}
	if !version.Released(version.Version) {
		return st
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	st.HubUpdate, st.Latest, st.CheckError = "manual", u.latest, u.err
	if !u.checkedAt.IsZero() {
		t := u.checkedAt
		st.CheckedAt = &t
	}
	st.UpdateAvailable = version.Older(version.Version, u.latest)
	return st
}

var errDevBuild = errors.New("this hub is a development build without a version to compare")
