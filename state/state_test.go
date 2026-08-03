package state

import (
	"os"
	"path/filepath"
	"testing"
)

// TestContractSurface is a compile-time check of the public API shape.
func TestContractSurface(t *testing.T) {
	var s State
	_ = s.Path // property (field)
	var _ func(string) bool = s.IsSeeded
	var _ func(string) string = s.LastSeenID
	var _ func(string) string = s.LastSeenAt
	var _ func(string) string = s.LastSync
	var _ func(string, string, string) error = s.SetLastSeen
	var _ func() error = s.Save
	var _ func(string) (*State, error) = NewState
}

func TestState_MissingFile_StartsEmpty(t *testing.T) {
	s, err := NewState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewState error: %v", err)
	}
	if s.IsSeeded("UC1") {
		t.Fatal("expected unseeded for missing file")
	}
	if s.LastSeenID("UC1") != "" || s.LastSeenAt("UC1") != "" {
		t.Fatal("expected empty getters for unseeded channel")
	}
}

func TestState_SeedThenSaveThenReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := NewState(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastSeen("UC1", "v1", "2026-07-27T10:00:00Z"); err != nil {
		t.Fatalf("SetLastSeen error: %v", err)
	}
	if !s.IsSeeded("UC1") {
		t.Fatal("expected seeded after SetLastSeen")
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// File written with mode 0600.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600, got %v", info.Mode().Perm())
	}

	// Reload round-trips the cursor.
	again, err := NewState(path)
	if err != nil {
		t.Fatalf("reload error: %v", err)
	}
	if !again.IsSeeded("UC1") || again.LastSeenID("UC1") != "v1" || again.LastSeenAt("UC1") != "2026-07-27T10:00:00Z" {
		t.Errorf("reload mismatch: %+v", again)
	}
}

func TestState_CorruptFile_ReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewState(path); err == nil {
		t.Fatal("expected error for corrupt file, got nil")
	}
}

func TestState_SetLastSeen_RejectsBadTimestamp(t *testing.T) {
	s, _ := NewState(filepath.Join(t.TempDir(), "state.json"))
	if err := s.SetLastSeen("UC1", "v1", "not-a-timestamp"); err == nil {
		t.Fatal("expected error for malformed timestamp")
	}
	if err := s.SetLastSeen("UC1", "v1", ""); err == nil {
		t.Fatal("expected error for empty timestamp")
	}
}

func TestState_LastSync_EmptyUntilSeeded(t *testing.T) {
	s, _ := NewState(filepath.Join(t.TempDir(), "state.json"))
	if got := s.LastSync("UC1"); got != "" {
		t.Errorf("LastSync unseeded = %q, want empty", got)
	}
	if err := s.SetLastSeen("UC1", "v1", "2026-07-27T10:00:00Z"); err != nil {
		t.Fatalf("SetLastSeen error: %v", err)
	}
	if got := s.LastSync("UC1"); got == "" {
		t.Error("LastSync empty after SetLastSeen; want a timestamp")
	}
}
