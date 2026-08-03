# Manifest Reconcile — youtube

Parent: [manifest-reconciliation](manifest-reconciliation.md).

## Current State

`youtube` is the Google integration facade (OAuth2 + YouTube Data API v3).
`FilterRegularVideos` was added in `cc2ab14`; the youtube CODEMANIFEST,
`youtube/.usages/facade.md`, and `youtube/export_test.go` (TestContractSurface) were
updated in that commit.

## Description

Verify `youtube/CODEMANIFEST` against `youtube/youtube.go` + `youtube/parse.go`, audit
`youtube/.usages/facade.md`, and audit `youtube/export_test.go`.

## Scope

**In scope:**

| Manifest element | Go |
|---|---|
| `Video()` props ID, PublishedAt | `youtube.Video{ID, PublishedAt}` |
| `YouTube(secretsPath, tokenPath, redirectURL)` constructor | `NewYouTube(secretsPath, tokenPath, redirectURL string) (*YouTube, error)` |
| method `ResolveUploads(channelID) -> uploadsPlaylistID, err` | `ResolveUploads(channelID string) (string, error)` |
| method `ListUploads(uploadsPlaylistID) -> videos, err` | `ListUploads(uploadsPlaylistID string) ([]Video, error)` |
| method `FilterRegularVideos(videos) -> regular, err` | `FilterRegularVideos(videos []Video) ([]Video, error)` |
| method `AddToPlaylist(playlistID, videoID) -> itemID, err` | `AddToPlaylist(playlistID, videoID string) (string, error)` |
| method `ResolveNames(channelID, playlistID) -> channelName, playlistName, err` | `ResolveNames(channelID, playlistID string) (string, string, error)` |
| method `ResolveChannelRef(ref) -> id, err` | `ResolveChannelRef(ref ChannelRef) (string, error)` |
| `ChannelRef()` props Kind, ID, Slug; method `IsID() -> ok` | `youtube.ChannelRef{Kind, ID, Slug}`, `IsID() bool` |
| `ParsePlaylistID(s) -> id, ok` | `ParsePlaylistID(s string) (string, bool)` |
| `ParseChannelRef(s) -> ref, ok` | `ParseChannelRef(s string) (ChannelRef, bool)` |
| (test surface) `NewWithService` | `youtube/export_test.go`: `var NewWithService = newWithService` |

- Audit `youtube/.usages/facade.md`: confirm the `FilterRegularVideos` section (≤60s
  Shorts, live-stream exclusion, input-order preservation, pass the
  new-since-watermark subset) matches the implementation.
- Audit `export_test.go`: confirm `NewWithService` is still the only test-only export
  and still aliases `newWithService`.

**Out of scope:** OAuth/API behaviour; the `cooks` (`youtube-data-api.md`,
`google-oauth2.md`).

## Acceptance Criteria

- All types/methods/routines above present and faithful; `Video`, `YouTube`,
  `ChannelRef` fields match; no orphans either direction.
- `FilterRegularVideos` annotation matches its impl (Shorts ≤60s; streams via
  liveStreamingDetails; ≤50-id batching; deleted/private dropped; unparseable kept).
- `facade.md` and `export_test.go` accurate.
- `goga lint` reports nothing for `youtube`.
- `go test ./youtube` passes (incl. `sync_integration_test.go`).

## Stack

Go + `google.golang.org/api/youtube/v3` + `golang.org/x/oauth2`/`google-oauth2`. No new
dependencies.

## External Dependencies

| Component        | Usage file                               | Status             |
|------------------|------------------------------------------|--------------------|
| youtube-data-api | `.goga/usages/cooks/youtube-data-api.md` | existing (out of scope) |
| google-oauth2    | `.goga/usages/cooks/google-oauth2.md`    | existing (out of scope) |

## Risks and Constraints

- `FilterRegularVideos` is the newest, most detailed method — verify the batch size,
  keep/drop rules, and parse-failure-keep behaviour against `youtube.go`.

## Scope Estimate

Single sub-task — small-to-medium (two source files + `.usages` + `export_test`), but
verification only.

## Existing Architecture

`youtube` is a leaf integration cell; imported by `cmd` and `syncer`. `parse.go`
holds the pure (no-API) reference parsing; API resolution lives on `YouTube`.

## Notes

Expected outcome: faithful, no edit required (the manifest, `.usages`, and
`export_test.go` were all updated together in `cc2ab14`).
