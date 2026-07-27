# Design Document: `manage-config-pairs`

Modification to the implemented `youtube-updater`. Adds CLI management of
channel→playlist pairs (add/remove/list) with auto-fetched names. Unchanged
behavior (sync) is covered by the first design doc
(`docs/design/youtube-playlist-sync.md`); this document covers **only the changes**.

Contracts are on disk, lint-clean, review-passed. This document specifies how to
bring the implementation up to the contracts.

---

## Contract Changes

### Changed CODEMANIFEST Files
- `config/CODEMANIFEST` — `ChannelMapping` +2 fields; +`SaveConfig`, `AddMapping`, `RemoveMapping`; `yaml` usage updated.
- `youtube/CODEMANIFEST` — `YouTube` +method `ResolveNames`; `youtube-data-api` usage +Scenario 4.
- `cmd/CODEMANIFEST` — +`RunAdd`, `RunRemove`, `RunList`; `config` import expanded.

### New Entities
- `SaveConfig` — config.go — persist mappings to YAML.
- `AddMapping` — config.go — upsert a pair (with names).
- `RemoveMapping` — config.go — drop a pair by channel ID (idempotent).
- `YouTube.ResolveNames` — youtube.go — resolve channel + playlist titles.
- `RunAdd` — main.go — add flow (auth: fetch names, upsert, save).
- `RunRemove` — main.go — remove flow (offline).
- `RunList` — main.go — list flow (offline).

### Changed Entities
- `ChannelMapping` — +`ChannelName`, +`PlaylistName` (yaml tags `channel_name`, `playlist_name`).

### Deleted Entities
None.

### Usages and Annotations Changes
- `yaml` (config, inline) — items now carry `channel_name`/`playlist_name`; "Emit writes the list back in the same shape."
- `youtube-data-api` (`.goga/usages/cooks/`) — +Scenario 4 (channels.list/​playlists.list `snippet.title`).

---

## Applied Fixes

### Fixed CODEMANIFEST Defects
None during design tracing (contracts already passed the arch review). Two review
fixes are the baseline:
- `SaveConfig` annotation generalized to "Write atomically so readers never observe a partial file" (no mechanism).
- `AddMapping`/`RemoveMapping` output label `mappings` → `updated` (no input/output name collision).

---

## Entity Interaction and Data Flow

### Interaction Diagram (change-relevant)
```
                         config.yaml
                             │
            LoadConfig ◄─────┴───── (existing, unchanged)
                │
   ┌────────────┼─────────────────────────┐
   ▼            ▼                         ▼
 RunList    RunRemove                   RunAdd
 (print)    │                           │ LoadConfig
            │ RemoveMapping             │ NewYouTube ─► YouTube.ResolveNames(ch,pl) → (chName, plName)
            │ SaveConfig                │ AddMapping(mappings, ch, pl, chName, plName)
            ▼                           │ SaveConfig
         config.yaml                    ▼
                                     config.yaml
```

### Data Flows
1. **Add:** `LoadConfig` → `[]ChannelMapping`; `NewYouTube` → `YouTube`; `ResolveNames(ch,pl)` → `(chName, plName)`; `AddMapping(...)` → updated `[]ChannelMapping`; `SaveConfig` → `config.yaml`.
2. **Remove:** `LoadConfig` → `[]ChannelMapping`; `RemoveMapping(ch)` → updated; `SaveConfig` → `config.yaml`.
3. **List:** `LoadConfig` → `[]ChannelMapping`; print each (id+name).
4. **Sync:** unchanged.

### Entity Dependencies
- `cmd` already imports `config` (`LoadConfig`, +`AddMapping`/`SaveConfig`/`RemoveMapping`) and `youtube` (`YouTube`); no new cell edges.
- `ResolveNames` uses `youtube-data-api`. `SaveConfig`/`AddMapping`/`RemoveMapping` use `yaml`.

---

## Code Stack Trace

### Trace: `SaveConfig`
1. **Input:** `path`, `mappings`.
2. Marshal `configFile{Channels: mappings}` via `yaml.Marshal` (or `MarshalIndent`). → checkpoint: valid YAML.
3. Atomic write: temp file in `filepath.Dir(path)`, write bytes, `os.Rename` over `path`. → checkpoint: readers never see a partial file.
4. **Output:** `err`.

### Trace: `AddMapping`
1. **Input:** `mappings`, `channelID`, `playlistID`, `channelName`, `playlistName`.
2. Scan `mappings` for an entry whose `ChannelID == channelID`.
3. If found → update its `PlaylistID`, `ChannelName`, `PlaylistName` in place. Else → append a new `ChannelMapping`.
4. **Output:** the (possibly same) `[]ChannelMapping`. No error.

### Trace: `RemoveMapping`
1. **Input:** `mappings`, `channelID`.
2. Filter out any entry whose `ChannelID == channelID` (build a new slice).
3. **Output:** the filtered `[]ChannelMapping`. No error (no-op if absent).

### Trace: `YouTube.ResolveNames`
1. **Input:** `channelID`, `playlistID`.
2. Channel name: `svc.Channels.List(["snippet"]).Id(channelID).Do()` (wrapped in `withRetry`); if 0 items → `channelNotFoundError`; else `Items[0].Snippet.Title`.
3. Playlist title: `svc.Playlists.List(["snippet"]).Id(playlistID).Do()` (wrapped in `withRetry`); if 0 items → `playlistNotFoundError`; else `Items[0].Snippet.Title`.
4. **Output:** `(channelName, playlistName, err)`. → checkpoint: per `youtube-data-api` Scenario 4; playlist must be public/owned.

### Trace: `RunAdd`
1. **Input:** `configPath`, `secretsPath`, `tokenPath`, `redirectURL`, `channelID`, `playlistID`.
2. `mappings = LoadConfig(configPath)`.
3. `yt = NewYouTube(secretsPath, tokenPath, redirectURL)` (OAuth).
4. `channelName, playlistName = yt.ResolveNames(channelID, playlistID)`.
5. `mappings = AddMapping(mappings, channelID, playlistID, channelName, playlistName)`.
6. `SaveConfig(configPath, mappings)`.
7. Log confirmation. **Output:** `err`.

### Trace: `RunRemove`
1. **Input:** `configPath`, `channelID`.
2. `mappings = LoadConfig(configPath)`.
3. `mappings = RemoveMapping(mappings, channelID)`.
4. `SaveConfig(configPath, mappings)`.
5. Log confirmation. **Output:** `err`.

### Trace: `RunList`
1. **Input:** `configPath`.
2. `mappings = LoadConfig(configPath)`.
3. For each mapping: print to stdout — `channelID`, `channelName` (or `<unnamed>`), `→`, `playlistID`, `playlistName` (or `<unnamed>`).
4. **Output:** `err` (only on load failure).

### Checkpoint Summary
Type flow passes: `LoadConfig`→`[]ChannelMapping` feeds `AddMapping`/`RemoveMapping`/`SaveConfig`; `ResolveNames`→`(string,string)` feeds `AddMapping`'s name params. No mutation/embedding. No defects.

---

## Algorithm Design

### `ChannelMapping` (modified data)
Add fields:
```
ChannelName  string  (yaml:"channel_name")   // "" for legacy pairs
PlaylistName string  (yaml:"playlist_name")  // "" for legacy pairs
```

### `SaveConfig`
**Algorithm:**
```
1. b = yaml.Marshal(configFile{Channels: mappings})
2. atomicWrite(path, b)   # temp file + os.Rename; perms 0644 (not secret)
   → err on write failure
```
**Errors:** marshal/write failure.
**Edge cases:** empty `mappings` → writes `channels: []` (valid, LoadConfig returns empty).

### `AddMapping`
**Algorithm:**
```
1. for each m in mappings:
     if m.ChannelID == channelID:
        m.PlaylistID = playlistID; m.ChannelName = channelName; m.PlaylistName = playlistName
        return mappings
2. append ChannelMapping{channelID, playlistID, channelName, playlistName}
3. return mappings
```
**Edge cases:** re-add updates in place (idempotent).

### `RemoveMapping`
**Algorithm:**
```
1. out = []
2. for each m in mappings: if m.ChannelID != channelID: append to out
3. return out
```
**Edge cases:** absent channel → returns the same set unchanged (no-op, no error).

### `YouTube.ResolveNames`
**Algorithm:** per trace. Reuses existing `withRetry`. Adds a `playlistNotFoundError` (analogous to `channelNotFoundError`).
**Errors:** channel/playlist not found; transient API errors retried.
**Edge cases:** private non-owned playlist → `playlists.list` returns 0 items → `playlistNotFoundError`.

### `RunAdd` / `RunRemove` / `RunList`
Per traces. `RunList` writes the table to **stdout** (data output); `RunAdd`/`RunRemove` log a one-line confirmation via `slog` (status) — matching `Run`'s logging style.

### `main` dispatch (modified)
```
1. define flags: existing sync flags + --add-channel, --add-playlist, --remove-channel, --list
2. count active modes:
     modeAdd = addChannel != "" || addPlaylist != ""
     modeRemove = removeChannel != ""
     modeList = list
   if (modeAdd + modeRemove + modeList as ints) > 1 → error "add/remove/list are mutually exclusive"
3. switch:
   case modeList:        RunList(configPath)
   case modeRemove:      RunRemove(configPath, removeChannel)
   case modeAdd:
       if addChannel == "" || addPlaylist == "" → error "--add-channel and --add-playlist are required together"
       RunAdd(configPath, secretsPath, tokenPath, redirectURL, addChannel, addPlaylist)
   default:              Run(configPath, secretsPath, tokenPath, statePath, redirectURL, dryRun)   # sync
4. on err → stderr + exit 1
```

---

## Cross-cutting Concerns

- **Error handling:** Go `(result, error)` throughout. `RunAdd`/`RunRemove`/`RunList` return `err`; `main` maps non-nil to exit 1. API errors (auth, not-found) propagate from `ResolveNames`; idempotent config ops never error on duplicate/absent.
- **Logging:** `RunAdd`/`RunRemove` use `slog.Info` (status, stderr) like the sync. `RunList` writes the pair table to **stdout** (user data). Mutual-exclusivity / paired-flag errors go to stderr.
- **Validation:** `main` enforces mutual exclusivity and paired `--add-*`. `ResolveNames` validates existence live (fetch fails if not found). No ID format validation (trusts input, consistent with `LoadConfig`).
- **Caching:** none new. (OAuth token cache unchanged.)
- **Concurrency:** none — sequential, single process. `config.yaml` is not concurrency-safe (documented; same caveat as `state.json`).

---

## Usages Analysis

### `youtube-data-api` (file)
- **What it provides:** YouTube Data API v3 patterns incl. the new name-resolution scenario.
- **Where used:** `youtube` cell — `ResolveNames` (and the existing methods).
- **Why chosen:** official client; only source for channel/playlist titles.
- **How exactly:** `Channels.List(["snippet"]).Id(...)` → `Snippet.Title`; `Playlists.List(["snippet"]).Id(...)` → `Snippet.Title`. (Scenario 4.)

### `yaml` (inline, config)
- **What:** parse + emit the `channels:` list with the four fields.
- **Where:** `LoadConfig` (parse), `SaveConfig` (emit).
- **How:** `yaml.Unmarshal`/`yaml.Marshal` of `configFile{Channels: []}`.

### `google-oauth2` (file)
Unchanged; still used by `NewYouTube` (which `RunAdd` calls).

### Imported Usages
None (no `Imports.Usages`).

---

## `.usages/` Update

### Cell: `youtube`
- **`facade.md`** → `youtube/.usages/facade.md` — Status: **needs supplement**. It currently documents `NewYouTube`, `ResolveUploads`, `ListUploads`, `AddToPlaylist`. **Add** a `ResolveNames` section (construct once, call `yt.ResolveNames(channelID, playlistID)` → names; fails if not found / playlist private & non-owned). Same functional domain (facade consumption) → supplement the existing file, not a new one.

### Cells `config`, `cmd`
No `.usages/` directory — none required (config routines are self-describing; cmd is the root).

---

## Test Stack Trace

### General Setup
- `config` tests: temp dir + `config.yaml`; pure-logic.
- `youtube` tests: `httptest` server returning canned `channels`/`playlists` JSON; facade via `NewWithService`.
- `cmd` tests: temp `config.yaml`; capture stdout for `RunList`.

### Source File Registry
`config/config.go` (+`config_test.go`), `youtube/youtube.go` (+`youtube_test.go`), `cmd/main.go` (+`main_test.go`).

---

### Positive Tests

#### `AddMapping_AddsNewPair`
**Setup:** `mappings = []`. **Input:** `AddMapping([], "UCa", "PLa", "Foo", "Bar")`.
**Trace:** no match → append `ChannelMapping{UCa, PLa, Foo, Bar}`.
**Assertions:** len==1; entry fields == `{UCa, PLa, Foo, Bar}`.
**Sufficiency:** basic append + name storage.

#### `AddMapping_UpsertsExisting`
**Setup:** `mappings = [{UCa, PLa, OldCh, OldPl}]`. **Input:** `AddMapping(m, "UCa", "PLb", "NewCh", "NewPl")`.
**Trace:** match → update in place.
**Assertions:** len==1; entry == `{UCa, PLb, NewCh, NewPl}`.
**Sufficiency:** idempotent upsert updates playlist + names.

#### `SaveConfig_RoundTripsNames`
**Setup:** temp `config.yaml`. **Input:** `SaveConfig(path, [{UCa, PLa, Foo, Bar}])` then `LoadConfig(path)`.
**Assertions:** 1 mapping with all four fields.
**Sufficiency:** write+read preserves names; valid YAML.

#### `ResolveNames_ReturnsBothNames` (httptest)
**Setup:** server returns `channels` (`snippet.title:"Foo"`) and `playlists` (`snippet.title:"Bar"`).
**Input:** `yt.ResolveNames("UCa", "PLa")`.
**Assertions:** `channelName=="Foo"`, `playlistName=="Bar"`, err nil.
**Sufficiency:** both API calls parse titles.

#### `RunList_PrintsPairs`
**Setup:** temp `config.yaml` with `[{UCa, PLa, Foo, Bar}]`; capture stdout.
**Input:** `RunList(path)`.
**Assertions:** stdout contains `UCa`, `Foo`, `PLa`, `Bar`; err nil.
**Sufficiency:** offline list output.

#### `RunRemove_RemovesPairAndSaves`
**Setup:** temp `config.yaml` with two pairs.
**Input:** `RunRemove(path, "UCa")`.
**Assertions:** `LoadConfig` after → 1 pair (`UCb`); err nil.
**Sufficiency:** offline remove + persist.

---

### Negative Tests

#### `ResolveNames_ChannelNotFound` (httptest)
**Setup:** `channels` returns `{"items":[]}`. **Input:** `ResolveNames("UCx","PLa")`.
**Assertions:** err non-nil; message references channel. **Sufficiency:** live validation on add.

#### `ResolveNames_PlaylistNotFound` (httptest)
**Setup:** `playlists` returns `{"items":[]}` (channel ok).
**Assertions:** err non-nil; message references playlist. **Sufficiency:** private/non-owned playlist → add fails.

---

### Edge Case Tests

#### `RemoveMapping_NoOpIfAbsent`
**Input:** `RemoveMapping([{UCa,PLa,Foo,Bar}], "UCz")`. **Assertions:** len==1 unchanged; no error.
**Sufficiency:** idempotent remove.

#### `RunList_ShowsUnamedForLegacyPairs`
**Setup:** pair with empty names. **Assertions:** stdout shows the IDs and `<unnamed>` placeholders.
**Sufficiency:** pre-existing pairs (no names) render sensibly.

#### `SaveConfig_EmptyMappings_WritesValidYAML`
**Input:** `SaveConfig(path, [])` then `LoadConfig`. **Assertions:** 0 mappings, err nil.
**Sufficiency:** removing the last pair leaves a valid (empty) config.

> `RunAdd` is not unit-tested in isolation (it constructs `YouTube` via real OAuth, like `Run`). Its components are covered: `ResolveNames` (httptest) and `AddMapping` (unit). Flag dispatch (mutual exclusivity, paired add) is covered by `main` behavior.

---

## Additional Instructions for the Implementation Agent

- `ChannelMapping`: add the two fields with `yaml:"channel_name"` / `yaml:"playlist_name"` tags; existing tests using `ChannelMapping{ChannelID, PlaylistID}` stay valid (positional/zero names).
- `SaveConfig`: reuse the atomic-write pattern (temp + `os.Rename`); perms `0644` (config is not secret — unlike `token.json`/`state.json`).
- `ResolveNames`: wrap each API call in the existing `withRetry`; add a `playlistNotFoundError` analogous to `channelNotFoundError`.
- `RunList` → stdout (data); `RunAdd`/`RunRemove` → `slog.Info` (status); keep `Run`'s logging unchanged.
- `main`: keep all existing flags; add the four new ones; enforce mutual exclusivity and paired `--add-*` before dispatch; bare invocation → sync (unchanged).
- Do not modify `CODEMANIFEST` files (read-only); if implementation diverges, fix the code.
