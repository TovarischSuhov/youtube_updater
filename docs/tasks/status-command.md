# Status Command

## Current State

`state.json` already records, per channel: `seeded`, `last_sync`, `last_seen_id`,
`last_seen_at` (see `state.cursor`). `config.yaml` holds the human channel and
playlist names (`config.ChannelMapping`). However **nothing displays this sync
state**:

- `state.State` exposes `IsSeeded`, `LastSeenID`, `LastSeenAt` — but **not**
  `LastSync` (the `cursor.LastSync` field has no accessor).
- Locally, the only way to inspect sync health is to read `state.json` by hand.
- The CI `status.yml` workflow parses `slog` log lines to find failed channels
  precisely because the binary emits no structured status.

`cmd.RunList` (offline, reads config only) is the established pattern for an
offline read-only command.

## Description

Add a read-only, **offline** `--status` command that prints the sync state of
every configured channel by cross-referencing `config.yaml` (names) with
`state.json` (cursors). No YouTube API calls, no auth required. The single code
gap is a `State.LastSync(channelID)` accessor; everything else is wiring that
mirrors `RunList`.

Per channel it shows: channel name + ID, playlist name + ID, `seeded`, last sync
time, and the last-seen video (ID + publish time). A channel that is configured
but never synced shows "not synced yet".

## Scope

**In scope:**
- `state.State.LastSync(channelID string) string` accessor (returns the stored
  RFC 3339 `last_sync`, empty if unseeded).
- `cmd`: a `--status` flag, a `modeStatus` branch, and `RunStatus(configPath,
  statePath string) error` that loads config + state and prints per-channel
  status to stdout.
- `main()` integration: `--status` joins the mutually-exclusive mode set
  (`--add-*`, `--remove-channel`, `--list`) and the command dispatch `switch`.
- Human-readable output: one block per channel; `<unnamed>` placeholders for
  missing names (reuse `nameOrUnnamed`).
- Graceful handling of a missing/empty `state.json` (every channel reads as
  unseeded; no error).
- Unit tests: `State.LastSync`; `RunStatus` output for a seeded channel, an
  unseeded channel, and an absent state file.

**Out of scope:**
- Any YouTube API call — no auth, no video-title resolution, no pending-new-video
  count (a future `--live` mode).
- Changes to the `config` or `state` on-disk schema; `--status` mutates nothing.
- Switching `status.yml` to consume `--status` output (noted as a follow-up).

## Acceptance Criteria

- `--status` exits 0 and prints one entry per configured channel.
- It runs **offline**: works without `client_secrets.json`/`token.json`.
- A seeded channel shows its last sync time and last-seen video ID + publish time.
- A configured-but-unseeded channel shows "not synced yet".
- A missing or empty `state.json` is not an error — all channels show unseeded.
- `--status` is mutually exclusive with `--add-*`/`--remove-channel`/`--list`.
- `go test ./...` passes; `goga lint` passes once the CODEMANIFESTs are updated
  (apply phase).

## Stack

- **Frameworks:** none (plain Go CLI).
- **Libraries:** none new — stdlib (`flag`, `fmt`, `log/slog`) plus the existing
  `config` and `state` packages.
- **Infrastructure:** none.

## External Dependencies

None. The task uses already-integrated packages (`config`, `state`) and stdlib
only; no new usage files are required.

## Risks and Constraints

- **New accessor is trivial** (`state.LastSync` reads an existing field) — low
  risk; covered by a focused test.
- **Output stability:** the format is human-oriented; if `status.yml` later parses
  it, agree on the format before depending on it (out of scope here).
- **Absent state file:** must not error — `NewState` already tolerates a missing
  file; `RunStatus` relies on that.

## Scope Estimate

**Single task** — small. Two source files (`state`, `cmd`) plus tests; no new
cells or inter-cell dependencies. No decomposition warranted.

## Existing Architecture

Five cells (`goga schema`): the task touches two, both widen-only:
- `state` — add the `LastSync` accessor (apply phase: `state/CODEMANIFEST`).
- `cmd` — add `RunStatus`, the `--status` flag, and dispatch wiring (apply phase:
  `cmd/CODEMANIFEST`).

`config` is used read-only (`LoadConfig`); no changes there.

## Notes

- **Decisions locked:** offline-only; human-readable per-channel block; reuse
  `nameOrUnnamed`; absorb into the existing mutual-exclusivity mode set.
- **Follow-up (not this task):** `status.yml` could run `go run ./cmd --status`
  for structured output instead of grepping `slog` lines; and an optional
  `--live` flag could add video titles / pending counts via the API.

### Example output

```
Foo Channel (UCaaaaaaaaaaaaaaaaaaaaaa) → My Playlist (PLbbbbbbbbbbbbbbbbbbbbbb)
  seeded:     true
  last sync:  2026-08-03T06:17:00Z
  last seen:  dQw4w9WgXcQ  (published 2026-08-01T12:00:00Z)

Bar Channel (UCcccccccccccccccccccccc) → <unnamed> (PLdddddddddddddddddddddd)
  seeded:     false
  last sync:  —
  last seen:  —  (not synced yet)
```
