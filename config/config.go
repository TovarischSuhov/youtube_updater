// Package config owns the channel→playlist configuration data model, its YAML
// parsing, and its mutation and persistence.
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ChannelMapping pairs a monitored YouTube channel with the target playlist its
// new uploads are added to, with human-readable names.
type ChannelMapping struct {
	ChannelID    string `yaml:"channel_id"`
	PlaylistID   string `yaml:"playlist_id"`
	ChannelName  string `yaml:"channel_name"`
	PlaylistName string `yaml:"playlist_name"`
}

// configFile mirrors the on-disk YAML structure: a top-level "channels:" list.
type configFile struct {
	Channels []ChannelMapping `yaml:"channels"`
}

// LoadConfig parses the YAML channel→playlist mapping at path into a list of
// ChannelMapping. An empty file yields an empty list, not an error. A missing
// file or a parse failure is returned as an error.
func LoadConfig(path string) ([]ChannelMapping, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return []ChannelMapping{}, nil
	}
	var f configFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Channels, nil
}

// SaveConfig writes the channel→playlist mappings to the config file as YAML.
// The write is atomic so readers never observe a partial file.
func SaveConfig(path string, mappings []ChannelMapping) error {
	b, err := yaml.Marshal(configFile{Channels: mappings})
	if err != nil {
		return err
	}
	return writeAtomic(path, b, 0o644)
}

// AddMapping upserts a channel→playlist pair (with names) into the list. If the
// channel already exists, its playlist and names are updated in place; otherwise
// a new entry is appended. Idempotent.
func AddMapping(mappings []ChannelMapping, channelID, playlistID, channelName, playlistName string) []ChannelMapping {
	for i := range mappings {
		if mappings[i].ChannelID == channelID {
			mappings[i].PlaylistID = playlistID
			mappings[i].ChannelName = channelName
			mappings[i].PlaylistName = playlistName
			return mappings
		}
	}
	return append(mappings, ChannelMapping{
		ChannelID:    channelID,
		PlaylistID:   playlistID,
		ChannelName:  channelName,
		PlaylistName: playlistName,
	})
}

// RemoveMapping returns the list without the pair for the given channel. It is a
// no-op (returns the same set) if the channel is absent.
func RemoveMapping(mappings []ChannelMapping, channelID string) []ChannelMapping {
	out := make([]ChannelMapping, 0, len(mappings))
	for _, m := range mappings {
		if m.ChannelID != channelID {
			out = append(out, m)
		}
	}
	return out
}

// writeAtomic writes data to path via a temp file in the same directory, then
// renames it into place so a crash or concurrent reader never sees a partial file.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
