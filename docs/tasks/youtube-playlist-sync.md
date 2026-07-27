# YouTube Channel → Playlist Sync

## Current State

Greenfield. The project currently contains only `.claude/` — no source code, no
config, and no cells architecture (`goga schema` returns `[]`).

`.goga/config.yml` has been initialized for this task: `language: golang`,
`build`/`pipeline` agent `claude`, with two project-level usages declared under
`codemanifest.usages` — `youtube-data-api` and `google-oauth2` — both already
authored under `.goga/usages/cooks/`. This task creates the first executable
from scratch.

## Description

A one-shot Go CLI that monitors a configured set of YouTube channels and appends
each channel's **new** uploads to a designated playlist owned by the user.

- The channel → playlist mapping is supplied by **IDs** (`UC… → PL…`) in a
  hand-edited YAML config.
- For each channel the tool resolves the channel's uploads playlist and enumerates
  its current videos.
- **First contact** with a channel **seeds** local state with the channel's
  current video IDs and adds **nothing** to the playlist.
- On every later run, only videos whose IDs are **not yet in state** are inserted
  into the mapped playlist; their IDs are then recorded in state.
- State is persisted to a local JSON file, so re-runs are **idempotent** (never
  duplicate an insert).
- Authentication uses **OAuth2 desktop credentials** — mandatory, because
  playlist writes require user consent (an API key is read-only and a service
  account is not usable for YouTube playlists).

## Scope

**In scope:**
- Read YAML config: a list of `{ channel_id, playlist_id }` entries.
- Resolve each channel's uploads playlist via `channels.list` (`contentDetails`).
- Enumerate uploads via `playlistItems.list` (paginated, newest-first).
- Seed-on-first-contact: if a channel is absent from state, record its current
  video IDs and add nothing.
- Insert unseen videos into the mapped playlist via `playlistItems.insert`;
  record their IDs in state.
- Persist state to a local JSON file with an atomic write (temp file + rename).
- OAuth2 desktop consent flow with a cached token and transparent auto-refresh,
  persisting refreshed tokens.
- CLI flags: `--config`, `--secrets`, `--state`, `--token`, `--dry-run`.
- Structured logging (`log/slog`) of per-channel results: new count, inserted IDs,
  skipped, and API unit cost.
- Unit tests for pure logic (state diffing, config parsing).

**Out of scope:**
- Web UI; long-running daemon / built-in scheduler (the binary is one-shot; run
  it via `cron`/`systemd` externally if periodic execution is wanted).
- Database server; any remote state store.
- Downloading or transcoding video files.
- Editing, removing, or reordering existing playlist items.
- `@handle` / playlist-title resolution (config is ID-only).
- Multi-account / multiple YouTube users.

## Acceptance Criteria

- `go build` produces a single static binary with no errors.
- With a valid config, `client_secrets.json`, and a completed one-time consent,
  running the binary on a **never-seen** channel adds nothing and records the
  channel's current uploads in the state file.
- After a new video is published on that channel, the next run inserts **exactly**
  that one video into the mapped playlist and records its ID; further re-runs with
  no new videos perform **zero** `playlistItems.insert` calls.
- Deleting the state file and re-running reproduces first-contact behavior
  (seeds, adds nothing) — confirming state is the sole source of truth.
- `--dry-run` prints the videos that would be inserted and mutates **nothing**
  (no inserts, no state change).
- The total YouTube API unit cost of each sync is logged.
- `client_secrets.json` and the token/state files are gitignored and written with
  file mode `0600`.

## Stack

- **Frameworks:** none (plain Go CLI).
- **Libraries:**
  - `google.golang.org/api/youtube/v3` — YouTube Data API v3 client.
  - `golang.org/x/oauth2` + `golang.org/x/oauth2/google` — OAuth2 desktop flow & token caching.
  - `google.golang.org/api/option` — pass credentials into the service client.
  - `gopkg.in/yaml.v3` — parse the hand-edited config.
  - Stdlib for the rest: `encoding/json` (state), `flag` (CLI), `log/slog` (logging),
    `net/http` (OAuth callback), `os`, `sync`.
- **Infrastructure:** none. Local files only: `config.yaml`, `state.json`, cached
  token file, user-provided `client_secrets.json`.

## External Dependencies

| Component | Usage file | Status |
|-----------|------------|--------|
| YouTube Data API v3 | `.goga/usages/cooks/youtube-data-api.md` | created |
| Google OAuth2 (Go desktop flow) | `.goga/usages/cooks/google-oauth2.md` | created |

## Risks and Constraints

- **Quota:** default 10 000 units/day; `playlistItems.insert` costs 50 units,
  list/resolve calls cost 1. Fine at personal scale; never re-insert. Details in
  the `youtube-data-api` usage.
- **OAuth onboarding:** one-time Google Cloud setup + interactive consent is the
  largest setup cost; headless/cron runs require a prior interactive run so a
  cached token with a refresh token exists. Details in the `google-oauth2` usage.
- **Eventual consistency:** a freshly published video may take minutes to appear
  in the uploads playlist; an overly frequent schedule can miss the newest until
  the next run.
- **Uploads playlist is bounded** (≈200 most recent videos). Acceptable for
  "new since seeding" semantics, but if a channel publishes more than that
  between two runs, the oldest unseen videos can drop out of the window.
- **Secret hygiene:** `client_secrets.json`, the token file, and `state.json`
  must be excluded from version control and stored with mode `0600`.

## Scope Estimate

**Single task** — small-to-medium. The CLI is cohesive and its internal modules
(config loader, auth/token store, channel resolver, uploads enumerator, new-video
detector, playlist adder, state store, orchestrator) are small and mutually
interdependent, so no decomposition is warranted.

## Existing Architecture

None. `goga schema` is `[]`. `.goga/config.yml` is initialized and the two
project-level usages are created. The initial cell decomposition (likely a sync
orchestrator depending on a Google-auth cell and a local-state cell) will be
designed in the `goga-arch-by-brainstorm` phase; this task deliberately does not
prescribe it.

## Notes

- **Decisions locked during grooming:** Go static binary; OAuth2 desktop
  credentials; seen-ID state with seed-on-first-contact (adds nothing on first
  contact); ID-only config; one-shot CLI.
- **File formats:** YAML for the hand-edited config; JSON for the machine-written
  state (atomic write). Both overridable via flags.
- **Default paths** (all overridable): `./config.yaml`, `./state.json`,
  `./token.json`, `./client_secrets.json`.

### Examples

Sample `config.yaml`:

```yaml
channels:
  - channel_id: UCxxxxxxxxxxxxxxxxxxxxxxxx   # UC… channel ID
    playlist_id: PLyyyyyyyyyyyyyyyyyyyyyyy   # PL… target playlist ID
  - channel_id: UCzzzzzzzzzzzzzzzzzzzzzzzz
    playlist_id: PLwwwwwwwwwwwwwwwwwwwwwwww
```

State file shape (`state.json`) — machine-written, do not hand-edit:

```json
{
  "channels": {
    "UCxxxxxxxxxxxxxxxxxxxxxxxx": {
      "seeded": true,
      "last_sync": "2026-07-27T18:30:00Z",
      "seen_video_ids": ["abc123", "def456"]
    }
  }
}
```

CLI usage:

```bash
# first run: complete OAuth consent in the browser, then seed every channel
./youtube-updater --config config.yaml --secrets client_secrets.json

# normal run: add only new uploads
./youtube-updater

# preview without changes
./youtube-updater --dry-run
```
