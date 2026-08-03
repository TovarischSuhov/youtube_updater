// Package youtube is the Google integration facade: it owns OAuth2 credentials
// and all YouTube Data API v3 calls, hiding pointer and HTTP types behind a
// string-based contract.
package youtube

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	youtubev3 "google.golang.org/api/youtube/v3"
)

// youtubeScope permits reading and modifying the user's YouTube data (playlists).
const youtubeScope = "https://www.googleapis.com/auth/youtube"

// Video is a YouTube video as relevant to sync: its identifier and publish time.
type Video struct {
	ID          string
	PublishedAt string // RFC 3339; used as the new-video watermark
}

// YouTube is the facade over Google credentials and the YouTube Data API v3.
type YouTube struct {
	svc *youtubev3.Service
}

// newWithService builds a facade from an already-constructed service (used in
// tests to point the service at an httptest server).
func newWithService(svc *youtubev3.Service) *YouTube {
	return &YouTube{svc: svc}
}

// NewYouTube constructs the facade: it obtains an authenticated HTTP client via
// OAuth2 (cached token if present, otherwise a one-time consent flow) and builds
// the YouTube service client. secretsPath points to a Desktop OAuth
// client_secrets.json; tokenPath caches the granted token; redirectURL is the
// loopback consent callback.
func NewYouTube(secretsPath, tokenPath, redirectURL string) (*YouTube, error) {
	ctx := context.Background()
	config, err := configFromSecrets(secretsPath, redirectURL)
	if err != nil {
		return nil, err
	}
	tok, err := tokenFor(ctx, config, tokenPath)
	if err != nil {
		return nil, err
	}
	client := httpClientWithSave(ctx, config, tok, tokenPath)
	svc, err := youtubev3.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("youtube: build service: %w", err)
	}
	return &YouTube{svc: svc}, nil
}

// ResolveUploads resolves a channel's uploads playlist identifier.
func (y *YouTube) ResolveUploads(channelID string) (string, error) {
	var uploads string
	err := withRetry("resolve uploads", func() error {
		resp, err := y.svc.Channels.List([]string{"contentDetails"}).Id(channelID).Do()
		if err != nil {
			return err
		}
		if len(resp.Items) == 0 {
			return &channelNotFoundError{id: channelID}
		}
		uploads = resp.Items[0].ContentDetails.RelatedPlaylists.Uploads
		return nil
	})
	if err != nil {
		return "", err
	}
	return uploads, nil
}

// ListUploads enumerates a channel's uploads, newest first.
func (y *YouTube) ListUploads(uploadsPlaylistID string) ([]Video, error) {
	var videos []Video
	err := withRetry("list uploads", func() error {
		call := y.svc.PlaylistItems.List([]string{"snippet", "contentDetails"}).
			PlaylistId(uploadsPlaylistID).MaxResults(50)
		return call.Pages(context.Background(), func(resp *youtubev3.PlaylistItemListResponse) error {
			for _, it := range resp.Items {
				vid := it.ContentDetails.VideoId
				if vid == "" {
					continue // playlist items may be non-video (e.g. deleted)
				}
				videos = append(videos, Video{ID: vid, PublishedAt: it.ContentDetails.VideoPublishedAt})
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return videos, nil
}

// shortMaxDuration is the upper bound for a YouTube Short: videos at or below 60s
// are treated as Shorts and excluded from sync.
const shortMaxDuration = 60 * time.Second

// FilterRegularVideos drops Shorts (duration ≤ shortMaxDuration) and live streams
// (any video carrying liveStreamingDetails — live, ended, or premiere) from
// videos, returning only regular long-form uploads in their original order.
//
// It classifies via videos.list (contentDetails for duration, liveStreamingDetails
// for streams), batched at ≤50 ids per call. A video absent from the response
// (deleted or made private between listing and classification) is dropped — it
// cannot be added to a playlist anyway. A video whose duration fails to parse is
// kept rather than silently dropped on an unrecognised format.
func (y *YouTube) FilterRegularVideos(videos []Video) ([]Video, error) {
	if len(videos) == 0 {
		return nil, nil
	}
	ids := make([]string, len(videos))
	for i, v := range videos {
		ids[i] = v.ID
	}

	keep := make(map[string]bool, len(videos))
	for start := 0; start < len(ids); start += 50 {
		batch := ids[start:min(start+50, len(ids))]
		if err := withRetry("classify videos", func() error {
			resp, err := y.svc.Videos.List([]string{"contentDetails", "liveStreamingDetails"}).Id(batch...).Do()
			if err != nil {
				return err
			}
			for _, it := range resp.Items {
				d, ok := parseISODuration(it.ContentDetails.Duration)
				if !ok {
					keep[it.Id] = true // unrecognised format: don't drop on a guess
					continue
				}
				if d <= shortMaxDuration {
					continue // Short
				}
				if it.LiveStreamingDetails != nil {
					continue // live stream / premiere
				}
				keep[it.Id] = true
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	out := make([]Video, 0, len(keep))
	for _, v := range videos {
		if keep[v.ID] {
			out = append(out, v)
		}
	}
	return out, nil
}

// parseISODuration parses the subset of ISO 8601 durations YouTube returns for
// video lengths (e.g. "PT1M30S", "PT45S", "PT1H", "P1D"). ok=false if s is not a
// recognised duration. YouTube never emits month/year components, so 'M' is taken
// as minutes (it only ever follows 'T') and 'D' as days.
func parseISODuration(s string) (time.Duration, bool) {
	if len(s) < 2 || s[0] != 'P' {
		return 0, false
	}
	var d time.Duration
	num := ""
	flush := func(unit byte) bool {
		if num == "" {
			return false
		}
		n, err := strconv.Atoi(num)
		num = ""
		if err != nil {
			return false
		}
		switch unit {
		case 'D':
			d += time.Duration(n) * 24 * time.Hour
		case 'H':
			d += time.Duration(n) * time.Hour
		case 'M':
			d += time.Duration(n) * time.Minute
		case 'S':
			d += time.Duration(n) * time.Second
		}
		return true
	}
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			num += string(c)
		case c == 'T':
			// separator between date and time components
		case c == 'D' || c == 'H' || c == 'M' || c == 'S':
			if !flush(c) {
				return 0, false
			}
		default:
			return 0, false
		}
	}
	return d, num == "" // trailing digits with no unit is malformed
}

// AddToPlaylist inserts a video into a playlist and returns the created item id.
func (y *YouTube) AddToPlaylist(playlistID, videoID string) (string, error) {
	var itemID string
	err := withRetry("add to playlist", func() error {
		item := &youtubev3.PlaylistItem{
			Snippet: &youtubev3.PlaylistItemSnippet{
				PlaylistId: playlistID,
				ResourceId: &youtubev3.ResourceId{
					Kind:    "youtube#video",
					VideoId: videoID,
				},
			},
		}
		created, err := y.svc.PlaylistItems.Insert([]string{"snippet"}, item).Do()
		if err != nil {
			return err
		}
		itemID = created.Id
		return nil
	})
	if err != nil {
		return "", err
	}
	return itemID, nil
}

// ResolveNames resolves the human-readable channel display name and playlist
// title. The playlist must be public or owned by the user, otherwise it is
// treated as not found.
func (y *YouTube) ResolveNames(channelID, playlistID string) (string, string, error) {
	var channelName, playlistName string
	err := withRetry("resolve channel name", func() error {
		resp, err := y.svc.Channels.List([]string{"snippet"}).Id(channelID).Do()
		if err != nil {
			return err
		}
		if len(resp.Items) == 0 {
			return &channelNotFoundError{id: channelID}
		}
		channelName = resp.Items[0].Snippet.Title
		return nil
	})
	if err != nil {
		return "", "", err
	}
	err = withRetry("resolve playlist name", func() error {
		resp, err := y.svc.Playlists.List([]string{"snippet"}).Id(playlistID).Do()
		if err != nil {
			return err
		}
		if len(resp.Items) == 0 {
			return &playlistNotFoundError{id: playlistID}
		}
		playlistName = resp.Items[0].Snippet.Title
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return channelName, playlistName, nil
}

// ResolveChannelRef resolves a channel reference to a concrete channel ID. A bare
// ID is returned unchanged with no API call. Handle, custom, and username slugs
// are resolved via channels.list — forHandle first (the modern default, which
// also resolves most legacy /c/ custom URLs), then forUsername as a fallback for
// /user/ usernames. Returns a channelNotFoundError if neither matches.
func (y *YouTube) ResolveChannelRef(ref ChannelRef) (string, error) {
	if ref.IsID() {
		return ref.ID, nil
	}
	slug := strings.TrimPrefix(strings.TrimSpace(ref.Slug), "@")
	if slug == "" {
		return "", fmt.Errorf("youtube: empty channel slug")
	}
	var id string
	err := withRetry("resolve channel ref", func() error {
		resp, err := y.svc.Channels.List([]string{"id"}).ForHandle(slug).Do()
		if err != nil {
			return err
		}
		if len(resp.Items) > 0 {
			id = resp.Items[0].Id
			return nil
		}
		// Fallback for legacy /user/ usernames and older custom URLs.
		resp, err = y.svc.Channels.List([]string{"id"}).ForUsername(slug).Do()
		if err != nil {
			return err
		}
		if len(resp.Items) == 0 {
			return &channelNotFoundError{id: slug}
		}
		id = resp.Items[0].Id
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// --- OAuth2 (per google-oauth2 usage) ---

func configFromSecrets(secretsPath, redirectURL string) (*oauth2.Config, error) {
	b, err := os.ReadFile(secretsPath)
	if err != nil {
		return nil, fmt.Errorf("youtube: read client secrets: %w", err)
	}
	config, err := google.ConfigFromJSON(b, youtubeScope)
	if err != nil {
		return nil, fmt.Errorf("youtube: parse client secrets: %w", err)
	}
	config.RedirectURL = redirectURL
	return config, nil
}

func tokenFor(ctx context.Context, config *oauth2.Config, tokenPath string) (*oauth2.Token, error) {
	if tok, err := loadToken(tokenPath); err == nil {
		return tok, nil
	}
	tok, err := authorize(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := saveToken(tokenPath, tok); err != nil {
		return nil, fmt.Errorf("youtube: persist token: %w", err)
	}
	return tok, nil
}

// authorize runs the consent flow on a local callback server and exchanges the
// returned code for a token. AccessTypeOffline ensures a refresh token is granted.
func authorize(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
	port := portFromRedirect(config.RedirectURL)
	state := randomState()
	authURL := config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			errCh <- fmt.Errorf("oauth: %s", e)
			http.Error(w, e, http.StatusBadRequest)
			return
		}
		if q.Get("state") != state {
			errCh <- errors.New("oauth: state mismatch")
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		codeCh <- q.Get("code")
		fmt.Fprintln(w, "Authorization complete. You can close this tab.")
	})
	srv := &http.Server{Addr: ":" + port, Handler: mux}
	go func() { _ = srv.ListenAndServe() }()
	defer srv.Shutdown(ctx)

	log.Printf("Open this URL to authorize (once):\n  %s\n", authURL)
	select {
	case code := <-codeCh:
		return config.Exchange(ctx, code)
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func loadToken(path string) (*oauth2.Token, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, err
	}
	// An expired access token is still usable when a refresh token is present:
	// config.TokenSource will mint a fresh access token from it on demand. This
	// is what lets non-interactive runs (e.g. scheduled CI) work — by run time
	// the cached access token has almost always expired, but the refresh token
	// is long-lived. Only reject a stored token when it can neither be used nor
	// refreshed.
	if tok.RefreshToken == "" && !tok.Valid() {
		return nil, errors.New("youtube: stored token invalid")
	}
	return &tok, nil
}

func saveToken(path string, tok *oauth2.Token) error {
	b, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// savingTokenSource wraps a token source so refreshed tokens are persisted.
type savingTokenSource struct {
	base oauth2.TokenSource
	path string
	mu   sync.Mutex
}

func (s *savingTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, err := s.base.Token()
	if err != nil {
		return nil, err
	}
	_ = saveToken(s.path, tok) // best-effort
	return tok, nil
}

func httpClientWithSave(ctx context.Context, config *oauth2.Config, tok *oauth2.Token, tokenPath string) *http.Client {
	base := config.TokenSource(ctx, tok) // refreshes automatically
	src := &savingTokenSource{base: base, path: tokenPath}
	return oauth2.NewClient(ctx, src)
}

func portFromRedirect(redirectURL string) string {
	u, err := url.Parse(redirectURL)
	if err != nil || u.Port() == "" {
		return "8080"
	}
	return u.Port()
}

func randomState() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "insecure-state-fallback"
	}
	return hex.EncodeToString(b)
}

// --- errors & retry ---

type channelNotFoundError struct{ id string }

func (e *channelNotFoundError) Error() string {
	return fmt.Sprintf("youtube: channel not found: %s", e.id)
}

type playlistNotFoundError struct{ id string }

func (e *playlistNotFoundError) Error() string {
	return fmt.Sprintf("youtube: playlist not found: %s", e.id)
}

// withRetry runs op with a bounded exponential backoff on transient errors
// (quota/rate-limit 403, 5xx). Non-retryable errors return immediately.
func withRetry(op string, fn func() error) error {
	const maxAttempts = 3
	var last error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		last = fn()
		if last == nil {
			return nil
		}
		if !retryable(last) {
			return fmt.Errorf("youtube: %s: %w", op, last)
		}
		if attempt < maxAttempts {
			time.Sleep(time.Duration(1<<attempt) * time.Second) // 2s, 4s
		}
	}
	return fmt.Errorf("youtube: %s after %d attempts: %w", op, maxAttempts, last)
}

func retryable(err error) bool {
	gerr, ok := errors.AsType[*googleapi.Error](err)
	if !ok {
		return false
	}
	if gerr.Code >= 500 {
		return true
	}
	if gerr.Code == 403 {
		for _, e := range gerr.Errors {
			switch e.Reason {
			case "quotaExceeded", "rateLimitExceeded", "userRateLimitExceeded":
				return true
			}
		}
	}
	return false
}
