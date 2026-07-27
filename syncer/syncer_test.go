package syncer

import (
	"testing"

	"youtube-updater/config"
	"youtube-updater/state"
	"youtube-updater/youtube"
)

// TestContractSurface is a compile-time check of the public API shape.
func TestContractSurface(t *testing.T) {
	var r ChannelResult
	_ = r.ChannelID
	_ = r.PlaylistID
	_ = r.Seeded
	_ = r.NewCount
	_ = r.AddedIDs
	_ = r.Err
	var _ func(*youtube.YouTube, *state.State, []config.ChannelMapping, bool) ([]ChannelResult, error) = SyncAll
}

func TestSelectNew_FiltersByWatermark(t *testing.T) {
	videos := []youtube.Video{
		{ID: "v5", PublishedAt: "2026-07-27T15:00:00Z"},
		{ID: "v3", PublishedAt: "2026-07-27T13:00:00Z"},
		{ID: "v1", PublishedAt: "2026-07-27T11:00:00Z"},
	}
	got := selectNew(videos, "2026-07-27T12:00:00Z")
	if len(got) != 2 {
		t.Fatalf("expected 2 new, got %d", len(got))
	}
	if got[0].ID != "v5" || got[1].ID != "v3" {
		t.Errorf("newest-first order wrong: %+v", got)
	}
}

func TestSelectNew_EmptyWatermark_SelectsAll(t *testing.T) {
	videos := []youtube.Video{
		{ID: "a", PublishedAt: "2026-07-27T10:00:00Z"},
		{ID: "b", PublishedAt: "2026-07-27T09:00:00Z"},
	}
	got := selectNew(videos, "")
	if len(got) != 2 {
		t.Fatalf("expected all selected for empty watermark, got %d", len(got))
	}
}

// Proves comparison is time-based, not lexical: 12:00+02:00 == 10:00Z, which is
// BEFORE the 11:00Z watermark. A lexical comparison would wrongly select it.
func TestSelectNew_ParsesToTime_NotLexical(t *testing.T) {
	videos := []youtube.Video{{ID: "x", PublishedAt: "2026-07-27T12:00:00+02:00"}}
	got := selectNew(videos, "2026-07-27T11:00:00Z")
	if len(got) != 0 {
		t.Fatalf("expected 0 (time-based), got %d", len(got))
	}
}

func TestSelectNew_ExcludesEqualTimestamp(t *testing.T) {
	// The last-seen video itself (equal timestamp) must not be re-selected.
	videos := []youtube.Video{{ID: "same", PublishedAt: "2026-07-27T11:00:00Z"}}
	got := selectNew(videos, "2026-07-27T11:00:00Z")
	if len(got) != 0 {
		t.Fatalf("expected 0 (equal not after), got %d", len(got))
	}
}
