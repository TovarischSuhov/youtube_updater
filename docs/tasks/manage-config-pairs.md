# Manage Config Pairs — CLI add/remove/list with names

> Modification to the implemented `youtube-updater` project. Touches `config`,
> `cmd`, and `youtube`; the sync flow is unchanged. Supersedes the earlier
> (ID-only) formulation of this task.

## Current State

`config.yaml` is **hand-edited only** and stores only IDs. The `config` cell
exposes read-only `LoadConfig(path) ([]ChannelMapping, error)` over
`ChannelMapping{ChannelID, PlaylistID}` — no names, no mutation, no write. The
`cmd` cell exposes a single `Run` (sync) behind stdlib `flag`. There is no way to
add, remove, or list channel→playlist pairs except editing the YAML by hand.

## Description

Add CLI flags to manage channel→playlist pairs in `config.yaml`, storing
human-readable names alongside the IDs:

- `--add-channel <id>` + `--add-playlist <id>` — **fetch** the channel name and
  playlist title from the YouTube API, upsert the pair (with names), save, and
  exit. (Requires OAuth like sync; also validates that the channel/playlist exist.)
- `--remove-channel <id>` — drop the pair by channel ID (no-op if absent), save,
  and exit. Offline.
- `--list` — print every pair (channel id+name → playlist id+name) from
  `config.yaml` and exit. Offline.
- Bare invocation (none of the above) runs the **sync** as today.

Mutation is **idempotent**: re-adding a channel updates its playlist **and refreshes
its names**; removing a missing channel is a no-op.

## Scope

**In scope:**
- Extend `ChannelMapping` with `ChannelName` + `PlaylistName`; persist in
  `config.yaml` (`channel_name`, `playlist_name`).
- `config` cell: add mutation + persistence (upsert, remove-by-channel-ID, write).
- `youtube` cell: add name resolution — channel name (`channels.list`
  `snippet.title`) and playlist title (`playlists.list` `snippet.title`).
- `cmd` cell: `--add-channel`, `--add-playlist`, `--remove-channel`, `--list`
  flags + dispatch (mutate/list-then-exit vs sync).
- Validation: `--add-channel` requires `--add-playlist`; `add`/`remove`/`list`
  are mutually exclusive with one another; bare = sync.
- Update the `youtube-data-api` usage to cover `playlists.list` and the
  `channels.list` `snippet` variant.
- Unit tests for mutation/persistence, name resolution (httptest), and dispatch.

**Out of scope:**
- Subcommands (stays flags); manual name flags (`--channel-name`, etc.).
- Backfilling names for pairs created before this feature (future enhancement).
- Editing playlist *contents*; `@handle`/name-as-input resolution; multi-file
  config; concurrency-safe writes.

## Acceptance Criteria

- `--add-channel UCx --add-playlist PLx` fetches both names, stores all four
  fields in `config.yaml`, exits 0 without syncing.
- Re-adding an existing channel with a different playlist updates the playlist and
  refreshes both names.
- `--remove-channel UCx` removes the pair and exits 0; re-running when absent exits
  0 (no-op, no error). Offline — no auth.
- `--list` prints all pairs (id + name) and exits 0; runs without auth.
- Bare run (no add/remove/list flags) syncs exactly as before — no regression.
- `--add-channel` without `--add-playlist` (or vice versa) → non-zero exit, no
  mutation.
- Any two of `add`/`remove`/`list` supplied together → non-zero exit, no mutation.
- `add` against a non-existent channel or inaccessible playlist → non-zero exit
  with a clear error (live validation via the fetch).
- After every write, `config.yaml` is valid YAML and `LoadConfig` reads names.
- All existing tests pass; new behavior is unit-tested.

## Stack

- **Frameworks:** none.
- **Libraries:** stdlib `flag`; `gopkg.in/yaml.v3` (parse + emit); the existing
  `google.golang.org/api/youtube/v3` client (for name resolution).
- **Infrastructure:** none — local `config.yaml` only.

## External Dependencies

| Component | Usage file | Status |
|-----------|------------|--------|
| YouTube Data API v3 | `.goga/usages/cooks/youtube-data-api.md` | **update** — add `playlists.list` (`snippet.title`) and the `channels.list` `snippet` variant for titles |

No new third-party packages.

## Risks and Constraints

- **`add` now requires OAuth** (it fetches names). The first `add` triggers the
  one-time consent flow if no cached token exists; `remove` and `list` stay offline.
- **Playlist accessibility:** `playlists.list` returns the title only for public or
  owned playlists; a private, non-owned playlist makes `add` fail.
- **YAML rewrite loses comments:** `yaml.Marshal` rewrites `config.yaml`; the file
  becomes tool-managed (stop hand-editing).
- **Pre-existing pairs have no names:** pairs added before this feature store no
  names; `--list` shows their IDs only. No auto-backfill (future enhancement).
- **No concurrency safety** on `config.yaml` (same caveat as `state.json`).

## Scope Estimate

**Single task, medium-small.** Three cells (`config`, `youtube`, `cmd`) plus one
usage-file update, tightly coupled around the channel→playlist pair. Implementable
in sequence: youtube name-resolution → config mutation/persistence → cmd flags.

## Existing Architecture

- **`config` cell** — `ChannelMapping` (data) gains `ChannelName`/`PlaylistName`;
  `LoadConfig` (routine) stays signature-stable (no ripple into `syncer`). New
  mutation + persistence is added (exact contract shape decided in brainstorm).
- **`youtube` cell** — add name resolution (channel name + playlist title). Existing
  `ResolveUploads`/`ListUploads`/`AddToPlaylist` unchanged.
- **`cmd` cell** — `Run` + `main` gain the new flags and mutate/list-vs-sync dispatch.
- **`syncer`, `state`** — unchanged; the sync path ignores the new name fields.

## Notes

- Locked decisions: **flags** (not subcommands); **idempotent** semantics;
  **auto-fetch** names during add; `remove`/`list` offline; bare run = sync; no new
  third-party deps.

### CLI examples

```bash
# Add (or update) a pair — fetches names from the API, writes config.yaml, exits
youtube-updater --add-channel UCxxxxxxxx --add-playlist PLyyyyyyyy
#   → stores channel_id, channel_name, playlist_id, playlist_name

# List configured pairs (offline; shows stored names)
youtube-updater --list
#   UCxxxxxxxx  “Some Channel”   →   PLyyyyyyyy  “My Watchlist”
#   UCzzzzzzzz  “Another”        →   PLwwwwwwww  “Tech”

# Remove a pair by channel id (offline)
youtube-updater --remove-channel UCxxxxxxxx

# Override the config file path for any command
youtube-updater --config /path/to/config.yaml --list

# Bare invocation still syncs (unchanged)
youtube-updater
youtube-updater --dry-run
```
