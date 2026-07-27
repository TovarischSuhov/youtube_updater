# Plan: `manage-config-pairs`

## Purpose

Add CLI management of channel→playlist pairs to the implemented `youtube-updater`:
`add` (fetch names, upsert, save), `remove` (offline, idempotent), `list` (offline),
storing human-readable channel/playlist names. After implementation the binary
supports `--add-channel/--add-playlist`, `--remove-channel`, `--list`; bare
invocation still syncs.

**Strategy:** modify 3 existing cells in dependency order — `config` (leaves) →
`youtube` → `cmd`. No new cells, no new module deps. Each task follows TDD
(contract tests → code → verify → logic tests → debug → re-verify → lint).
`CODEMANIFEST` files are **read-only**; the contracts already lead the code.

## Context

### Contract Surface (changed entities only)

**`ChannelMapping`** (cell `config`, `config.go`) — MODIFIED data Entity:
- +`ChannelName -> string` (yaml `channel_name`; empty for legacy pairs)
- +`PlaylistName -> string` (yaml `playlist_name`; empty for legacy pairs)

**`SaveConfig`** (config, `config.go`) — NEW Routine:
- `SaveConfig(path: string, mappings: []ChannelMapping) -> err:error` — marshal via `yaml`; write atomically; output readable by `LoadConfig`.

**`AddMapping`** (config, `config.go`) — NEW Routine:
- `AddMapping(mappings, channelID, playlistID, channelName, playlistName) -> updated:[]ChannelMapping` — upsert (update in place if `channelID` exists, else append); idempotent.

**`RemoveMapping`** (config, `config.go`) — NEW Routine:
- `RemoveMapping(mappings, channelID) -> updated:[]ChannelMapping` — drop by channel id; idempotent no-op if absent.

**`YouTube.ResolveNames`** (cell `youtube`, `youtube.go`) — NEW method on Entity `YouTube`:
- `ResolveNames(channelID, playlistID) -> channelName:string, playlistName:string, err:error` — `channels.list`/`playlists.list` `snippet.title`; fails if not found / playlist private & non-owned.

**`RunAdd`** (cell `cmd`, `main.go`) — NEW Routine:
- `RunAdd(configPath, secretsPath, tokenPath, redirectURL, channelID, playlistID) -> err:error` — `LoadConfig` → `NewYouTube` → `ResolveNames` → `AddMapping` → `SaveConfig`. Requires OAuth.

**`RunRemove`** (cmd, `main.go`) — NEW Routine:
- `RunRemove(configPath, channelID) -> err:error` — `LoadConfig` → `RemoveMapping` → `SaveConfig`. Offline.

**`RunList`** (cmd, `main.go`) — NEW Routine:
- `RunList(configPath) -> err:error` — `LoadConfig` → print pairs (id+name). Offline.

`Run` (sync) unchanged. `main` gains flags + dispatch.

### Re-exports
None.

### Usages Context
- `yaml` (inline, config) — parse + emit the `channels:` list with the four fields.
- `youtube-data-api` (`.goga/usages/cooks/`) — Scenario 4: `channels.list`/`playlists.list` `snippet.title` (1 unit each).
- `google-oauth2` — unchanged; used by `NewYouTube` (which `RunAdd` calls).

### Imported Usages
None (no `Imports.Usages`).

### Local Usages
- `youtube/.usages/facade.md` — Status: **needs supplement** (add `ResolveNames` section). Updated in Task 2.

### External Dependencies
No new packages (all libs already in go.mod).

## Facts
- Module `youtube-updater`; packages `config`, `youtube`, `cmd` already exist with implemented code.
- `config.yaml` is not secret → `SaveConfig` perms `0644` (atomic write: temp + `os.Rename`).
- `youtube.go` already has `withRetry` and `channelNotFoundError` → reuse; add `playlistNotFoundError`.
- `RunList` → stdout (data); `RunAdd`/`RunRemove` → `slog.Info` (status).
- `main` flag defaults: existing sync flags + `--add-channel ""`, `--add-playlist ""`, `--remove-channel ""`, `--list false`.
- All 8 CODEMANIFESTs lint-clean; contracts read-only.

## Gap Analysis
- `config.go`: missing `ChannelName`/`PlaylistName` fields, `SaveConfig`, `AddMapping`, `RemoveMapping`.
- `youtube.go`: missing `ResolveNames` (and `playlistNotFoundError`).
- `main.go`: missing `RunAdd`/`RunRemove`/`RunList` and the add/remove/list flag dispatch.
- `youtube/.usages/facade.md`: missing `ResolveNames` consumer section.
- Existing code to reuse: `configFile` struct + yaml round-trip; `withRetry`; `NewYouTube`.
- `RunAdd` not unit-testable in isolation (constructs `YouTube` via real OAuth) — components tested separately.

---

## Tasks

> **Package ordering rule**: complete each package before the next. Within each coding task, contract tests first (TDD).

### Task 1: `config` — ChannelMapping names + SaveConfig/AddMapping/RemoveMapping

Modify `config/config.go`: add two fields to `ChannelMapping` and implement three routines. Covers `ChannelMapping` (modified), `SaveConfig`, `AddMapping`, `RemoveMapping`.

**Verbatim traces (from design):**
- `SaveConfig(path, mappings)`: marshal `configFile{Channels: mappings}` via `yaml`; atomic write (temp file + `os.Rename`, 0644); → err.
- `AddMapping(mappings, channelID, playlistID, channelName, playlistName)`: if an entry's `ChannelID==channelID` → update its `PlaylistID`/`ChannelName`/`PlaylistName` in place; else append a new `ChannelMapping`; return the slice. Idempotent.
- `RemoveMapping(mappings, channelID)`: build a new slice excluding entries whose `ChannelID==channelID`; return it. No-op if absent.

**Usages relevant to this task:**
- `yaml`: emit with `yaml.Marshal(configFile{Channels: mappings})`; round-trips with `LoadConfig`.

**CRITICAL: `CODEMANIFEST` files — read-only. Fix implementation, never the contract.**

- [ ] **STEP 0 — Declaration**: covers `config.ChannelMapping` (+fields), `config.SaveConfig`, `config.AddMapping`, `config.RemoveMapping` at `config/config.go`.
- [ ] **Contract tests (STEP 1)**: in `config/config_test.go` — `ChannelMapping` has fields `ChannelName`,`PlaylistName` (string); `SaveConfig(path string, mappings []ChannelMapping) error`; `AddMapping(mappings []ChannelMapping, channelID, playlistID, channelName, playlistName string) []ChannelMapping`; `RemoveMapping(mappings []ChannelMapping, channelID string) []ChannelMapping`. (Expected to fail.)
- [ ] **Code (STEP 2)**: add `ChannelName`/`PlaylistName` (yaml tags `channel_name`/`playlist_name`); implement `SaveConfig` (atomic, 0644), `AddMapping` (upsert), `RemoveMapping` (filter) per traces.
- [ ] **Interface verification (STEP 3)**: `go test ./config/...` — contract tests pass.
- [ ] **Logic tests (STEP 4)**: `AddMapping_AddsNewPair`; `AddMapping_UpsertsExisting` (updates playlist+names); `RemoveMapping_NoOpIfAbsent`; `SaveConfig_RoundTripsNames` (write→LoadConfig reads 4 fields); `SaveConfig_EmptyMappings_WritesValidYAML`.
- [ ] **Debugging (STEP 5)**: `go test ./config/...` — fix code (not tests) until all pass.
- [ ] **Contract re-verification (STEP 6)**: signatures match CODEMANIFEST; existing `LoadConfig`/`ChannelMapping{ChannelID,PlaylistID}` usages still compile (additive).
- [ ] **Lint (STEP 7)**: `gofmt -l .`; `go vet ./config/...`; `goga lint`.
- [ ] **Completion (STEP 8)**: mark → review → approval → next.

### Task 2: `youtube` — YouTube.ResolveNames + facade.md supplement

Modify `youtube/youtube.go`: add `ResolveNames` method (+ `playlistNotFoundError`). Covers `YouTube.ResolveNames`. Also supplement `youtube/.usages/facade.md`.

**Verbatim trace (from design):**
- `ResolveNames(channelID, playlistID)`: channel name via `svc.Channels.List(["snippet"]).Id(channelID).Do()` wrapped in `withRetry` — 0 items → `channelNotFoundError`, else `Items[0].Snippet.Title`; playlist title via `svc.Playlists.List(["snippet"]).Id(playlistID).Do()` wrapped in `withRetry` — 0 items → `playlistNotFoundError`, else `Items[0].Snippet.Title`; return `(channelName, playlistName, err)`.

**Usages relevant to this task:**
- `youtube-data-api` (Scenario 4): `channels.list`/`playlists.list` `part=snippet` → `Snippet.Title`; playlist must be public/owned.

**CRITICAL: `CODEMANIFEST` files — read-only.**

- [ ] **STEP 0 — Declaration**: covers `youtube.YouTube.ResolveNames` at `youtube/youtube.go`; supplements `youtube/.usages/facade.md`.
- [ ] **Contract tests (STEP 1)**: in `youtube/youtube_test.go` — `YouTube` has method `ResolveNames(channelID, playlistID string) (string, string, error)`. (Expected to fail.)
- [ ] **Code (STEP 2)**: add `playlistNotFoundError`; implement `ResolveNames` (two `withRetry`-wrapped calls per trace).
- [ ] **Interface verification (STEP 3)**: `go test ./youtube/...` — contract tests pass.
- [ ] **Logic tests (STEP 4)** (via `httptest` + `newWithService`): `ResolveNames_ReturnsBothNames`; `ResolveNames_ChannelNotFound` (channels returns empty → err); `ResolveNames_PlaylistNotFound` (playlists returns empty → err).
- [ ] **Debugging (STEP 5)**: `go test ./youtube/...` until green.
- [ ] **Contract re-verification (STEP 6)**: method signature matches; no pointer/HTTP type leaks; existing methods unchanged.
- [ ] **Docs**: supplement `youtube/.usages/facade.md` with a `ResolveNames` section (construct once, call `yt.ResolveNames(channelID, playlistID)` → names; fails if not found / playlist private & non-owned). Same domain → extend the existing file.
- [ ] **Lint (STEP 7)**: `gofmt -l .`; `go vet ./youtube/...`; `goga lint`.
- [ ] **Completion (STEP 8)**: mark → review → approval → next.

### Task 3: `cmd` — RunAdd/RunRemove/RunList + main dispatch

Modify `cmd/main.go`: add `RunAdd`, `RunRemove`, `RunList` and extend `main` flag dispatch. Covers `RunAdd`, `RunRemove`, `RunList`. `Run` (sync) unchanged.

**Verbatim traces (from design):**
- `RunAdd(configPath, secretsPath, tokenPath, redirectURL, channelID, playlistID)`: `mappings=LoadConfig`; `yt=NewYouTube`; `channelName,playlistName=yt.ResolveNames`; `mappings=AddMapping(...)`; `SaveConfig`; log. (Requires OAuth; fails if channel/playlist not found.)
- `RunRemove(configPath, channelID)`: `mappings=LoadConfig`; `mappings=RemoveMapping(...)`; `SaveConfig`; log. (Offline; no-op if absent.)
- `RunList(configPath)`: `mappings=LoadConfig`; print each to stdout (channel id+name → playlist id+name; `<unnamed>` when name empty). (Offline.)
- `main` dispatch: count active modes (add/remove/list) — if >1 → error "add/remove/list are mutually exclusive"; switch — `list`→RunList, `remove`→RunRemove, `add` (requires both `--add-channel` and `--add-playlist`, else error)→RunAdd, default→Run (sync).

**Usages relevant to this task:** none new (composition; delegates to `config` + `youtube`).

**CRITICAL: `CODEMANIFEST` files — read-only.**

- [ ] **STEP 0 — Declaration**: covers `cmd.RunAdd`, `cmd.RunRemove`, `cmd.RunList` at `cmd/main.go`.
- [ ] **Contract tests (STEP 1)**: in `cmd/main_test.go` — `RunAdd(configPath, secretsPath, tokenPath, redirectURL, channelID, playlistID string) error`; `RunRemove(configPath, channelID string) error`; `RunList(configPath string) error`. (Expected to fail.)
- [ ] **Code (STEP 2)**: implement `RunList` (stdout table), `RunRemove` (offline), `RunAdd` (auth flow per trace); extend `main` with `--add-channel`/`--add-playlist`/`--remove-channel`/`--list` flags and the dispatch (mutual exclusivity + paired add); keep existing sync flags + `Run`.
- [ ] **Interface verification (STEP 3)**: `go test ./cmd/...` — contract tests pass.
- [ ] **Logic tests (STEP 4)**: `RunList_PrintsPairs` (capture stdout; temp config with names); `RunList_ShowsUnnamedForLegacyPairs`; `RunRemove_RemovesPairAndSaves` (temp config, remove, reload). (`RunAdd` not unit-tested — constructs `YouTube` via OAuth; covered by `ResolveNames`+`AddMapping` component tests.)
- [ ] **Debugging (STEP 5)**: `go test ./cmd/...` until green.
- [ ] **Contract re-verification (STEP 6)**: signatures match; `Run` unchanged; bare invocation still syncs.
- [ ] **Lint (STEP 7)**: `gofmt -l .`; `go vet ./cmd/...`; `goga lint`.
- [ ] **Completion (STEP 8)**: mark → review → approval → next.

---

## Validation Commands

- `go build ./...`: all packages compile
- `go test ./...`: run all tests
- `go vet ./...`: vet
- `gofmt -l .`: formatting (must output nothing)
- `goga lint`: contract lint — must remain `cells: 5 errors: 0` (CODEMANIFEST untouched)

---

## Completion Criteria

- [ ] `ChannelMapping` has `ChannelName`/`PlaylistName`; `SaveConfig`/`AddMapping`/`RemoveMapping` implemented in `config.go`
- [ ] `YouTube.ResolveNames` implemented in `youtube.go`; `facade.md` supplemented
- [ ] `RunAdd`/`RunRemove`/`RunList` implemented in `main.go`; `main` dispatches with mutual-exclusivity + paired-add validation
- [ ] `Run` (sync) unchanged; bare invocation still syncs (no regression — existing tests pass)
- [ ] Every coding task followed the TDD workflow
- [ ] All 11 design test scenarios are covered across tasks
- [ ] No new cells/packages/dependencies; no `CODEMANIFEST` modified
- [ ] All validation commands pass (`go build`, `go test`, `go vet`, `gofmt`, `goga lint`)
- [ ] Every Usages entry (`yaml`, `youtube-data-api`) is referenced in at least one task
