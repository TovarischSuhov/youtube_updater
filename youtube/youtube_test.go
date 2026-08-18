package youtube

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
	var _ func(string) ([]Video, error) = y.ListUploads
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
	got, err := yt.ListUploads("UUx")
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
	got, err := yt.ListUploads("UUx")
	if err != nil {
		t.Fatalf("ListUploads error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
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
