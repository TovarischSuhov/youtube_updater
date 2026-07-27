// Package state owns the persisted per-channel "last seen video" cursor store —
// the sole source of truth for which videos have been processed.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// State is the persisted per-channel "last seen video" cursor store.
type State struct {
	Path     string // state file path (facade property)
	channels map[string]cursor
}

// cursor is the per-channel record (unexported; accessed via State methods).
type cursor struct {
	Seeded     bool   `json:"seeded"`
	LastSeenID string `json:"last_seen_id"`
	LastSeenAt string `json:"last_seen_at"`
	LastSync   string `json:"last_sync"`
}

// NewState loads the state file at path. A missing or empty file yields an empty
// store (no error). A corrupt (non-empty, invalid JSON) file returns an error.
func NewState(path string) (*State, error) {
	s := &State{Path: path, channels: map[string]cursor{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.channels); err != nil {
		return nil, fmt.Errorf("state: parse %q: %w", path, err)
	}
	return s, nil
}

// IsSeeded reports whether a first contact has ever been recorded for the channel.
func (s *State) IsSeeded(channelID string) bool {
	return s.channels[channelID].Seeded
}

// LastSeenID returns the identifier of the last seen video for the channel
// (empty if the channel is unseeded).
func (s *State) LastSeenID(channelID string) string {
	return s.channels[channelID].LastSeenID
}

// LastSeenAt returns the publish-time watermark (RFC 3339) for the channel
// (empty if the channel is unseeded).
func (s *State) LastSeenAt(channelID string) string {
	return s.channels[channelID].LastSeenAt
}

// SetLastSeen records the last seen video for a channel and marks it seeded.
// publishedAt must be a non-empty RFC 3339 timestamp.
func (s *State) SetLastSeen(channelID, videoID, publishedAt string) error {
	if _, err := time.Parse(time.RFC3339, publishedAt); err != nil {
		return fmt.Errorf("state: invalid publishedAt %q: %w", publishedAt, err)
	}
	s.channels[channelID] = cursor{
		Seeded:     true,
		LastSeenID: videoID,
		LastSeenAt: publishedAt,
		LastSync:   time.Now().UTC().Format(time.RFC3339),
	}
	return nil
}

// Save persists all cursors to the state file atomically: a crash or concurrent
// reader never observes a partially-written file.
func (s *State) Save() error {
	b, err := json.MarshalIndent(s.channels, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once rename succeeds
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}
