package youtube_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
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
	ts              *httptest.Server
	mu              sync.Mutex
	inserts         []string        // inserted videoIds, in order
	failInsert      map[string]bool // videoID → fail its insert; nil/empty (default) = off
	uploadsPageSize int             // >0: serve uploads list paginated at this size (default 0 = single page)
	listGets        int             // GET /playlistItems page fetches so far (pin how much history was listed)
}

// injectInsertFailures arms insert-failure injection: POST /youtube/v3/playlistItems
// returns a non-retryable 403 (reason "forbidden" — not a quota/rate-limit reason,
// so withRetry fails fast without backoff sleeps) for the given video IDs and
// records no insert. Called with no arguments it disables injection again.
// Disabled by default — existing tests are unaffected.
func (h *harness) injectInsertFailures(videoIDs ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failInsert = make(map[string]bool, len(videoIDs))
	for _, id := range videoIDs {
		h.failInsert[id] = true
	}
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
				_, _ = fmt.Fprint(w, `{"items":[]}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"items":[{"id":%q,"contentDetails":{"relatedPlaylists":{"uploads":%q}}}]}`, id, "UU"+id)
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
			_, _ = fmt.Fprintf(w, `{"items":[%s]}`, strings.Join(items, ","))
		case "/youtube/v3/playlistItems":
			if r.Method == http.MethodGet {
				pl := r.URL.Query().Get("playlistId")
				channelID := strings.TrimPrefix(pl, "UU")
				h.mu.Lock()
				h.listGets++
				size := h.uploadsPageSize
				h.mu.Unlock()
				vids := byChannel[channelID]
				if size > 0 { // opt-in pagination (default: one page, as before)
					start := 0
					if tok := r.URL.Query().Get("pageToken"); tok != "" {
						start, _ = strconv.Atoi(tok)
					}
					end := min(start+size, len(vids))
					next := ""
					if end < len(vids) {
						next = strconv.Itoa(end)
					}
					writePlaylistItems(w, vids[start:end], next)
					return
				}
				writePlaylistItems(w, vids, "")
				return
			}
			body, _ := io.ReadAll(r.Body)
			vid := parseInsertVideoID(body)
			h.mu.Lock()
			if h.failInsert[vid] {
				h.mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = fmt.Fprint(w, `{"error":{"code":403,"message":"injected insert failure","errors":[{"domain":"youtube.playlistItem","reason":"forbidden","message":"injected insert failure"}]}}`)
				return
			}
			h.inserts = append(h.inserts, vid)
			h.mu.Unlock()
			_, _ = fmt.Fprint(w, `{"id":"item"}`)
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
	// Point the Shorts probe at the harness server too: /shorts/{id} falls through
	// to http.NotFound (any non-200 is "inconclusive" for the probe), so
	// classification takes the duration fallback deterministically — same outcome
	// as the default wiring (www.youtube.com answers non-200 for fake IDs), but
	// hermetic and without real-network dials.
	return youtube.NewWithShorts(svc, h.ts.URL, h.ts.Client())
}

// writePlaylistItems writes one playlistItems.list page. A non-empty nextToken
// is served as nextPageToken, making the client fetch the next page.
func writePlaylistItems(w io.Writer, videos []youtube.Video, nextToken string) {
	if len(videos) == 0 {
		_, _ = fmt.Fprint(w, `{"items":[]}`)
		return
	}
	parts := make([]string, len(videos))
	for i, v := range videos {
		parts[i] = fmt.Sprintf(`{"contentDetails":{"videoId":%q,"videoPublishedAt":%q}}`, v.ID, v.PublishedAt)
	}
	body := `{"items":[` + strings.Join(parts, ",") + `]`
	if nextToken != "" {
		body += `,"nextPageToken":` + strconv.Quote(nextToken)
	}
	_, _ = fmt.Fprint(w, body+"}")
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

// sortedEqual reports whether the two ID lists contain the same elements
// regardless of order.
func sortedEqual(got, want []string) bool {
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	slices.Sort(g)
	slices.Sort(w)
	return slices.Equal(g, w)
}

// countID counts how many times a video ID appears in an insert log.
func countID(ids []string, id string) int {
	n := 0
	for _, x := range ids {
		if x == id {
			n++
		}
	}
	return n
}

// TestSyncAll_PartialInsertFailure_CursorAtLastProcessed_ErrKept covers FR1:
// when an insert fails mid-batch, insertion stops, the insert error is kept
// (not overwritten), AddedIDs reports only what was actually inserted, and the
// watermark parks at the newest successfully PROCESSED video — not at the
// newest upload, which would silently drop the failed/uninserted videos.
func TestSyncAll_PartialInsertFailure_CursorAtLastProcessed_ErrKept(t *testing.T) {
	vids := []youtube.Video{
		{ID: "v_new", PublishedAt: "2026-07-27T15:00:00Z"},
		{ID: "v_mid", PublishedAt: "2026-07-27T13:00:00Z"},
		{ID: "v_old", PublishedAt: "2026-07-27T11:00:00Z"},
	}
	h := newHarness(t, map[string][]youtube.Video{"UCa": vids}, nil, nil)
	h.injectInsertFailures("v_mid")
	st := seededState(t, "UCa", "2026-07-27T10:00:00Z") // watermark 10:00
	mappings := []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}

	// Run 1: v_old (11:00) inserts, v_mid (13:00) fails, v_new never attempted.
	res1, err := syncer.SyncAll(h.youTube(t), st, mappings, false)
	if err != nil {
		t.Fatal(err)
	}
	r1 := res1[0]
	if r1.Err == nil || !strings.Contains(r1.Err.Error(), "v_mid") {
		t.Errorf("run1 Err=%v, want error mentioning v_mid", r1.Err)
	}
	if r1.NewCount != 3 {
		t.Errorf("run1 NewCount=%d, want 3 (all were new)", r1.NewCount)
	}
	if len(h.inserts) != 1 || h.inserts[0] != "v_old" {
		t.Errorf("run1 inserts=%v, want [v_old] only (stop at failed v_mid)", h.inserts)
	}
	// AddedIDs must reflect what was actually inserted, not the would-be list.
	if len(r1.AddedIDs) != 1 || r1.AddedIDs[0] != "v_old" {
		t.Errorf("run1 AddedIDs=%v, want [v_old]", r1.AddedIDs)
	}
	// Watermark parks at the last successfully processed video (v_old, 11:00) so
	// v_mid and v_new are still new on the next run. The old behavior parked at
	// the newest upload (15:00) and lost them.
	if got := st.LastSeenAt("UCa"); got != "2026-07-27T11:00:00Z" {
		t.Errorf("run1 cursor=%q, want last processed 11:00", got)
	}

	// Run 2 without the failure: v_mid and v_new are retried, nothing duplicated.
	h.injectInsertFailures()
	res2, err := syncer.SyncAll(h.youTube(t), st, mappings, false)
	if err != nil {
		t.Fatal(err)
	}
	if res2[0].Err != nil {
		t.Errorf("run2 Err=%v, want nil", res2[0].Err)
	}
	if res2[0].NewCount != 2 {
		t.Errorf("run2 NewCount=%d, want 2 (v_mid, v_new)", res2[0].NewCount)
	}
	want := []string{"v_old", "v_mid", "v_new"}
	if !slices.Equal(h.inserts, want) {
		t.Errorf("inserts after run2=%v, want %v (each exactly once, oldest→newest)", h.inserts, want)
	}
	if got := st.LastSeenAt("UCa"); got != "2026-07-27T15:00:00Z" {
		t.Errorf("run2 cursor=%q, want newest (15:00)", got)
	}
	// Membership only — the plan does not pin run-2 AddedIDs order.
	if !sortedEqual(res2[0].AddedIDs, []string{"v_mid", "v_new"}) {
		t.Errorf("run2 AddedIDs=%v, want exactly {v_mid, v_new}", res2[0].AddedIDs)
	}
}

// TestSyncAll_PartialInsertFailure_NonMonotonic_WatermarkClampedBelowFailed pins
// the clamped partial-failure watermark: with a non-monotonic upload list the
// watermark must park at the newest processed timestamp strictly OLDER than the
// failed video's — never at or over it, even when another processed video
// carries a newer timestamp via list-order inversion. v_b (13:00) fails;
// processed = {v_c (12:00), v_d (14:00, inverted: sits deeper in the list)};
// watermark = 12:00. Parking at the raw max (14:00) or at the list-newest
// (v_b's own 13:00) would make the retry's selectNew skip v_b forever — a
// silently lost episode; the retry run below proves v_b lands exactly once.
// v_d re-inserting on the retry is the documented acceptable price: a visible
// duplicate beats a lost episode.
func TestSyncAll_PartialInsertFailure_NonMonotonic_WatermarkClampedBelowFailed(t *testing.T) {
	// List order (as served, newest-first position) is NOT time-monotonic:
	// the newest upload v_d (14:00) sits last AND is newer than the failing
	// v_b (13:00) that precedes it positionally.
	vids := []youtube.Video{
		{ID: "v_b", PublishedAt: "2026-07-27T13:00:00Z"},
		{ID: "v_c", PublishedAt: "2026-07-27T12:00:00Z"},
		{ID: "v_d", PublishedAt: "2026-07-27T14:00:00Z"},
	}
	h := newHarness(t, map[string][]youtube.Video{"UCa": vids}, nil, nil)
	h.injectInsertFailures("v_b")
	st := seededState(t, "UCa", "2026-07-27T10:00:00Z")
	mappings := []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}

	// Run 1: the oldest→newest walk inserts v_d, then v_c, then fails on v_b.
	res, err := syncer.SyncAll(h.youTube(t), st, mappings, false)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "v_b") {
		t.Errorf("run1 Err=%v, want error mentioning v_b", res[0].Err)
	}
	if !slices.Equal(h.inserts, []string{"v_d", "v_c"}) {
		t.Errorf("run1 inserts=%v, want [v_d v_c] (walk order; v_b failed)", h.inserts)
	}
	// Clamp: only v_c (12:00) is processed AND strictly older than the failed
	// v_b (13:00); the inverted v_d (14:00) must not lift the watermark over
	// the failure point.
	if got := st.LastSeenAt("UCa"); got != "2026-07-27T12:00:00Z" {
		t.Errorf("run1 cursor=%q, want clamped processed 12:00 (below failed v_b's 13:00, despite inverted v_d 14:00)", got)
	}

	// Run 2 without the failure: selectNew(>12:00) re-selects v_b and the
	// inverted v_d (v_c is done). v_b must land exactly once — the
	// no-silent-loss guarantee the clamp exists for; v_d lands a second time,
	// the accepted duplicate.
	h.injectInsertFailures()
	res2, err := syncer.SyncAll(h.youTube(t), st, mappings, false)
	if err != nil {
		t.Fatal(err)
	}
	if res2[0].Err != nil {
		t.Errorf("run2 Err=%v, want nil", res2[0].Err)
	}
	if res2[0].NewCount != 2 {
		t.Errorf("run2 NewCount=%d, want 2 (v_b, v_d)", res2[0].NewCount)
	}
	if n := countID(h.inserts, "v_b"); n != 1 {
		t.Errorf("v_b inserted %d times across runs, want exactly 1 (a miss = silently lost episode)", n)
	}
	if n := countID(h.inserts, "v_c"); n != 1 {
		t.Errorf("v_c inserted %d times across runs, want exactly 1", n)
	}
	// The inverted v_d (14:00 > failed 13:00) stays above the clamped
	// watermark and is inserted again — a visible duplicate, accepted price.
	if n := countID(h.inserts, "v_d"); n != 2 {
		t.Errorf("v_d inserted %d times across runs, want 2 (once per run — accepted duplicate)", n)
	}
	if !slices.Equal(res2[0].AddedIDs, []string{"v_d", "v_b"}) {
		t.Errorf("run2 AddedIDs=%v, want [v_d v_b] (walk order; v_d re-selected above the clamp)", res2[0].AddedIDs)
	}
	// Full success advances positionally to the list-newest (v_b, 13:00), not
	// the max timestamp — same positional semantics documented in
	// TestSyncAll_FirstInsertFails_ExcludedNewerShort_CursorUntouched.
	if got := st.LastSeenAt("UCa"); got != "2026-07-27T13:00:00Z" {
		t.Errorf("run2 cursor=%q, want list-newest 13:00", got)
	}
}

// TestSyncAll_PartialInsertFailure_ExcludedShortBeforeFailure_NotProcessed pins
// the excluded-upload rule on insert failure: an excluded Short/stream counts as
// processed ONLY if the oldest→newest walk REACHED it before the break — i.e. it
// sits AFTER the failed position in the newest-first list. Here the Short s_new
// (15:00) sits BEFORE the failing v_mid, so the walk breaks without ever
// reaching it: the watermark must park at the last inserted v_old (11:00).
// Counting s_new unconditionally parks the cursor at 15:00, the retry run finds
// nothing new, and v_mid is silently lost forever.
func TestSyncAll_PartialInsertFailure_ExcludedShortBeforeFailure_NotProcessed(t *testing.T) {
	vids := []youtube.Video{
		{ID: "s_new", PublishedAt: "2026-07-27T15:00:00Z"}, // Short, newest — walked AFTER the break point
		{ID: "v_mid", PublishedAt: "2026-07-27T13:00:00Z"}, // regular — insert fails here
		{ID: "v_old", PublishedAt: "2026-07-27T11:00:00Z"}, // regular — inserted first
	}
	kind := map[string]string{"s_new": "short"}
	h := newHarness(t, map[string][]youtube.Video{"UCa": vids}, nil, kind)
	h.injectInsertFailures("v_mid")
	st := seededState(t, "UCa", "2026-07-27T10:00:00Z")
	mappings := []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}

	res1, err := syncer.SyncAll(h.youTube(t), st, mappings, false)
	if err != nil {
		t.Fatal(err)
	}
	r1 := res1[0]
	if r1.Err == nil || !strings.Contains(r1.Err.Error(), "v_mid") {
		t.Errorf("run1 Err=%v, want error mentioning v_mid", r1.Err)
	}
	if r1.NewCount != 2 {
		t.Errorf("run1 NewCount=%d, want 2 (v_mid, v_old; Short excluded)", r1.NewCount)
	}
	if len(h.inserts) != 1 || h.inserts[0] != "v_old" {
		t.Errorf("run1 inserts=%v, want [v_old]", h.inserts)
	}
	if len(r1.AddedIDs) != 1 || r1.AddedIDs[0] != "v_old" {
		t.Errorf("run1 AddedIDs=%v, want [v_old]", r1.AddedIDs)
	}
	// s_new was never reached → NOT processed → cursor parks at v_old (11:00),
	// NOT at 15:00.
	if got := st.LastSeenAt("UCa"); got != "2026-07-27T11:00:00Z" {
		t.Errorf("run1 cursor=%q, want last inserted 11:00 (excluded Short before the failure must not advance it)", got)
	}

	// Run 2 without the failure: v_mid is retried and lands once; s_new is
	// excluded again (never inserted); v_old is not duplicated.
	h.injectInsertFailures()
	res2, err := syncer.SyncAll(h.youTube(t), st, mappings, false)
	if err != nil {
		t.Fatal(err)
	}
	if res2[0].Err != nil {
		t.Errorf("run2 Err=%v, want nil", res2[0].Err)
	}
	if res2[0].NewCount != 1 {
		t.Errorf("run2 NewCount=%d, want 1 (only v_mid)", res2[0].NewCount)
	}
	if !slices.Equal(h.inserts, []string{"v_old", "v_mid"}) {
		t.Errorf("inserts after run2=%v, want [v_old v_mid] (v_mid recovered exactly once)", h.inserts)
	}
	// Full success (the remaining Short is classified out) → cursor advances to
	// the newest upload overall.
	if got := st.LastSeenAt("UCa"); got != "2026-07-27T15:00:00Z" {
		t.Errorf("run2 cursor=%q, want newest (15:00)", got)
	}
}

// TestSyncAll_PartialInsertFailure_ExcludedShortAfterFailure_CountsProcessed is
// the mirror guard: an excluded Short sitting AFTER the failed position (walked
// BEFORE the break) IS processed, so the watermark parks at its timestamp —
// between the last insert (11:00) and the failure (13:00). Guards the fix
// against under-shooting: dropping excluded uploads from "processed" entirely
// would park at 11:00 and re-visit the Short on every retry.
func TestSyncAll_PartialInsertFailure_ExcludedShortAfterFailure_CountsProcessed(t *testing.T) {
	vids := []youtube.Video{
		{ID: "v_new", PublishedAt: "2026-07-27T15:00:00Z"}, // regular, newest — walked AFTER the break point
		{ID: "v_mid", PublishedAt: "2026-07-27T13:00:00Z"}, // regular — insert fails here
		{ID: "s_old", PublishedAt: "2026-07-27T12:00:00Z"}, // Short — walked BEFORE the break point: processed
		{ID: "v_old", PublishedAt: "2026-07-27T11:00:00Z"}, // regular — inserted first
	}
	kind := map[string]string{"s_old": "short"}
	h := newHarness(t, map[string][]youtube.Video{"UCa": vids}, nil, kind)
	h.injectInsertFailures("v_mid")
	st := seededState(t, "UCa", "2026-07-27T10:00:00Z")
	mappings := []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}

	res1, err := syncer.SyncAll(h.youTube(t), st, mappings, false)
	if err != nil {
		t.Fatal(err)
	}
	r1 := res1[0]
	if r1.Err == nil || !strings.Contains(r1.Err.Error(), "v_mid") {
		t.Errorf("run1 Err=%v, want error mentioning v_mid", r1.Err)
	}
	if len(h.inserts) != 1 || h.inserts[0] != "v_old" {
		t.Errorf("run1 inserts=%v, want [v_old]", h.inserts)
	}
	// Processed = {v_old 11:00, s_old 12:00} → cursor parks at 12:00; v_new was
	// never reached and must stay new.
	if got := st.LastSeenAt("UCa"); got != "2026-07-27T12:00:00Z" {
		t.Errorf("run1 cursor=%q, want walked-past Short's 12:00", got)
	}

	// Run 2 without the failure: v_mid and v_new land, each exactly once.
	h.injectInsertFailures()
	res2, err := syncer.SyncAll(h.youTube(t), st, mappings, false)
	if err != nil {
		t.Fatal(err)
	}
	if res2[0].Err != nil {
		t.Errorf("run2 Err=%v, want nil", res2[0].Err)
	}
	if res2[0].NewCount != 2 {
		t.Errorf("run2 NewCount=%d, want 2 (v_mid, v_new)", res2[0].NewCount)
	}
	if !slices.Equal(h.inserts, []string{"v_old", "v_mid", "v_new"}) {
		t.Errorf("inserts after run2=%v, want [v_old v_mid v_new] (each exactly once)", h.inserts)
	}
	if got := st.LastSeenAt("UCa"); got != "2026-07-27T15:00:00Z" {
		t.Errorf("run2 cursor=%q, want newest (15:00)", got)
	}
}

// TestSyncAll_FirstInsertFails_ExcludedNewerShort_CursorUntouched pins the
// zero-success edge of the partial-failure watermark: with NO insert landing,
// excluded uploads must not count as processed even when the walk passed them —
// otherwise a non-monotonic Short newer than an unattempted regular parks the
// cursor past it and the regular is silently lost. s_d (14:00) sits at a deeper
// list position than the failing v_b (11:00) but carries a newer timestamp; the
// pre-fix augmentation parked at 14:00, and the retry's selectNew(>14:00) never
// saw v_a (13:00) again.
func TestSyncAll_FirstInsertFails_ExcludedNewerShort_CursorUntouched(t *testing.T) {
	vids := []youtube.Video{
		{ID: "v_a", PublishedAt: "2026-07-27T13:00:00Z"}, // regular — never attempted
		{ID: "v_b", PublishedAt: "2026-07-27T11:00:00Z"}, // regular — first (oldest) walk step, fails
		{ID: "s_d", PublishedAt: "2026-07-27T14:00:00Z"}, // Short — newer than v_a, deeper position
	}
	kind := map[string]string{"s_d": "short"}
	h := newHarness(t, map[string][]youtube.Video{"UCa": vids}, nil, kind)
	h.injectInsertFailures("v_b")
	st := seededState(t, "UCa", "2026-07-27T10:00:00Z")
	mappings := []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}

	res1, err := syncer.SyncAll(h.youTube(t), st, mappings, false)
	if err != nil {
		t.Fatal(err)
	}
	r1 := res1[0]
	if r1.Err == nil || !strings.Contains(r1.Err.Error(), "v_b") {
		t.Errorf("run1 Err=%v, want error mentioning v_b", r1.Err)
	}
	if len(h.inserts) != 0 {
		t.Errorf("run1 inserts=%v, want none", h.inserts)
	}
	if len(r1.AddedIDs) != 0 {
		t.Errorf("run1 AddedIDs=%v, want empty (nothing inserted)", r1.AddedIDs)
	}
	// Zero successful inserts → nothing processed → cursor must not move at all
	// (the excluded Short's newer timestamp must not park it at 14:00).
	if got := st.LastSeenAt("UCa"); got != "2026-07-27T10:00:00Z" {
		t.Errorf("run1 cursor=%q, want untouched watermark 10:00", got)
	}

	// Run 2 without the failure: v_a and v_b land, each exactly once; the cursor
	// advances to the newest upload overall (the Short's 14:00).
	h.injectInsertFailures()
	res2, err := syncer.SyncAll(h.youTube(t), st, mappings, false)
	if err != nil {
		t.Fatal(err)
	}
	if res2[0].Err != nil {
		t.Errorf("run2 Err=%v, want nil", res2[0].Err)
	}
	if res2[0].NewCount != 2 {
		t.Errorf("run2 NewCount=%d, want 2 (v_a, v_b)", res2[0].NewCount)
	}
	if !slices.Equal(h.inserts, []string{"v_b", "v_a"}) {
		t.Errorf("inserts after run2=%v, want [v_b v_a] (each exactly once, oldest→newest)", h.inserts)
	}
	// Full-success advance is positional (videos[0] = v_a, 13:00), not
	// max-timestamp: the deeper newer Short stays excluded and re-selected —
	// by design, harmless (it never inserts).
	if got := st.LastSeenAt("UCa"); got != "2026-07-27T13:00:00Z" {
		t.Errorf("run2 cursor=%q, want list-newest 13:00", got)
	}
}

// TestSyncAll_FirstInsertFails_CursorUntouched_RetriedWhole covers FR1.5: when
// the very first insert fails, nothing was processed, so the watermark stays
// untouched and the whole batch is retried on the next run.
func TestSyncAll_FirstInsertFails_CursorUntouched_RetriedWhole(t *testing.T) {
	vids := []youtube.Video{
		{ID: "v_new", PublishedAt: "2026-07-27T15:00:00Z"},
		{ID: "v_mid", PublishedAt: "2026-07-27T13:00:00Z"},
		{ID: "v_old", PublishedAt: "2026-07-27T11:00:00Z"},
	}
	h := newHarness(t, map[string][]youtube.Video{"UCa": vids}, nil, nil)
	h.injectInsertFailures("v_old") // oldest → first insert attempt fails
	st := seededState(t, "UCa", "2026-07-27T10:00:00Z")
	mappings := []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}

	res1, err := syncer.SyncAll(h.youTube(t), st, mappings, false)
	if err != nil {
		t.Fatal(err)
	}
	if res1[0].Err == nil || !strings.Contains(res1[0].Err.Error(), "v_old") {
		t.Errorf("run1 Err=%v, want error mentioning v_old", res1[0].Err)
	}
	if len(h.inserts) != 0 {
		t.Errorf("run1 inserts=%v, want none", h.inserts)
	}
	if len(res1[0].AddedIDs) != 0 {
		t.Errorf("run1 AddedIDs=%v, want empty (nothing inserted)", res1[0].AddedIDs)
	}
	// Zero processed → cursor must not move at all.
	if got := st.LastSeenAt("UCa"); got != "2026-07-27T10:00:00Z" {
		t.Errorf("run1 cursor=%q, want untouched watermark 10:00", got)
	}

	// Run 2 without the failure: all three inserted, each exactly once.
	h.injectInsertFailures()
	res2, err := syncer.SyncAll(h.youTube(t), st, mappings, false)
	if err != nil {
		t.Fatal(err)
	}
	if res2[0].Err != nil {
		t.Errorf("run2 Err=%v, want nil", res2[0].Err)
	}
	if res2[0].NewCount != 3 {
		t.Errorf("run2 NewCount=%d, want 3 (whole batch retried)", res2[0].NewCount)
	}
	want := []string{"v_old", "v_mid", "v_new"}
	if !slices.Equal(h.inserts, want) {
		t.Errorf("inserts after run2=%v, want %v (each exactly once)", h.inserts, want)
	}
	if got := st.LastSeenAt("UCa"); got != "2026-07-27T15:00:00Z" {
		t.Errorf("run2 cursor=%q, want newest (15:00)", got)
	}
}

// TestSyncAll_AllShortsBatch_AdvancesCursor_NoError covers FR1.6: a batch of
// nothing but new Shorts/streams inserts nothing and reports no error, yet the
// watermark still advances to the newest upload — the failure-aware cursor
// logic must not regress the "everything excluded" path.
func TestSyncAll_AllShortsBatch_AdvancesCursor_NoError(t *testing.T) {
	vids := []youtube.Video{
		{ID: "s_new", PublishedAt: "2026-07-27T15:00:00Z"},
		{ID: "s_old", PublishedAt: "2026-07-27T12:00:00Z"},
	}
	kind := map[string]string{"s_new": "short", "s_old": "short"}
	h := newHarness(t, map[string][]youtube.Video{"UCa": vids}, nil, kind)
	st := seededState(t, "UCa", "2026-07-27T10:00:00Z")

	res, err := syncer.SyncAll(h.youTube(t), st, []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err != nil {
		t.Errorf("Err=%v, want nil", res[0].Err)
	}
	if res[0].NewCount != 0 {
		t.Errorf("NewCount=%d, want 0 (only Shorts)", res[0].NewCount)
	}
	if len(h.inserts) != 0 {
		t.Errorf("inserts=%v, want none", h.inserts)
	}
	if len(res[0].AddedIDs) != 0 {
		t.Errorf("AddedIDs=%v, want empty", res[0].AddedIDs)
	}
	if got := st.LastSeenAt("UCa"); got != "2026-07-27T15:00:00Z" {
		t.Errorf("cursor=%q, want newest (15:00)", got)
	}
}

// TestSyncAll_PassesCursor_ListingStopsEarly pins FR2 end-to-end: SyncAll feeds
// the channel's stored cursor into the uploads listing, so the listing stops
// paginating once a whole page is older than it and page three of the history is
// never fetched. A listing that ignores the watermark walks all three pages
// (the quota burn under fix); a SyncAll that passes no cursor stops after page
// one and misses page two's items — both fail this test.
func TestSyncAll_PassesCursor_ListingStopsEarly(t *testing.T) {
	vids := []youtube.Video{
		{ID: "v3", PublishedAt: "2026-07-27T11:00:00Z"},  // new — page 1
		{ID: "v2", PublishedAt: "2026-07-27T10:30:00Z"},  // new — page 1 (mixed ⇒ keep paging)
		{ID: "v1", PublishedAt: "2026-07-27T09:00:00Z"},  // old — page 2
		{ID: "v0", PublishedAt: "2026-07-27T08:30:00Z"},  // old — page 2 (all old ⇒ stop)
		{ID: "v00", PublishedAt: "2026-07-27T08:00:00Z"}, // old — page 3 (must stay unfetched)
	}
	h := newHarness(t, map[string][]youtube.Video{"UCa": vids}, nil, nil)
	h.uploadsPageSize = 2 // → pages: [v3 v2], [v1 v0], [v00]
	st := seededState(t, "UCa", "2026-07-27T10:00:00Z")

	res, err := syncer.SyncAll(h.youTube(t), st, []config.ChannelMapping{{ChannelID: "UCa", PlaylistID: "PLa"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if h.listGets != 2 {
		t.Errorf("playlistItems page fetches=%d, want 2 (stop once page 2 is all-old; full history=3, no-cursor=1)", h.listGets)
	}
	if res[0].NewCount != 2 {
		t.Errorf("NewCount=%d, want 2 (v2, v3)", res[0].NewCount)
	}
	if !slices.Equal(h.inserts, []string{"v2", "v3"}) {
		t.Errorf("inserts=%v, want [v2 v3]", h.inserts)
	}
	if got := st.LastSeenAt("UCa"); got != "2026-07-27T11:00:00Z" {
		t.Errorf("cursor=%q, want newest (11:00)", got)
	}
}
