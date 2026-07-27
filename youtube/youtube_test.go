package youtube

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
	var _ func(string, string) (string, error) = y.AddToPlaylist
	var _ func(string, string) (string, string, error) = y.ResolveNames
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

func TestResolveUploads_ReturnsUploadsPlaylistID(t *testing.T) {
	yt := newTestYouTube(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"contentDetails":{"relatedPlaylists":{"uploads":"UUabc"}}}]}`)
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
		fmt.Fprint(w, `{"items":[]}`)
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
			fmt.Fprint(w, `{"items":[{"contentDetails":{"videoId":"v1","videoPublishedAt":"2026-07-27T10:00:00Z"}},{"contentDetails":{"videoId":"v2","videoPublishedAt":"2026-07-27T09:00:00Z"}}],"nextPageToken":"TOKEN2"}`)
			return
		}
		fmt.Fprint(w, `{"items":[{"contentDetails":{"videoId":"v3","videoPublishedAt":"2026-07-27T08:00:00Z"}}]}`)
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
		fmt.Fprint(w, `{"items":[]}`)
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
		fmt.Fprint(w, `{"id":"PLI1"}`)
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
			fmt.Fprint(w, `{"items":[{"snippet":{"title":"Foo Channel"}}]}`)
		case "/youtube/v3/playlists":
			fmt.Fprint(w, `{"items":[{"snippet":{"title":"My Playlist"}}]}`)
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
			fmt.Fprint(w, `{"items":[]}`)
		case "/youtube/v3/playlists":
			fmt.Fprint(w, `{"items":[{"snippet":{"title":"P"}}]}`)
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
			fmt.Fprint(w, `{"items":[{"snippet":{"title":"C"}}]}`)
		case "/youtube/v3/playlists":
			fmt.Fprint(w, `{"items":[]}`)
		}
	})
	if _, _, err := yt.ResolveNames("UCa", "PLx"); err == nil {
		t.Fatal("expected playlist-not-found error, got nil")
	}
}
