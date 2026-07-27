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
	if !tok.Valid() {
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
