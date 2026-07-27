package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestContractSurface is a compile-time check that the public API exists with the
// declared shape (facade accessibility + signatures).
func TestContractSurface(t *testing.T) {
	var cm ChannelMapping
	_ = cm.ChannelID
	_ = cm.PlaylistID
	_ = cm.ChannelName
	_ = cm.PlaylistName
	var _ func(string) ([]ChannelMapping, error) = LoadConfig
	var _ func(string, []ChannelMapping) error = SaveConfig
	var _ func([]ChannelMapping, string, string, string, string) []ChannelMapping = AddMapping
	var _ func([]ChannelMapping, string) []ChannelMapping = RemoveMapping
}

func TestLoadConfig_ParsesMappings(t *testing.T) {
	path := writeTemp(t, "channels:\n  - channel_id: UCa\n    playlist_id: PLa\n  - channel_id: UCb\n    playlist_id: PLb\n")
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(got))
	}
	if got[0].ChannelID != "UCa" || got[0].PlaylistID != "PLa" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].ChannelID != "UCb" || got[1].PlaylistID != "PLb" {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestLoadConfig_EmptyFile_ReturnsEmptyList(t *testing.T) {
	path := writeTemp(t, "")
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("expected nil error for empty file, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %d", len(got))
	}
}

func TestLoadConfig_MalformedYAML_ReturnsError(t *testing.T) {
	path := writeTemp(t, "channels: [ { channel_id: UCa ]")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
}

func TestLoadConfig_MissingFile_ReturnsError(t *testing.T) {
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestAddMapping_AddsNewPair(t *testing.T) {
	got := AddMapping(nil, "UCa", "PLa", "Foo", "Bar")
	if len(got) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(got))
	}
	want := ChannelMapping{ChannelID: "UCa", PlaylistID: "PLa", ChannelName: "Foo", PlaylistName: "Bar"}
	if got[0] != want {
		t.Errorf("got %+v, want %+v", got[0], want)
	}
}

func TestAddMapping_UpsertsExisting(t *testing.T) {
	in := []ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa", ChannelName: "OldCh", PlaylistName: "OldPl"}}
	got := AddMapping(in, "UCa", "PLb", "NewCh", "NewPl")
	if len(got) != 1 {
		t.Fatalf("expected 1 mapping (upsert), got %d", len(got))
	}
	want := ChannelMapping{ChannelID: "UCa", PlaylistID: "PLb", ChannelName: "NewCh", PlaylistName: "NewPl"}
	if got[0] != want {
		t.Errorf("got %+v, want %+v", got[0], want)
	}
}

func TestRemoveMapping_NoOpIfAbsent(t *testing.T) {
	in := []ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa", ChannelName: "Foo", PlaylistName: "Bar"}}
	got := RemoveMapping(in, "UCz")
	if len(got) != 1 || got[0].ChannelID != "UCa" {
		t.Fatalf("remove of absent channel changed the list: %+v", got)
	}
}

func TestRemoveMapping_RemovesPair(t *testing.T) {
	in := []ChannelMapping{
		{ChannelID: "UCa", PlaylistID: "PLa"},
		{ChannelID: "UCb", PlaylistID: "PLb"},
	}
	got := RemoveMapping(in, "UCa")
	if len(got) != 1 || got[0].ChannelID != "UCb" {
		t.Fatalf("expected only UCb left, got %+v", got)
	}
}

func TestSaveConfig_RoundTripsNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	in := []ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa", ChannelName: "Foo", PlaylistName: "Bar"}}
	if err := SaveConfig(path, in); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(got) != 1 || got[0] != in[0] {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, in)
	}
}

func TestSaveConfig_EmptyMappings_WritesValidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := SaveConfig(path, nil); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list after saving nil, got %d", len(got))
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
