# youtube-updater

Syncs new uploads from YouTube channels you follow into playlists you own, so new
videos land in one place without manual adding. It tracks only the **last seen
video per channel** — the first sync seeds a cursor and adds nothing, and each
later sync adds just the videos published since.

A CLI written in Go. No server, no database: config is a YAML file, state is a
JSON cursor file, and auth is a cached OAuth refresh token.

## How syncing works

For each configured channel → playlist pair:

1. **First contact** — the channel's cursor is *seeded* to its current newest
   upload. Nothing is added. This avoids dumping a channel's entire back-catalog
   into your playlist.
2. **Later syncs** — only videos published *after* the stored watermark are added
   (inserted oldest → newest so the playlist stays chronological), then the
   watermark advances.

Add `--dry-run` to see what *would* be added without touching the playlist or state.

## Requirements

- Go 1.26.3+
- A Google OAuth **Desktop app** client (see setup below)

## Setup

### 1. Create OAuth credentials

1. In the [Google Cloud Console](https://console.cloud.google.com/), create or
   pick a project.
2. Enable the **YouTube Data API v3**.
3. Under *APIs & Services → Credentials*, create an **OAuth client ID** of type
   **Desktop app**.
4. Download the JSON and save it as `client_secrets.json` in the repo root.

The required scope is `https://www.googleapis.com/auth/youtube` (read and modify
your playlists) — already baked into the tool.

### 2. Build

```sh
go build -o youtube-updater ./cmd
# or run directly without producing a binary:  go run ./cmd
```

### 3. Authorize (one-time)

```sh
./youtube-updater
```

With no cached token yet, the tool prints an authorization URL and listens on
`http://localhost:8080` for the consent callback. Open the URL, approve, and the
granted token (with a refresh token) is saved to `token.json`. Later runs reuse
and auto-refresh it — no browser needed again.

## Usage

```sh
# Sync all configured channels (the default command)
./youtube-updater

# Preview without writing to the playlist or state
./youtube-updater --dry-run

# Add a channel → playlist pair (resolves names from the API).
# Both accept a bare ID OR a pasted YouTube URL:
./youtube-updater \
  --add-channel https://www.youtube.com/@SomeHandle \
  --add-playlist https://www.youtube.com/playlist?list=PLxxxxxxxx

# Bare IDs still work too:
#   ./youtube-updater --add-channel UCxxxxxxxx --add-playlist PLxxxxxxxx

# List configured pairs
./youtube-updater --list

# Remove a pair (offline — no API call). Accepts a channel ID or a
# /channel/UC… URL; handles need the API, so run --list for the ID first.
./youtube-updater --remove-channel UCxxxxxxxx
```

`--add-channel` accepts: a bare `UC…` ID, a `…/channel/UC…` URL, or a `@handle` /
`/c/Custom` / `/user/Legacy` URL (resolved to a channel ID via the API).
`--add-playlist` accepts: a bare playlist ID or any URL with a `?list=` parameter
(playlist or watch URL). `--remove-channel` accepts IDs and `/channel/UC…` URLs
only — it stays offline.

### Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--config` | `config.yaml` | Channel → playlist mappings |
| `--secrets` | `client_secrets.json` | OAuth Desktop client secrets |
| `--token` | `token.json` | Cached OAuth token (refresh token) |
| `--state` | `state.json` | Per-channel last-seen cursor |
| `--redirect` | `http://localhost:8080` | OAuth consent callback URL |
| `--dry-run` | `false` | Detect only — no inserts, no state change |
| `--log-level` | `info` | `debug` / `info` / `warn` / `error` |
| `--add-channel` / `--add-playlist` | — | Add a pair (required together) |
| `--remove-channel` | — | Remove a pair |
| `--list` | — | List pairs |

`--add-channel`/`--add-playlist`, `--remove-channel`, and `--list` are mutually
exclusive.

## Files

| File | Tracked? | Purpose |
| --- | --- | --- |
| `config.yaml` | committed | Your channel → playlist pairs |
| `client_secrets.json` | gitignored | OAuth client credentials |
| `token.json` | gitignored | Cached OAuth token (refresh token) |
| `state.json` | gitignored | Per-channel last-seen watermark |

`config.yaml` holds no secrets, so commit it with your pairs. The other three are
gitignored — keep them local.

### `config.yaml` format

Normally managed via `--add-channel` / `--remove-channel` (rarely hand-edited):

```yaml
channels:
  - channel_id: UCxxxxxxxxxxxxxxxx
    playlist_id: PLxxxxxxxxxxxxxxxx
    channel_name: Foo Channel
    playlist_name: My Playlist
```

## Scheduled sync (GitHub Actions)

A daily workflow at [`.github/workflows/sync.yml`](.github/workflows/sync.yml)
runs the sync in CI so you don't have to. To enable it:

1. Complete the one-time authorization locally (above) to produce a `token.json`
   with a refresh token.
2. Add two repository secrets: `CLIENT_SECRETS_JSON` and `TOKEN_JSON` (full file
   contents each).
3. Commit `config.yaml` with your pairs.

The workflow writes both secrets to disk, runs the sync, and caches `state.json`
between runs so the watermark advances instead of re-seeding each time. The
refresh token is long-lived, so the same `TOKEN_JSON` secret is reused every run.
Trigger it manually from the Actions tab or let the daily schedule run it.

## Manual status check (GitHub Actions)

A second workflow at [`.github/workflows/status.yml`](.github/workflows/status.yml)
runs on demand from the Actions tab (no schedule). It lists your configured pairs,
runs a sync, and **marks any channel that failed to update** — without failing the
run. Failed channels show up as warning annotations in the log and in a "Sync
status" section on the run-summary page; the job stays green, so you can see which
channels need attention without a hard failure. It shares the same `state.json`
cache and concurrency group as the daily sync, so the two never overlap or diverge.

No extra setup beyond the two secrets and a committed `config.yaml`.

## Project layout

```
cmd/      main: flag parsing, command dispatch, logging
config/   channel → playlist YAML model + atomic persistence
state/    per-channel last-seen cursor (JSON)
youtube/  OAuth2 + YouTube Data API v3 facade
syncer/   per-channel sync orchestration (seed / advance)
```
