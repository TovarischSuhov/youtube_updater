# youtube facade — consumer guide

## Domain
How to construct and use the `YouTube` facade (package `youtube`) from outside the
cell. The facade encapsulates OAuth2 and the YouTube Data API; no pointer or HTTP
type crosses the boundary.

## Obtain a facade
Construct once per run with paths only:

    youTube, err := youtube.NewYouTube(secretsPath, tokenPath, redirectURL)

First construction with no cached token opens a browser for one-time consent and
writes the token to tokenPath; later constructions reuse and refresh it silently.

## Resolve a channel's uploads

    uploadsID, err := youTube.ResolveUploads(channelID)

## Enumerate uploads (newest first)

    videos, err := youTube.ListUploads(uploadsID) // []youtube.Video

## Keep only regular videos (drop Shorts and streams)

    regular, err := youTube.FilterRegularVideos(videos) // []youtube.Video

Removes Shorts and live streams (any video with live streaming details) and
returns the rest in input order. Shorts are classified by probing
`youtube.com/shorts/{id}` (HTTP 200 = Short); if the probe does not return HTTP
200, the classifier falls back to duration ≤ 180s. Pass the new-since-watermark
subset so classification batches only what may be added.

## Add a video to a playlist

    itemID, err := youTube.AddToPlaylist(playlistID, videoID)

## Resolve channel and playlist names

Fetch the human-readable titles (e.g. for a config `list` command):

    channelName, playlistName, err := youTube.ResolveNames(channelID, playlistID)

Fails if the channel is not found, or the playlist is not found / not accessible
(the playlist must be public or owned by the authenticated user).

## Constraints for consumers
- Call AddToPlaylist only for videos already deduplicated upstream; the facade does
  not check for duplicates.
- Methods are safe for sequential calls within a single sync pass.
