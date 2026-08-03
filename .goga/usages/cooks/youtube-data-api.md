# YouTube Data API v3 (Go)

## Domain

This usage describes how to call the **YouTube Data API v3** from Go using the
official client `google.golang.org/api/youtube/v3`. It covers exactly the
operations this project needs: resolving a channel's uploads playlist,
enumerating recent uploads, and inserting a video into a playlist.

**Audience:** the implementing agent and any developer wiring up the YouTube
service client. Assumes an authenticated `*http.Client` is already available
(see `google-oauth2` for how to obtain one — but this file is self-contained:
every snippet below takes a ready `*youtube.Service`).

## Prerequisites

```bash
go get google.golang.org/api/youtube/v3
go get google.golang.org/api/option
```

The service is constructed once from an OAuth2-backed HTTP client:

```go
import (
    "context"
    "google.golang.org/api/option"
    "google.golang.org/api/youtube/v3"
)

// client is an *http.Client whose transport supplies a valid OAuth2 token.
func newYouTubeService(ctx context.Context, client *http.Client) (*youtube.Service, error) {
    return youtube.NewService(ctx, option.WithHTTPClient(client))
}
```

The scope requested at authentication time must permit playlist writes — use
`youtube.YoutubeScope` (`https://www.googleapis.com/auth/youtube`).

## Scenario 1 — Resolve a channel's uploads playlist

A channel's uploads are exposed as a special playlist. Given a channel ID
(`UC…`), fetch its `contentDetails` to read the uploads playlist ID.

```go
// uploadsPlaylistID returns the uploads playlist ID for a channel, or an error.
func uploadsPlaylistID(ctx context.Context, svc *youtube.Service, channelID string) (string, error) {
    resp, err := svc.Channels.
        List([]string{"contentDetails"}).
        Id(channelID).
        Do()
    if err != nil {
        return "", err
    }
    if len(resp.Items) == 0 {
        return "", fmt.Errorf("channel not found: %s", channelID)
    }
    return resp.Items[0].ContentDetails.RelatedPlaylists.Uploads, nil
}
```

`resp.Items[0].ContentDetails.RelatedPlaylists.Uploads` is the playlist ID to
enumerate in Scenario 2.

## Scenario 2 — Enumerate recent uploads (paginated)

List the items of the uploads playlist. Use `.Pages()` to handle pagination
automatically (50 items per page). Each item's video ID is at
`item.ContentDetails.VideoId`; publish time is at `item.ContentDetails.VideoPublishedAt`
(RFC 3339).

```go
type Video struct {
    ID        string
    Published time.Time
}

// recentUploads enumerates every item currently in the uploads playlist.
// Note: the uploads playlist is bounded (typically the most recent ~200 videos),
// so this is the right source for "what exists now" — e.g. seeding state.
func recentUploads(ctx context.Context, svc *youtube.Service, uploadsPlaylistID string) ([]Video, error) {
    var videos []Video
    err := svc.PlaylistItems.
        List([]string{"snippet", "contentDetails"}).
        PlaylistId(uploadsPlaylistID).
        MaxResults(50).
        Pages(ctx, func(resp *youtube.PlaylistItemListResponse) error {
            for _, it := range resp.Items {
                vid := it.ContentDetails.VideoId
                if vid == "" {
                    continue // playlist items can be non-video (e.g. deleted)
                }
                pub, _ := time.Parse(time.RFC3339, it.ContentDetails.VideoPublishedAt)
                videos = append(videos, Video{ID: vid, Published: pub})
            }
            return nil
        })
    return videos, err
}
```

Tip: enumerate newest-first by walking the result in page order; the uploads
playlist returns newest items on the first page.

## Scenario 3 — Insert a video into a playlist

Add a video to the user's target playlist. The resource id pins the video.

```go
// addToPlaylist inserts videoID into playlistID and returns the created item's ID.
func addToPlaylist(ctx context.Context, svc *youtube.Service, playlistID, videoID string) (string, error) {
    item := &youtube.PlaylistItem{
        Snippet: &youtube.PlaylistItemSnippet{
            PlaylistId: playlistID,
            ResourceId: &youtube.ResourceId{
                Kind:    "youtube#video",
                VideoId: videoID,
            },
        },
    }
    created, err := svc.PlaylistItems.Insert([]string{"snippet"}, item).Do()
    if err != nil {
        return "", err
    }
    return created.Id, nil
}
```

If a video is already in the playlist, the API still returns success — it does
**not** deduplicate. De-duplication is the caller's responsibility (this project
relies on local seen-ID state to guarantee it).

## Scenario 4 — Resolve channel and playlist names

Fetch the human-readable channel display name and playlist title (for the config's
`list` output). Both cost 1 quota unit each.

Channel name via `channels.list` with `part=snippet`:

```go
resp, err := svc.Channels.List([]string{"snippet"}).Id(channelID).Do()
if err != nil {
    return "", "", err
}
if len(resp.Items) == 0 {
    return "", "", fmt.Errorf("channel not found: %s", channelID)
}
channelName := resp.Items[0].Snippet.Title
```

Playlist title via `playlists.list` with `part=snippet`:

```go
resp, err := svc.Playlists.List([]string{"snippet"}).Id(playlistID).Do()
if err != nil {
    return channelName, "", err
}
if len(resp.Items) == 0 {
    return channelName, "", fmt.Errorf("playlist not found: %s", playlistID)
}
playlistName := resp.Items[0].Snippet.Title
```

`playlists.list` returns the title only for playlists that are public or owned by
the authenticated user; a private, non-owned playlist yields no items (treat as
not found).

## Scenario 5 — Resolve a channel from a handle or username

A channel reference may arrive as an `@handle`, a legacy `/user/` username, or a
`/c/` custom name rather than a `UC…` ID. Resolve it to a concrete channel ID with
`channels.list`, trying `forHandle` first (the modern default, which also matches
most custom URLs) and falling back to `forUsername` for legacy usernames.
`forHandle` accepts the slug with or without a leading `@`.

```go
// channelIDFromSlug resolves a handle/username slug to a channel ID.
func channelIDFromSlug(ctx context.Context, svc *youtube.Service, slug string) (string, error) {
    resp, err := svc.Channels.List([]string{"id"}).ForHandle(slug).Do()
    if err != nil {
        return "", err
    }
    if len(resp.Items) > 0 {
        return resp.Items[0].Id, nil
    }
    // Legacy fallback for /user/ usernames and older custom URLs.
    resp, err = svc.Channels.List([]string{"id"}).ForUsername(slug).Do()
    if err != nil {
        return "", err
    }
    if len(resp.Items) == 0 {
        return "", fmt.Errorf("channel not found for slug: %s", slug)
    }
    return resp.Items[0].Id, nil
}
```

The channel `id` is always present in the response, so `part=id` suffices and
still costs 1 quota unit. A bare `UC…` ID needs no call — parse and use it
directly.

## Quota accounting

The API enforces a daily quota (default **10 000 units**). Cost per call used
here:

| Call                  | part requested          | Cost  |
|-----------------------|-------------------------|-------|
| `channels.list`       | `contentDetails`        | 1     |
| `channels.list`       | `id` (forHandle/forUsername) | 1 |
| `playlistItems.list`  | `snippet,contentDetails`| 1     |
| `playlistItems.insert`| `snippet`               | 50    |

Practical impact: one full sync of N channels costs roughly `N` (channel
resolve) + `N` (one list page each) + `50 × (new videos added)`. For a handful
of channels and a few new videos per day this stays far below the daily cap;
insert cost dominates, so prefer **one insert per genuinely-new video** and
never re-insert.

## Error handling

- API errors arrive as `*googleapi.Error` in `err`. Inspect
  `e.Code` (HTTP status) and `e.Errors[]` (per-reason details).
- `e.Code == 403` with reason `quotaExceeded` or `rateLimitExceeded` → back off
  and retry; do not treat as a permanent failure.
- `e.Code == 404` on `channels.list` → channel ID is wrong/private; skip it.
- Honor `googleapi.Error`'s rate-limit signals with exponential backoff rather
  than a tight retry loop.
