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
	var _ func([]string) int = s.PruneExcept
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

func TestState_PruneExcept_RemovesStaleKeepsConfigured(t *testing.T) {
	s, err := NewState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	cursors := map[string][2]string{
		"UCa":     {"v-a", "2026-08-01T10:00:00Z"},
		"UCb":     {"v-b", "2026-08-02T11:00:00Z"},
		"UCstale": {"v-old", "2026-07-01T09:00:00Z"},
	}
	for ch, vv := range cursors {
		if err := s.SetLastSeen(ch, vv[0], vv[1]); err != nil {
			t.Fatalf("SetLastSeen(%s) error: %v", ch, err)
		}
	}

	removed := s.PruneExcept([]string{"UCa", "UCb"})
	if removed != 1 {
		t.Fatalf("PruneExcept removed = %d, want 1", removed)
	}
	if s.IsSeeded("UCstale") || s.LastSeenID("UCstale") != "" || s.LastSeenAt("UCstale") != "" {
		t.Errorf("stale cursor survived prune: seeded=%t id=%q at=%q",
			s.IsSeeded("UCstale"), s.LastSeenID("UCstale"), s.LastSeenAt("UCstale"))
	}
	for ch, vv := range cursors {
		if ch == "UCstale" {
			continue
		}
		if !s.IsSeeded(ch) || s.LastSeenID(ch) != vv[0] || s.LastSeenAt(ch) != vv[1] || s.LastSync(ch) == "" {
			t.Errorf("kept cursor %s damaged by prune: seeded=%t id=%q at=%q sync=%q, want id=%q at=%q",
				ch, s.IsSeeded(ch), s.LastSeenID(ch), s.LastSeenAt(ch), s.LastSync(ch), vv[0], vv[1])
		}
	}
}

func TestState_PruneExcept_EmptyKeepRemovesAll(t *testing.T) {
	s, err := NewState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range []string{"UCa", "UCb"} {
		if err := s.SetLastSeen(ch, "v", "2026-08-01T10:00:00Z"); err != nil {
			t.Fatalf("SetLastSeen(%s) error: %v", ch, err)
		}
	}
	if removed := s.PruneExcept([]string{}); removed != 2 {
		t.Fatalf("PruneExcept([]) removed = %d, want 2", removed)
	}
	if s.IsSeeded("UCa") || s.IsSeeded("UCb") {
		t.Errorf("empty keep must remove every cursor: UCa=%t UCb=%t", s.IsSeeded("UCa"), s.IsSeeded("UCb"))
	}
}

func TestState_PruneExcept_EmptyState_NoOp(t *testing.T) {
	s, err := NewState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if removed := s.PruneExcept([]string{"UCa"}); removed != 0 {
		t.Fatalf("PruneExcept on empty state removed = %d, want 0", removed)
	}
}

func TestState_PruneExcept_RoundTripAfterSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := NewState(path)
	if err != nil {
		t.Fatal(err)
	}
	cursors := map[string][2]string{
		"UCa":     {"v-a", "2026-08-01T10:00:00Z"},
		"UCb":     {"v-b", "2026-08-02T11:00:00Z"},
		"UCstale": {"v-old", "2026-07-01T09:00:00Z"},
	}
	for ch, vv := range cursors {
		if err := s.SetLastSeen(ch, vv[0], vv[1]); err != nil {
			t.Fatalf("SetLastSeen(%s) error: %v", ch, err)
		}
	}
	if removed := s.PruneExcept([]string{"UCa", "UCb"}); removed != 1 {
		t.Fatalf("PruneExcept removed = %d, want 1", removed)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// A corrupt state.json would fail right here: the file format is unchanged.
	again, err := NewState(path)
	if err != nil {
		t.Fatalf("reload after prune+save: %v", err)
	}
	if again.IsSeeded("UCstale") || again.LastSeenID("UCstale") != "" {
		t.Errorf("stale cursor persisted after prune+save: seeded=%t id=%q",
			again.IsSeeded("UCstale"), again.LastSeenID("UCstale"))
	}
	for ch, vv := range cursors {
		if ch == "UCstale" {
			continue
		}
		if !again.IsSeeded(ch) || again.LastSeenID(ch) != vv[0] || again.LastSeenAt(ch) != vv[1] {
			t.Errorf("kept cursor %s did not round-trip: seeded=%t id=%q at=%q, want id=%q at=%q",
				ch, again.IsSeeded(ch), again.LastSeenID(ch), again.LastSeenAt(ch), vv[0], vv[1])
		}
	}
}
