package youtube_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/option"
	youtubev3 "google.golang.org/api/youtube/v3"

	"youtube-updater/config"
	"youtube-updater/state"
	"youtube-updater/syncer"
	"youtube-updater/youtube"
)

// harness is an httptest-backed fake YouTube API serving per-channel upload lists
// and recording playlist inserts.
type harness struct {
	ts      *httptest.Server
	mu      sync.Mutex
	inserts []string // inserted videoIds, in order
}

// newHarness builds a fake server. byChannel maps channelID → its uploads
// (newest-first); notFound maps channelID → true to make ResolveUploads 404; kind
// maps videoID → "short"|"stream" to drive the videos.list classifier (absent =
// regular long-form). All videos default to regular when kind is nil/empty.
func newHarness(t *testing.T, byChannel map[string][]youtube.Video, notFound map[string]bool, kind map[string]string) *harness {
	t.Helper()
	h := &harness{}
	h.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/youtube/v3/channels":
			id := r.URL.Query().Get("id")
			if notFound[id] {
				fmt.Fprint(w, `{"items":[]}`)
				return
			}
			fmt.Fprintf(w, `{"items":[{"id":%q,"contentDetails":{"relatedPlaylists":{"uploads":%q}}}]}`, id, "UU"+id)
		case "/youtube/v3/videos":
			var ids []string
			for _, v := range r.URL.Query()["id"] {
				ids = append(ids, strings.Split(v, ",")...)
			}
			var items []string
			for _, id := range ids {
				switch kind[id] {
				case "short":
					items = append(items, fmt.Sprintf(`{"id":%q,"contentDetails":{"duration":"PT30S"}}`, id))
				case "stream":
					items = append(items, fmt.Sprintf(`{"id":%q,"contentDetails":{"duration":"PT1H"},"liveStreamingDetails":{"actualStartTime":"2026-01-01T00:00:00Z"}}`, id))
				default:
					items = append(items, fmt.Sprintf(`{"id":%q,"contentDetails":{"duration":"PT10M"}}`, id))
				}
			}
			fmt.Fprintf(w, `{"items":[%s]}`, strings.Join(items, ","))
		case "/youtube/v3/playlistItems":
			if r.Method == http.MethodGet {
				pl := r.URL.Query().Get("playlistId")
				channelID := strings.TrimPrefix(pl, "UU")
				writePlaylistItems(w, byChannel[channelID])
				return
			}
			body, _ := io.ReadAll(r.Body)
			h.mu.Lock()
			h.inserts = append(h.inserts, parseInsertVideoID(body))
			h.mu.Unlock()
			fmt.Fprint(w, `{"id":"item"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.ts.Close)
	return h
}

func (h *harness) youTube(t *testing.T) *youtube.YouTube {
	t.Helper()
	svc, err := youtubev3.NewService(context.Background(), option.WithEndpoint(h.ts.URL), option.WithHTTPClient(h.ts.Client()))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return youtube.NewWithService(svc)
}

func writePlaylistItems(w io.Writer, videos []youtube.Video) {
	if len(videos) == 0 {
		fmt.Fprint(w, `{"items":[]}`)
		return
	}
	parts := make([]string, len(videos))
	for i, v := range videos {
		parts[i] = fmt.Sprintf(`{"contentDetails":{"videoId":%q,"videoPublishedAt":%q}}`, v.ID, v.PublishedAt)
	}
	fmt.Fprintf(w, `{"items":[%s]}`, strings.Join(parts, ","))
}

func parseInsertVideoID(b []byte) string {
	var req struct {
		Snippet struct {
			ResourceID struct {
				VideoID string `json:"videoId"`
			} `json:"resourceId"`
		} `json:"snippet"`
	}
	_ = json.Unmarshal(b, &req)
	return req.Snippet.ResourceID.VideoID
}

func freshState(t *testing.T) *state.State {
	t.Helper()
	st, err := state.NewState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func seededState(t *testing.T, channelID, publishedAt string) *state.State {
	t.Helper()
	st := freshState(t)
	if err := st.SetLastSeen(channelID, "seed-video", publishedAt); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSyncAll_AddsOnlyNewVideos_AdvancesCursor(t *testing.T) {
	vids := []youtube.Video{
		{ID: "v5", PublishedAt: "2026-07-27T15:00:00Z"},
		{ID: "v1", PublishedAt: "2026-07-27T13:00:00Z"},
		{ID: "old", PublishedAt: "2026-07-27T09:00:00Z"},
	}
	h := newHarness(t, map[string][]youtube.Video{"UCa": vids}, nil, nil)
	st := seededState(t, "UCa", "2026-07-27T10:00:00Z") // watermark 10:00

	res, err := syncer.SyncAll(h.youTube(t), st, []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].NewCount != 2 {
		t.Errorf("NewCount=%d, want 2", res[0].NewCount)
	}
	if len(h.inserts) != 2 {
		t.Fatalf("inserts=%v, want 2", h.inserts)
	}
	// oldest→newest insertion order: v1 (13:00) then v5 (15:00)
	if h.inserts[0] != "v1" || h.inserts[1] != "v5" {
		t.Errorf("insert order=%v, want [v1 v5]", h.inserts)
	}
	if st.LastSeenAt("UCa") != "2026-07-27T15:00:00Z" {
		t.Errorf("cursor=%q, want newest (15:00)", st.LastSeenAt("UCa"))
	}
}

func TestSyncAll_SeedsOnFirstContact_AddsNothing(t *testing.T) {
	vids := []youtube.Video{
		{ID: "v5", PublishedAt: "2026-07-27T15:00:00Z"},
		{ID: "v3", PublishedAt: "2026-07-27T13:00:00Z"},
	}
	h := newHarness(t, map[string][]youtube.Video{"UCa": vids}, nil, nil)
	st := freshState(t) // unseeded

	res, err := syncer.SyncAll(h.youTube(t), st, []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Seeded || res[0].NewCount != 0 {
		t.Errorf("result=%+v, want Seeded+NewCount 0", res[0])
	}
	if len(h.inserts) != 0 {
		t.Fatalf("expected no inserts, got %v", h.inserts)
	}
	if st.LastSeenAt("UCa") != "2026-07-27T15:00:00Z" {
		t.Errorf("cursor=%q, want newest", st.LastSeenAt("UCa"))
	}
}

func TestSyncAll_EmptyUploads_SkipsChannel_NoCursor(t *testing.T) {
	h := newHarness(t, map[string][]youtube.Video{"UCa": nil}, nil, nil)
	st := freshState(t) // unseeded

	res, err := syncer.SyncAll(h.youTube(t), st, []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].NewCount != 0 {
		t.Errorf("NewCount=%d, want 0", res[0].NewCount)
	}
	if st.IsSeeded("UCa") {
		t.Error("channel should stay unseeded on empty uploads")
	}
	if len(h.inserts) != 0 {
		t.Fatalf("expected no inserts, got %v", h.inserts)
	}
}

func TestSyncAll_NoNewVideos_PerformsZeroInserts(t *testing.T) {
	vids := []youtube.Video{
		{ID: "v3", PublishedAt: "2026-07-27T13:00:00Z"},
		{ID: "v1", PublishedAt: "2026-07-27T11:00:00Z"},
	}
	h := newHarness(t, map[string][]youtube.Video{"UCa": vids}, nil, nil)
	st := seededState(t, "UCa", "2026-07-27T15:00:00Z") // watermark newer than all

	res, err := syncer.SyncAll(h.youTube(t), st, []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].NewCount != 0 {
		t.Errorf("NewCount=%d, want 0", res[0].NewCount)
	}
	if len(h.inserts) != 0 {
		t.Fatalf("expected no inserts, got %v", h.inserts)
	}
}

func TestSyncAll_DryRun_MutatesNothing(t *testing.T) {
	vids := []youtube.Video{
		{ID: "v5", PublishedAt: "2026-07-27T15:00:00Z"},
		{ID: "v1", PublishedAt: "2026-07-27T13:00:00Z"},
	}
	h := newHarness(t, map[string][]youtube.Video{"UCa": vids}, nil, nil)
	st := seededState(t, "UCa", "2026-07-27T10:00:00Z")

	res, err := syncer.SyncAll(h.youTube(t), st, []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].NewCount != 2 {
		t.Errorf("dry-run should still report would-be-added, NewCount=%d want 2", res[0].NewCount)
	}
	if len(h.inserts) != 0 {
		t.Fatalf("dry-run must not insert, got %v", h.inserts)
	}
	if st.LastSeenAt("UCa") != "2026-07-27T10:00:00Z" {
		t.Errorf("dry-run must not advance cursor, got %q", st.LastSeenAt("UCa"))
	}
}

func TestSyncAll_ReRunIsIdempotent(t *testing.T) {
	vids := []youtube.Video{
		{ID: "v5", PublishedAt: "2026-07-27T15:00:00Z"},
		{ID: "v3", PublishedAt: "2026-07-27T13:00:00Z"},
	}
	h := newHarness(t, map[string][]youtube.Video{"UCa": vids}, nil, nil)
	st := seededState(t, "UCa", "2026-07-27T10:00:00Z")
	mappings := []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}

	if _, err := syncer.SyncAll(h.youTube(t), st, mappings, false); err != nil {
		t.Fatal(err)
	}
	if len(h.inserts) != 2 { // first run added v3, v5
		t.Fatalf("after run1 inserts=%v, want 2", h.inserts)
	}

	res, err := syncer.SyncAll(h.youTube(t), st, mappings, false)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].NewCount != 0 {
		t.Errorf("run2 NewCount=%d, want 0 (idempotent)", res[0].NewCount)
	}
	if len(h.inserts) != 2 { // no new inserts on re-run
		t.Fatalf("run2 added inserts=%v, expected none", h.inserts)
	}
}

func TestSyncAll_ChannelError_RecordsPerChannel_ContinuesOthers(t *testing.T) {
	h := newHarness(t, map[string][]youtube.Video{"UCb": {{ID: "v1", PublishedAt: "2026-07-27T11:00:00Z"}}}, map[string]bool{"UCa": true}, nil)
	st := freshState(t)
	mappings := []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}, {ChannelID: "UCb", PlaylistID: "PLb"}}

	res, err := syncer.SyncAll(h.youTube(t), st, mappings, false)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err == nil {
		t.Error("UCa should have an error (not found)")
	}
	if res[1].Err != nil {
		t.Errorf("UCb should succeed, got err: %v", res[1].Err)
	}
	if !res[1].Seeded {
		t.Error("UCb should be seeded on first contact")
	}
}

// TestSyncAll_SkipsShortsAndStreams_AdvancesCursor verifies only regular
// long-form uploads are added; Shorts and streams are skipped, but the watermark
// still advances past them so they are never re-scanned.
func TestSyncAll_SkipsShortsAndStreams_AdvancesCursor(t *testing.T) {
	vids := []youtube.Video{
		{ID: "v_new", PublishedAt: "2026-07-27T15:00:00Z"},    // regular — should be added
		{ID: "v_short", PublishedAt: "2026-07-27T14:00:00Z"},  // Short — skipped
		{ID: "v_stream", PublishedAt: "2026-07-27T13:00:00Z"}, // live stream — skipped
		{ID: "old", PublishedAt: "2026-07-27T09:00:00Z"},      // below watermark — not new
	}
	kind := map[string]string{"v_short": "short", "v_stream": "stream"}
	h := newHarness(t, map[string][]youtube.Video{"UCa": vids}, nil, kind)
	st := seededState(t, "UCa", "2026-07-27T10:00:00Z")

	res, err := syncer.SyncAll(h.youTube(t), st, []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].NewCount != 1 {
		t.Errorf("NewCount=%d, want 1 (only the regular new video)", res[0].NewCount)
	}
	if len(h.inserts) != 1 || h.inserts[0] != "v_new" {
		t.Fatalf("inserts=%v, want [v_new] only (Short and stream excluded)", h.inserts)
	}
	// Cursor advances to the newest upload overall, so the skipped Short/stream
	// are not re-evaluated on the next run.
	if st.LastSeenAt("UCa") != "2026-07-27T15:00:00Z" {
		t.Errorf("cursor=%q, want newest (15:00)", st.LastSeenAt("UCa"))
	}
}
