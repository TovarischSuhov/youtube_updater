package youtube

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	youtubev3 "google.golang.org/api/youtube/v3"
)

// TestContractSurface is a compile-time check of the public API shape.
func TestContractSurface(t *testing.T) {
	var v Video
	_ = v.ID
	_ = v.PublishedAt
	var _ func(string, string, string) (*YouTube, error) = NewYouTube
	var y YouTube
	var _ func(string) (string, error) = y.ResolveUploads
	var _ func(string, string) ([]Video, error) = y.ListUploads // uploadsID + watermark
	var _ func([]Video) ([]Video, error) = y.FilterRegularVideos
	var _ func(string, string) (string, error) = y.AddToPlaylist
	var _ func(string, string) (string, string, error) = y.ResolveNames
	var _ func(ChannelRef) (string, error) = y.ResolveChannelRef
}

func newTestYouTube(t *testing.T, h http.HandlerFunc) *YouTube {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	svc, err := youtubev3.NewService(context.Background(), option.WithEndpoint(ts.URL), option.WithHTTPClient(ts.Client()))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return newWithService(svc)
}

// newTestYouTubeWithShorts builds a facade whose Data API service and Shorts probe
// both point at httptest servers, so the HEAD classification path is exercised
// without real network calls. The probe client does not follow redirects (mirrors
// defaultShortsClient). dataHandler serves /youtube/v3/*; shortsHandler serves
// /shorts/{id}.
func newTestYouTubeWithShorts(t *testing.T, dataHandler, shortsHandler http.HandlerFunc) *YouTube {
	t.Helper()
	dataTS := httptest.NewServer(dataHandler)
	t.Cleanup(dataTS.Close)
	shortsTS := httptest.NewServer(shortsHandler)
	t.Cleanup(shortsTS.Close)
	svc, err := youtubev3.NewService(context.Background(), option.WithEndpoint(dataTS.URL), option.WithHTTPClient(dataTS.Client()))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	shortsClient := shortsTS.Client()
	shortsClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return newWithShorts(svc, shortsTS.URL, shortsClient)
}

// videoIDs returns every id from a /videos list request. The google-api client
// sends multiple ids as repeated id= query params, so Query().Get would only see
// the first; this also tolerates a comma-joined single value.
func videoIDs(r *http.Request) []string {
	var ids []string
	for _, v := range r.URL.Query()["id"] {
		ids = append(ids, strings.Split(v, ",")...)
	}
	return ids
}

// writeVideosList writes a /youtube/v3/videos response. durFor maps id→ISO-8601
// duration; an id absent from durFor is omitted from the response (simulating a
// deleted/private video); stream marks ids carrying liveStreamingDetails.
func writeVideosList(w http.ResponseWriter, r *http.Request, durFor map[string]string, stream map[string]bool) {
	if r.URL.Path != "/youtube/v3/videos" {
		http.NotFound(w, r)
		return
	}
	var items []string
	for _, id := range videoIDs(r) {
		dur, ok := durFor[id]
		if !ok {
			continue // absent from response ⇒ dropped by the classifier
		}
		if stream[id] {
			items = append(items, fmt.Sprintf(`{"id":%q,"contentDetails":{"duration":%q},"liveStreamingDetails":{"actualStartTime":"2026-01-01T00:00:00Z"}}`, id, dur))
			continue
		}
		items = append(items, fmt.Sprintf(`{"id":%q,"contentDetails":{"duration":%q}}`, id, dur))
	}
	_, _ = fmt.Fprintf(w, `{"items":[%s]}`, strings.Join(items, ","))
}

func TestResolveUploads_ReturnsUploadsPlaylistID(t *testing.T) {
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"items":[{"contentDetails":{"relatedPlaylists":{"uploads":"UUabc"}}}]}`)
	})
	got, err := yt.ResolveUploads("UCabc")
	if err != nil {
		t.Fatalf("ResolveUploads error: %v", err)
	}
	if got != "UUabc" {
		t.Errorf("got %q, want UUabc", got)
	}
}

func TestResolveUploads_ChannelNotFound_ReturnsError(t *testing.T) {
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"items":[]}`)
	})
	if _, err := yt.ResolveUploads("UCx"); err == nil {
		t.Fatal("expected error for channel not found")
	}
}

func TestListUploads_ParsesPaginatedNewestFirst(t *testing.T) {
	page := 0
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		if page == 0 {
			page++
			_, _ = fmt.Fprint(w, `{"items":[{"contentDetails":{"videoId":"v1","videoPublishedAt":"2026-07-27T10:00:00Z"}},{"contentDetails":{"videoId":"v2","videoPublishedAt":"2026-07-27T09:00:00Z"}}],"nextPageToken":"TOKEN2"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"items":[{"contentDetails":{"videoId":"v3","videoPublishedAt":"2026-07-27T08:00:00Z"}}]}`)
	})
	// The watermark predates every item, so no page is all-old and the listing
	// walks the whole (two-page) history — the parse/ordering behavior under test.
	got, err := yt.ListUploads("UUx", "2026-07-27T00:00:00Z")
	if err != nil {
		t.Fatalf("ListUploads error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 videos, got %d", len(got))
	}
	if got[0].ID != "v1" || got[2].ID != "v3" {
		t.Errorf("newest-first order wrong: %+v", got)
	}
	if got[0].PublishedAt != "2026-07-27T10:00:00Z" {
		t.Errorf("PublishedAt not preserved: %+v", got[0])
	}
}

func TestListUploads_EmptyPlaylist_ReturnsEmptyNoError(t *testing.T) {
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"items":[]}`)
	})
	got, err := yt.ListUploads("UUx", "")
	if err != nil {
		t.Fatalf("ListUploads error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
}

// pagedUploads builds a facade whose playlistItems.list serves pages[i] on the
// i-th request (all but the last page carry a nextPageToken) and counts page
// requests, so a test can pin exactly how much of the history a listing touched.
func pagedUploads(t *testing.T, pages ...[]Video) (*YouTube, *int) {
	t.Helper()
	hits := 0
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/youtube/v3/playlistItems" {
			http.NotFound(w, r)
			return
		}
		i := hits
		hits++
		if i >= len(pages) {
			i = len(pages) - 1 // defensive: the assertions below pin the exact count
		}
		var parts []string
		for _, v := range pages[i] {
			parts = append(parts, fmt.Sprintf(`{"contentDetails":{"videoId":%q,"videoPublishedAt":%q}}`, v.ID, v.PublishedAt))
		}
		body := `{"items":[` + strings.Join(parts, ",") + `]`
		if i < len(pages)-1 {
			body += `,"nextPageToken":"next"`
		}
		_, _ = fmt.Fprint(w, body+"}")
	})
	return yt, &hits
}

// TestListUploads_AllOldPage_StopsPagination pins the FR2 early exit: when EVERY
// item of the current page is older than the watermark, the listing must not
// request the next page (which would burn quota on years of history) — and must
// return the fetched page's items without an error (the stop is not a failure).
func TestListUploads_AllOldPage_StopsPagination(t *testing.T) {
	yt, hits := pagedUploads(t,
		[]Video{
			{ID: "old1", PublishedAt: "2026-07-27T09:00:00Z"},
			{ID: "old2", PublishedAt: "2026-07-27T08:00:00Z"},
		},
		[]Video{{ID: "never", PublishedAt: "2026-07-27T07:00:00Z"}}, // must stay unfetched
	)
	got, err := yt.ListUploads("UUx", "2026-07-27T10:00:00Z")
	if err != nil {
		t.Fatalf("ListUploads error: %v (an early stop must not surface as an error)", err)
	}
	if *hits != 1 {
		t.Errorf("page fetches=%d, want 1 (all page-1 items older than the watermark ⇒ stop)", *hits)
	}
	if len(got) != 2 || got[0].ID != "old1" || got[1].ID != "old2" {
		t.Errorf("got %+v, want the two page-1 items", got)
	}
}

// TestListUploads_MixedPage_ContinuesPagination pins the safeguard against
// premieres/late publishings whose date may sit later than their list position:
// a page holding ANY item newer than the watermark must keep the pagination
// going into the next page, not stop on the first old element.
func TestListUploads_MixedPage_ContinuesPagination(t *testing.T) {
	yt, hits := pagedUploads(t,
		[]Video{
			{ID: "new", PublishedAt: "2026-07-27T11:00:00Z"},
			{ID: "old", PublishedAt: "2026-07-27T09:00:00Z"},
		},
		[]Video{{ID: "paged", PublishedAt: "2026-07-27T08:00:00Z"}},
	)
	got, err := yt.ListUploads("UUx", "2026-07-27T10:00:00Z")
	if err != nil {
		t.Fatalf("ListUploads error: %v", err)
	}
	if *hits != 2 {
		t.Errorf("page fetches=%d, want 2 (a mixed page must not stop the listing)", *hits)
	}
	if len(got) != 3 || got[2].ID != "paged" {
		t.Errorf("got %+v, want all three items including page 2's", got)
	}
}

// TestListUploads_EmptyWatermark_ListsOnePage pins the unseeded-channel rule: a
// channel with no cursor yet only needs the newest upload for seeding, so an
// empty watermark lists exactly one page regardless of history depth.
func TestListUploads_EmptyWatermark_ListsOnePage(t *testing.T) {
	yt, hits := pagedUploads(t,
		[]Video{
			{ID: "newest", PublishedAt: "2026-07-27T11:00:00Z"},
			{ID: "second", PublishedAt: "2026-07-27T10:00:00Z"},
		},
		[]Video{{ID: "p2", PublishedAt: "2026-07-27T09:00:00Z"}},
		[]Video{{ID: "p3", PublishedAt: "2026-07-27T08:00:00Z"}},
	)
	got, err := yt.ListUploads("UUx", "")
	if err != nil {
		t.Fatalf("ListUploads error: %v", err)
	}
	if *hits != 1 {
		t.Errorf("page fetches=%d, want 1 (an unseeded channel needs only the newest)", *hits)
	}
	if len(got) != 2 || got[0].ID != "newest" {
		t.Errorf("got %+v, want the first page only", got)
	}
}

func TestAddToPlaylist_ReturnsItemID(t *testing.T) {
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "expected POST", http.StatusMethodNotAllowed)
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"PLI1"}`)
	})
	got, err := yt.AddToPlaylist("PLx", "v1")
	if err != nil {
		t.Fatalf("AddToPlaylist error: %v", err)
	}
	if got != "PLI1" {
		t.Errorf("got %q, want PLI1", got)
	}
}

func TestResolveNames_ReturnsBothNames(t *testing.T) {
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/youtube/v3/channels":
			_, _ = fmt.Fprint(w, `{"items":[{"snippet":{"title":"Foo Channel"}}]}`)
		case "/youtube/v3/playlists":
			_, _ = fmt.Fprint(w, `{"items":[{"snippet":{"title":"My Playlist"}}]}`)
		}
	})
	chName, plName, err := yt.ResolveNames("UCa", "PLa")
	if err != nil {
		t.Fatalf("ResolveNames error: %v", err)
	}
	if chName != "Foo Channel" {
		t.Errorf("channel name %q, want Foo Channel", chName)
	}
	if plName != "My Playlist" {
		t.Errorf("playlist name %q, want My Playlist", plName)
	}
}

func TestResolveNames_ChannelNotFound(t *testing.T) {
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/youtube/v3/channels":
			_, _ = fmt.Fprint(w, `{"items":[]}`)
		case "/youtube/v3/playlists":
			_, _ = fmt.Fprint(w, `{"items":[{"snippet":{"title":"P"}}]}`)
		}
	})
	if _, _, err := yt.ResolveNames("UCx", "PLa"); err == nil {
		t.Fatal("expected channel-not-found error, got nil")
	}
}

func TestResolveNames_PlaylistNotFound(t *testing.T) {
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/youtube/v3/channels":
			_, _ = fmt.Fprint(w, `{"items":[{"snippet":{"title":"C"}}]}`)
		case "/youtube/v3/playlists":
			_, _ = fmt.Fprint(w, `{"items":[]}`)
		}
	})
	if _, _, err := yt.ResolveNames("UCa", "PLx"); err == nil {
		t.Fatal("expected playlist-not-found error, got nil")
	}
}

// TestLoadToken_ExpiredAccessTokenWithRefreshToken_LoadsOK covers the non-
// interactive case (e.g. scheduled CI): the cached access token has expired but
// a refresh token is present, so loadToken must return the token rather than
// fall back to the interactive consent flow.
func TestLoadToken_ExpiredAccessTokenWithRefreshToken_LoadsOK(t *testing.T) {
	expired := &oauth2.Token{
		AccessToken:  "expired-access",
		RefreshToken: "refresh-xyz",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Hour),
	}
	path := filepath.Join(t.TempDir(), "token.json")
	if err := saveToken(path, expired); err != nil {
		t.Fatalf("saveToken: %v", err)
	}
	got, err := loadToken(path)
	if err != nil {
		t.Fatalf("loadToken error: %v", err)
	}
	if got.RefreshToken != "refresh-xyz" {
		t.Errorf("refresh token = %q, want refresh-xyz", got.RefreshToken)
	}
}

// TestLoadToken_ExpiredAccessTokenNoRefreshToken_ReturnsError confirms a stored
// token with no refresh path is rejected — there is nothing to recover it from.
func TestLoadToken_ExpiredAccessTokenNoRefreshToken_ReturnsError(t *testing.T) {
	expired := &oauth2.Token{
		AccessToken: "expired-access",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(-time.Hour),
	}
	path := filepath.Join(t.TempDir(), "token.json")
	if err := saveToken(path, expired); err != nil {
		t.Fatalf("saveToken: %v", err)
	}
	if _, err := loadToken(path); err == nil {
		t.Fatal("expected error for expired token without refresh token, got nil")
	}
}

// TestResolveChannelRef_IDPassthrough_NoServerHit verifies a bare ID is returned
// unchanged without touching the API.
func TestResolveChannelRef_IDPassthrough_NoServerHit(t *testing.T) {
	const ch = "UC1234567890abcdefghijkl"
	called := false
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	got, err := yt.ResolveChannelRef(ChannelRef{Kind: "id", ID: ch})
	if err != nil {
		t.Fatalf("ResolveChannelRef error: %v", err)
	}
	if got != ch {
		t.Errorf("got %q, want the same ID back", got)
	}
	if called {
		t.Error("API was called for an ID ref; expected a pure passthrough")
	}
}

// TestResolveChannelRef_HandleResolvedViaForHandle verifies a handle slug is
// resolved through channels.list?forHandle.
func TestResolveChannelRef_HandleResolvedViaForHandle(t *testing.T) {
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/youtube/v3/channels" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("forHandle") == "SomeHandle" {
			_, _ = fmt.Fprint(w, `{"items":[{"id":"UC_RESOLVED_FROM_HANDLE"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"items":[]}`)
	})
	got, err := yt.ResolveChannelRef(ChannelRef{Kind: "handle", Slug: "SomeHandle"})
	if err != nil {
		t.Fatalf("ResolveChannelRef error: %v", err)
	}
	if got != "UC_RESOLVED_FROM_HANDLE" {
		t.Errorf("got %q, want UC_RESOLVED_FROM_HANDLE", got)
	}
}

// TestResolveChannelRef_FallsBackToForUsername verifies that when forHandle finds
// nothing, the lookup retries via forUsername (covers legacy /user/ names).
func TestResolveChannelRef_FallsBackToForUsername(t *testing.T) {
	calls := 0
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("forHandle") != "" {
			_, _ = fmt.Fprint(w, `{"items":[]}`) // handle miss → fall back
			return
		}
		if r.URL.Query().Get("forUsername") == "LegacyName" {
			_, _ = fmt.Fprint(w, `{"items":[{"id":"UC_FROM_USERNAME"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"items":[]}`)
	})
	got, err := yt.ResolveChannelRef(ChannelRef{Kind: "user", Slug: "LegacyName"})
	if err != nil {
		t.Fatalf("ResolveChannelRef error: %v", err)
	}
	if got != "UC_FROM_USERNAME" {
		t.Errorf("got %q, want UC_FROM_USERNAME", got)
	}
	if calls != 2 {
		t.Errorf("expected 2 API calls (forHandle then forUsername), got %d", calls)
	}
}

// TestResolveChannelRef_NotFound_ReturnsError verifies both lookups missing
// surfaces as an error.
func TestResolveChannelRef_NotFound_ReturnsError(t *testing.T) {
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"items":[]}`)
	})
	if _, err := yt.ResolveChannelRef(ChannelRef{Kind: "handle", Slug: "Ghost"}); err == nil {
		t.Fatal("expected error for unresolved handle, got nil")
	}
}

func TestParseISODuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"PT45S", 45 * time.Second, true},
		{"PT1M", 60 * time.Second, true}, // Short boundary: exactly 60s is a Short
		{"PT1M1S", 61 * time.Second, true},
		{"PT1M30S", 90 * time.Second, true},
		{"PT1H", time.Hour, true},
		{"PT2H1M10S", 2*time.Hour + time.Minute + 10*time.Second, true},
		{"P1D", 24 * time.Hour, true},
		{"P0D", 0, true},
		{"", 0, false},
		{"garbage", 0, false},
		{"P1X", 0, false},
		{"PT1M30", 0, false}, // trailing digits with no unit
	}
	for _, c := range cases {
		got, ok := parseISODuration(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseISODuration(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestFilterRegularVideos_DropsShortsAndStreams(t *testing.T) {
	durFor := map[string]string{"v_regular": "PT10M", "v_short": "PT30S", "v_stream": "PT1H"}
	stream := map[string]bool{"v_stream": true}
	yt := newTestYouTubeWithShorts(t,
		func(w http.ResponseWriter, r *http.Request) { writeVideosList(w, r, durFor, stream) },
		func(w http.ResponseWriter, r *http.Request) {
			// 200 ⇒ Short; 3xx ⇒ regular.
			if strings.TrimPrefix(r.URL.Path, "/shorts/") == "v_short" {
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Redirect(w, r, "/watch", http.StatusFound)
		},
	)
	in := []Video{
		{ID: "v_regular", PublishedAt: "2026-08-01T10:00:00Z"},
		{ID: "v_short", PublishedAt: "2026-08-01T11:00:00Z"},
		{ID: "v_stream", PublishedAt: "2026-08-01T12:00:00Z"},
	}
	got, err := yt.FilterRegularVideos(in)
	if err != nil {
		t.Fatalf("FilterRegularVideos error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "v_regular" {
		t.Errorf("got %+v, want only [v_regular] (Short and stream dropped)", got)
	}
}

func TestFilterRegularVideos_PreservesOrder(t *testing.T) {
	durFor := map[string]string{"a": "PT5M", "b": "PT5M", "c": "PT5M"}
	yt := newTestYouTubeWithShorts(t,
		func(w http.ResponseWriter, r *http.Request) { writeVideosList(w, r, durFor, nil) },
		// All regular (3xx) — verifies newest-first input order is preserved.
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/watch", http.StatusFound)
		},
	)
	in := []Video{
		{ID: "a", PublishedAt: "2026-08-01T03:00:00Z"},
		{ID: "b", PublishedAt: "2026-08-01T02:00:00Z"},
		{ID: "c", PublishedAt: "2026-08-01T01:00:00Z"},
	}
	got, err := yt.FilterRegularVideos(in)
	if err != nil {
		t.Fatalf("FilterRegularVideos error: %v", err)
	}
	if len(got) != 3 || got[0].ID != "a" || got[2].ID != "c" {
		t.Errorf("order not preserved: %+v", got)
	}
}

// TestFilterRegularVideos_DropsShortViaProbe verifies a probe 200 drops a video
// even when its duration is long (so only the probe, not the fallback, drops it).
func TestFilterRegularVideos_DropsShortViaProbe(t *testing.T) {
	durFor := map[string]string{"v_shortprobe": "PT20M", "v_long": "PT20M"}
	yt := newTestYouTubeWithShorts(t,
		func(w http.ResponseWriter, r *http.Request) { writeVideosList(w, r, durFor, nil) },
		func(w http.ResponseWriter, r *http.Request) {
			if strings.TrimPrefix(r.URL.Path, "/shorts/") == "v_shortprobe" {
				w.WriteHeader(http.StatusOK) // 200 ⇒ Short
				return
			}
			http.Redirect(w, r, "/watch", http.StatusFound) // 3xx ⇒ regular
		},
	)
	in := []Video{
		{ID: "v_shortprobe", PublishedAt: "2026-08-01T10:00:00Z"},
		{ID: "v_long", PublishedAt: "2026-08-01T09:00:00Z"},
	}
	got, err := yt.FilterRegularVideos(in)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "v_long" {
		t.Errorf("got %+v, want only [v_long] (probe 200 ⇒ Short dropped)", got)
	}
}

// TestFilterRegularVideos_DropsStream verifies a live stream is dropped before the
// probe is ever attempted.
func TestFilterRegularVideos_DropsStream(t *testing.T) {
	probeHits := 0
	durFor := map[string]string{"v_stream": "PT1H"}
	stream := map[string]bool{"v_stream": true}
	yt := newTestYouTubeWithShorts(t,
		func(w http.ResponseWriter, r *http.Request) { writeVideosList(w, r, durFor, stream) },
		func(w http.ResponseWriter, r *http.Request) { probeHits++ },
	)
	got, err := yt.FilterRegularVideos([]Video{{ID: "v_stream", PublishedAt: "2026-08-01T10:00:00Z"}})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want stream dropped", got)
	}
	if probeHits != 0 {
		t.Errorf("probe hit %d times, want 0 (streams dropped before probe)", probeHits)
	}
}

// TestFilterRegularVideos_FallsBackToDurationOnProbeError verifies that an
// inconclusive probe (5xx) falls back to duration: ≤180s dropped, >180s kept, and
// the probe error is NOT propagated (FilterRegularVideos returns nil error).
func TestFilterRegularVideos_FallsBackToDurationOnProbeError(t *testing.T) {
	durFor := map[string]string{"v_le": "PT2M", "v_gt": "PT4M"} // 120s, 240s
	yt := newTestYouTubeWithShorts(t,
		func(w http.ResponseWriter, r *http.Request) { writeVideosList(w, r, durFor, nil) },
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable) // 503 ⇒ inconclusive
		},
	)
	in := []Video{
		{ID: "v_le", PublishedAt: "2026-08-01T10:00:00Z"},
		{ID: "v_gt", PublishedAt: "2026-08-01T09:00:00Z"},
	}
	got, err := yt.FilterRegularVideos(in)
	if err != nil {
		t.Fatalf("error: %v (probe error must not propagate)", err)
	}
	if len(got) != 1 || got[0].ID != "v_gt" {
		t.Errorf("got %+v, want only [v_gt] (≤180s dropped via fallback, >180s kept)", got)
	}
}

// TestFilterRegularVideos_ProbeErrorUnparseableDuration_Keeps verifies that on an
// inconclusive probe with an unparseable duration, the video is kept (never guessed
// away), matching the long-standing defensive behaviour.
func TestFilterRegularVideos_ProbeErrorUnparseableDuration_Keeps(t *testing.T) {
	durFor := map[string]string{"v_x": ""} // empty ⇒ unparseable
	yt := newTestYouTubeWithShorts(t,
		func(w http.ResponseWriter, r *http.Request) { writeVideosList(w, r, durFor, nil) },
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable) // 503 ⇒ inconclusive
		},
	)
	got, err := yt.FilterRegularVideos([]Video{{ID: "v_x", PublishedAt: "2026-08-01T10:00:00Z"}})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "v_x" {
		t.Errorf("got %+v, want [v_x] kept (probe error + unparseable duration ⇒ keep)", got)
	}
}

// TestFilterRegularVideos_ProbeRedirectNotFollowed proves the redirect trap is
// avoided: /shorts/{id} returns 302 to /watch which itself 200s. Because the probe
// client does not follow redirects, the 302 reads as regular and the video is kept.
func TestFilterRegularVideos_ProbeRedirectNotFollowed(t *testing.T) {
	durFor := map[string]string{"v1": "PT10M"}
	yt := newTestYouTubeWithShorts(t,
		func(w http.ResponseWriter, r *http.Request) { writeVideosList(w, r, durFor, nil) },
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/watch" {
				w.WriteHeader(http.StatusOK) // the trap: a followed redirect would land here
				return
			}
			http.Redirect(w, r, "/watch", http.StatusFound) // 302
		},
	)
	got, err := yt.FilterRegularVideos([]Video{{ID: "v1", PublishedAt: "2026-08-01T10:00:00Z"}})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "v1" {
		t.Errorf("got %+v, want [v1] kept (302 ⇒ inconclusive ⇒ duration fallback; not followed to 200)", got)
	}
}

// TestFilterRegularVideos_Probe3xx_FallsBackToDuration verifies that a probe 3xx
// (e.g. a consent/bot redirect from a datacenter IP) is treated as inconclusive:
// the classifier falls back to duration, dropping a ≤180s Short and keeping a
// >180s regular video. Before fail-closed, the 3xx read as "regular" and the Short
// leaked into the playlist.
func TestFilterRegularVideos_Probe3xx_FallsBackToDuration(t *testing.T) {
	durFor := map[string]string{"v_short": "PT55S", "v_long": "PT10M"}
	yt := newTestYouTubeWithShorts(t,
		func(w http.ResponseWriter, r *http.Request) { writeVideosList(w, r, durFor, nil) },
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/watch", http.StatusFound) // 3xx ⇒ inconclusive
		},
	)
	in := []Video{
		{ID: "v_short", PublishedAt: "2026-08-12T08:00:16Z"},
		{ID: "v_long", PublishedAt: "2026-08-12T07:00:00Z"},
	}
	got, err := yt.FilterRegularVideos(in)
	if err != nil {
		t.Fatalf("FilterRegularVideos error: %v (probe inconclusive must not propagate)", err)
	}
	if len(got) != 1 || got[0].ID != "v_long" {
		t.Errorf("got %+v, want only [v_long] (Short dropped via duration fallback on 3xx)", got)
	}
}

// TestFilterRegularVideos_Probe4xx_FallsBackToDuration verifies that a probe 4xx
// (e.g. 403/429 bot-block from a datacenter IP) is treated as inconclusive and
// falls back to duration, exactly like a 3xx or 5xx.
func TestFilterRegularVideos_Probe4xx_FallsBackToDuration(t *testing.T) {
	durFor := map[string]string{"v_short": "PT55S", "v_long": "PT10M"}
	yt := newTestYouTubeWithShorts(t,
		func(w http.ResponseWriter, r *http.Request) { writeVideosList(w, r, durFor, nil) },
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden) // 4xx ⇒ inconclusive
		},
	)
	in := []Video{
		{ID: "v_short", PublishedAt: "2026-08-12T08:00:16Z"},
		{ID: "v_long", PublishedAt: "2026-08-12T07:00:00Z"},
	}
	got, err := yt.FilterRegularVideos(in)
	if err != nil {
		t.Fatalf("FilterRegularVideos error: %v (probe inconclusive must not propagate)", err)
	}
	if len(got) != 1 || got[0].ID != "v_long" {
		t.Errorf("got %+v, want only [v_long] (Short dropped via duration fallback on 4xx)", got)
	}
}

// TestFilterRegularVideos_VideoAbsentInList_Dropped verifies a video present in the
// input but missing from the videos.list response is dropped, with no probe.
func TestFilterRegularVideos_VideoAbsentInList_Dropped(t *testing.T) {
	probeHits := 0
	yt := newTestYouTubeWithShorts(t,
		func(w http.ResponseWriter, r *http.Request) { writeVideosList(w, r, nil, nil) }, // no items
		func(w http.ResponseWriter, r *http.Request) { probeHits++ },
	)
	got, err := yt.FilterRegularVideos([]Video{{ID: "v_ghost", PublishedAt: "2026-08-01T10:00:00Z"}})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want [] (absent from list ⇒ dropped)", got)
	}
	if probeHits != 0 {
		t.Errorf("probe hit %d times, want 0 (absent video never probed)", probeHits)
	}
}

func TestFilterRegularVideos_EmptyInput_NoServerHit(t *testing.T) {
	called := false
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	got, err := yt.FilterRegularVideos(nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil", got)
	}
	if called {
		t.Error("API was hit for empty input")
	}
}

// --- FR3: OAuth consent robustness (S2) ---

// authResult carries one authorize outcome out of its goroutine.
type authResult struct {
	tok *oauth2.Token
	err error
}

// startAuthorize runs authorize in the background so a test can interact with
// the callback server (or just bound the wait) while it is pending.
func startAuthorize(ctx context.Context, config *oauth2.Config) <-chan authResult {
	ch := make(chan authResult, 1)
	go func() {
		tok, err := authorize(ctx, config)
		ch <- authResult{tok, err}
	}()
	return ch
}

// waitAuthorize collects the authorize result but never hangs: authorize
// already bounds its own wait (consentTimeout, callback-server error
// propagation, ctx cancellation), so this cap is the second line of defense —
// a regression there must not stall the whole package run against go test's
// 10-minute deadline.
func waitAuthorize(t *testing.T, ch <-chan authResult, wait time.Duration) (*oauth2.Token, error) {
	t.Helper()
	select {
	case r := <-ch:
		return r.tok, r.err
	case <-time.After(wait):
		t.Fatalf("authorize still pending after %v (want its own consentTimeout / server-error / ctx path to fire first)", wait)
		return nil, nil
	}
}

// muteLog redirects the standard logger into a buffer for the duration of the
// test (authorize logs the consent URL to stderr) and restores it on cleanup.
func muteLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	old := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return buf
}

// lockedBuffer is a concurrency-safe bytes.Buffer: authorize writes the consent
// URL into it via log.Printf from its own goroutine while the test goroutine
// polls the content — a plain bytes.Buffer would race (and detonate under -race).
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (lb *lockedBuffer) Write(p []byte) (int, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.buf.Write(p)
}

func (lb *lockedBuffer) String() string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.buf.String()
}

// reservePort binds :0 (wildcard) and either keeps the listener alive (busy case)
// or hands back a port that was free at that moment (free case: caller closes
// first). Wildcard binds conflict with authorize's ":"+port wildcard bind.
func reservePort(t *testing.T) (*net.TCPListener, int) {
	t.Helper()
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	return l.(*net.TCPListener), l.Addr().(*net.TCPAddr).Port
}

// dummyConfig builds an oauth2.Config whose endpoints are never contacted
// (Exchange is unreachable in the busy-port and timeout scenarios).
func dummyConfig(port int) *oauth2.Config {
	return &oauth2.Config{
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
		RedirectURL: fmt.Sprintf("http://localhost:%d", port),
	}
}

// setConsentTimeout swaps consentTimeout for the test and restores it on cleanup.
func setConsentTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := consentTimeout
	consentTimeout = d
	t.Cleanup(func() { consentTimeout = old })
}

// TestAuthorize_BusyPort_ReturnsErrorPromptly (FR3): when the local callback
// port is already taken, ListenAndServe fails; authorize must surface that
// failure as an error promptly instead of swallowing the server error and
// blocking forever for a consent that can never arrive. The elapsed bound is
// intentionally stricter than the 3s consentTimeout: a correct implementation
// fails in milliseconds via errCh, while a timeout-only fallback would take ~3s.
func TestAuthorize_BusyPort_ReturnsErrorPromptly(t *testing.T) {
	l, port := reservePort(t) // kept open: the port stays busy for the whole test
	defer func() { _ = l.Close() }()

	muteLog(t)
	setConsentTimeout(t, 3*time.Second)

	config := dummyConfig(port)
	start := time.Now()
	tok, err := waitAuthorize(t, startAuthorize(context.Background(), config), 10*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("authorize returned token %+v, want an error for the busy callback port %d", tok, port)
	}
	if elapsed >= 2*time.Second {
		t.Errorf("authorize took %v to fail on a busy port, want < 2s (server error must propagate, not wait out consentTimeout)", elapsed)
	}
}

// TestAuthorize_Timeout_ReturnsClearError (FR3): when nobody completes the
// consent, authorize must give up after ~consentTimeout with an error that says
// what happened and what to do next (open the logged URL, check the port,
// restart) — not block forever. Elapsed bound is wide against flake: 50ms
// threshold must fire well under 5s.
func TestAuthorize_Timeout_ReturnsClearError(t *testing.T) {
	l, port := reservePort(t)
	_ = l.Close() // port is free for authorize's callback server now

	muteLog(t)
	setConsentTimeout(t, 50*time.Millisecond)

	config := dummyConfig(port)
	start := time.Now()
	_, err := waitAuthorize(t, startAuthorize(context.Background(), config), 10*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("authorize returned no error, want a consent-timeout error")
	}
	if elapsed >= 5*time.Second {
		t.Errorf("authorize took %v, want the 50ms consentTimeout to fire well before 5s", elapsed)
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "timed out") {
		t.Errorf("error %q does not say the consent wait timed out (want substring %q)", err, "timed out")
	}
	// The message must carry an action hint per FR3: open the logged URL/link
	// or check the port. A bare "timed out" is not enough for the user to
	// recover; the exact wording stays free (requirement, not phrasing).
	hint := false
	for _, kw := range []string{"url", "port", "link"} {
		if strings.Contains(msg, kw) {
			hint = true
			break
		}
	}
	if !hint {
		t.Errorf("error %q gives no action hint (want it to mention the logged URL/link or the port to free)", err)
	}
}

// TestAuthorize_ConsentCallback_ReturnsToken is the happy-path regression net
// for the select/goroutine refactor: the authorization URL is still logged, a
// callback with the right state and a code exchanges to a token without error.
func TestAuthorize_ConsentCallback_ReturnsToken(t *testing.T) {
	tokenTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Content-Type matters: oauth2 parses text/plain (the sniffed default)
		// as a query string, not JSON.
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"t","token_type":"Bearer","expiry":%q}`,
			time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer tokenTS.Close()

	l, port := reservePort(t)
	_ = l.Close()
	logBuf := muteLog(t) // capture: the consent URL must still be logged

	config := &oauth2.Config{
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: tokenTS.URL,
		},
		RedirectURL: fmt.Sprintf("http://localhost:%d", port),
	}

	ch := startAuthorize(context.Background(), config)

	authURL, err := loggedAuthURL(logBuf, 5*time.Second)
	if err != nil {
		t.Fatalf("authorization URL not logged: %v", err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse logged auth URL %q: %v", authURL, err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatalf("logged auth URL %q carries no state parameter", authURL)
	}

	// Complete the consent the way the browser would; retry while the callback
	// server is still coming up.
	var resp *http.Response
	for i := 0; i < 100; i++ {
		resp, err = http.Get(fmt.Sprintf("http://localhost:%d/?state=%s&code=test-code", port, url.QueryEscape(state)))
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("callback GET failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("callback status = %d, want 200", resp.StatusCode)
	}

	tok, err := waitAuthorize(t, ch, 10*time.Second)
	if err != nil {
		t.Fatalf("authorize error: %v", err)
	}
	if tok == nil || tok.AccessToken != "t" {
		t.Errorf("token = %+v, want access_token %q", tok, "t")
	}
}

// loggedAuthURL polls buf until the "Open this URL" line shows up and returns
// the authorization URL logged by authorize.
func loggedAuthURL(buf *lockedBuffer, within time.Duration) (string, error) {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, f := range strings.Fields(buf.String()) {
			if strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://") {
				return f, nil
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return "", fmt.Errorf("no URL logged within %v (buffer: %q)", within, buf.String())
}
