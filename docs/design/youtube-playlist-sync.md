# Design Document: `youtube-playlist-sync`

Complete architectural specification for the greenfield Go CLI. Derived from the
reviewed CODEMANIFESTs. Specifies **what** and **how** to implement — no source
code; algorithms are pseudocode. Implementation order is handled at planning time.

**Module:** `youtube-updater` (go.mod at repo root). Packages mirror cell paths:
`config`, `state`, `youtube`, `syncer`, `cmd` (package `main`).

**Dependencies (go.mod):**
`google.golang.org/api/youtube/v3`, `golang.org/x/oauth2`,
`golang.org/x/oauth2/google`, `google.golang.org/api/option`,
`gopkg.in/yaml.v3`. Stdlib only otherwise (`encoding/json`, `flag`, `log/slog`,
`net/http`, `os`, `time`, `fmt`).

---

## Contract Changes

### Changed CODEMANIFEST Files
All cells are **new** (greenfield; not a git repo, so the whole set is the change set):
- `config/CODEMANIFEST` — new. ChannelMapping (data) + LoadConfig (routine).
- `state/CODEMANIFEST` — new. State (cursor store).
- `youtube/CODEMANIFEST` — new. Video (data) + YouTube (facade).
- `syncer/CODEMANIFEST` — new. ChannelResult (data) + SyncAll (routine).
- `cmd/CODEMANIFEST` — new. Run (routine).

### New Entities
- `ChannelMapping` — config.go — one channel→playlist pair (data).
- `LoadConfig` — config.go — parse YAML config → []ChannelMapping.
- `State` — state.go — persisted per-channel last-seen-video cursor.
- `Video` — youtube.go — {ID, PublishedAt}.
- `YouTube` — youtube.go — facade: OAuth2 + YouTube Data API v3.
- `ChannelResult` — syncer.go — per-channel sync outcome (data).
- `SyncAll` — syncer.go — orchestrate sync across mappings.
- `Run` — main.go — CLI entry / composition root.

### Changed / Deleted Entities
None.

### Usages and Annotations Changes
New project usages `youtube-data-api`, `google-oauth2` (`.goga/usages/cooks/`)
apply to the `youtube` cell. Inline usages `yaml` (config) and `json` (state).

---

## Applied Fixes

### Fixed CODEMANIFEST Defects
No defects found during design-phase tracing. The two fixes applied earlier (arch
review) are reflected in the current contracts and are the design baseline:
- `syncer` SyncAll — empty-uploads guard (skip channel, no cursor) — High.
- `cmd` Run / `state` Save — annotations generalized (no impl detail) — Medium.

**Note on constructor errors:** `State(path)` and `YouTube(secretsPath,…)` can fail
(file corrupt / OAuth failure). The DSL entity signature models construction
*input* only (returns are implicit), so the error return is an implementation
detail: factories are `NewState(path) (*State, error)` and
`NewYouTube(secrets,token,redirect) (*YouTube, error)`. `Run` checks both errors.
No signature change required.

---

## Entity Interaction and Data Flow

### Interaction Diagram
```
                       config.yaml
                           │ LoadConfig
                           ▼
                      []ChannelMapping ──────────────┐
                                                     │
  client_secrets.json ─┐                            │
  token.json ──────────┴─► YouTube ◄──── NewYouTube │
                            │  ▲                     │
                   ResolveUploads/ListUploads/       │
                   AddToPlaylist                     │
                            │                        ▼
                            └────► SyncAll(youTube, State, mappings, dryRun)
                                       │ reads/writes        │ returns
                                       ▼                     ▼
                                    State ◄──── NewState   []ChannelResult
                                       ▲ Save                  │
                            state.json │                       ▼
                                       │                    Run → log + exit
```

### Data Flows
1. **Config flow:** `config.yaml` → `LoadConfig` → `[]ChannelMapping` → passed into `SyncAll` (and the playlist IDs feed `AddToPlaylist`).
2. **Video flow:** `YouTube.ResolveUploads(channelID)` → `uploadsPlaylistID` → `YouTube.ListUploads` → `[]Video` (newest-first) → watermark selection in `SyncAll` → `YouTube.AddToPlaylist(playlistID, videoID)` per new video.
3. **Cursor flow:** `State.LastSeenAt(channelID)` (watermark) → compared against `Video.PublishedAt`; on advance, `State.SetLastSeen(channelID, newestID, newestPublishedAt)`; `State.Save()` persists to `state.json`.

### Entity Dependencies
- Leaves (no deps): `config`, `state`, `youtube`.
- `syncer` → `config.ChannelMapping`, `state.State`, `youtube.{YouTube,Video}`.
- `cmd` → `config.LoadConfig`, `state.State`, `youtube.YouTube`, `syncer.{SyncAll,ChannelResult}`.
- Initialization order in `Run`: `LoadConfig` → `NewYouTube` → `NewState` → `SyncAll` → `State.Save`.

---

## Code Stack Trace

### Trace: `LoadConfig`
1. **Input:** `path` (string) to a YAML file.
2. Read file bytes at `path`. → checkpoint: read error propagates as `err`.
3. Unmarshal via `yaml.v3` into wrapper `{ channels: [{channel_id, playlist_id}] }`. → checkpoint: parse error propagates as `err`; mapping uses yaml tags `channel_id`→`ChannelID`, `playlist_id`→`PlaylistID`.
4. **Output:** `mappings []ChannelMapping` (empty list if file empty — not an error).

### Trace: `State` (construction) / `NewState`
1. **Input:** `path`.
2. `os.ReadFile(path)`. If `os.IsNotExist` OR len==0 → start with empty channel map (no error). → checkpoint: missing/empty ⇒ empty.
3. Else `json.Unmarshal` into `map[string]cursorState`. → checkpoint: corrupt JSON ⇒ `err`.
4. **Output:** `*State` holding `path` + channel map.

### Trace: `State.SetLastSeen`
1. **Input:** `channelID, videoID, publishedAt`.
2. Validate `publishedAt` non-empty and parseable as RFC3339 (`time.Parse(time.RFC3339,…)`). → checkpoint: invalid ⇒ `err`.
3. Store `{Seeded:true, LastSeenID:videoID, LastSeenAt:publishedAt}` for `channelID`; set `LastSync` to now.
4. **Output:** `err` (nil on success). State mutated in memory (not yet persisted).

### Trace: `State.Save`
1. **Input:** none (uses stored `path`).
2. `json.MarshalIndent` the channel map.
3. Write to a temp file in the same directory as `path`, `os.Chmod 0600`, then `os.Rename` over `path`. → checkpoint: atomic — readers/crash never see a partial file.
4. **Output:** `err`.

### Trace: `YouTube` (construction) / `NewYouTube`
1. **Input:** `secretsPath, tokenPath, redirectURL`.
2. Read `secretsPath`; `google.ConfigFromJSON(b, youtubeScope)` → `oauth2.Config`; set `RedirectURL=redirectURL`. → checkpoint: missing/invalid secrets ⇒ `err`.
3. Token: `loadToken(tokenPath)`; if absent/invalid → run consent flow (`authorize`: local HTTP callback on `redirectURL`, `AccessTypeOffline`+`ApprovalForce`), persist via `saveToken` (0600). → checkpoint: consent denied / exchange fail ⇒ `err`.
4. Build `*http.Client` via a token source that persists refreshed tokens (`savingTokenSource`, per `google-oauth2` usage).
5. `youtube.NewService(ctx, option.WithHTTPClient(client))`. → checkpoint: service build fail ⇒ `err`.
6. **Output:** `*YouTube` wrapping the service.

### Trace: `YouTube.ResolveUploads`
1. **Input:** `channelID`.
2. `service.Channels.List(["contentDetails"]).Id(channelID).Do()`. → checkpoint: API error ⇒ `err`.
3. If `len(Items)==0` ⇒ `err` ("channel not found").
4. **Output:** `uploadsPlaylistID = Items[0].ContentDetails.RelatedPlaylists.Uploads`.

### Trace: `YouTube.ListUploads`
1. **Input:** `uploadsPlaylistID`.
2. `service.PlaylistItems.List(["snippet","contentDetails"]).PlaylistId(id).MaxResults(50).Pages(ctx, fn)` — collect `Video{ID: item.ContentDetails.VideoId, PublishedAt: item.ContentDetails.VideoPublishedAt}`, skipping items with empty VideoId. → checkpoint: API error ⇒ `err`; pagination handled by `.Pages`.
3. **Output:** `videos []Video`, newest-first (uploads playlist ordering).

### Trace: `YouTube.AddToPlaylist`
1. **Input:** `playlistID, videoID`.
2. `service.PlaylistItems.Insert(["snippet"], &PlaylistItem{Snippet:{PlaylistId, ResourceId:{Kind:"youtube#video", VideoId:videoID}}}).Do()`. → checkpoint: API error (incl. quota/rate-limit) ⇒ `err`.
3. **Output:** `itemID = created.Id`.

### Trace: `SyncAll`
1. **Input:** `youTube, state, mappings, dryRun`.
2. For each `ChannelMapping m` in `mappings`:
   1. `uploadsID, err = youTube.ResolveUploads(m.ChannelID)`; on err → `ChannelResult{Err}`; continue.
   2. `videos, err = youTube.ListUploads(uploadsID)`; on err → `ChannelResult{Err}`; continue.
   3. If `len(videos)==0` → `ChannelResult{NewCount:0}`; continue (no cursor set). → checkpoint: empty guard.
   4. `newest = videos[0]`.
   5. If `!state.IsSeeded(m.ChannelID)`:
      - result `{Seeded:true, NewCount:0}`; if `!dryRun`: `state.SetLastSeen(m.ChannelID, newest.ID, newest.PublishedAt)`.
   6. Else:
      - `mark = state.LastSeenAt(m.ChannelID)`; `isNew = selectNew(videos, mark)` (those with parsed time after mark).
      - if `!dryRun`: for each `v` in `reverse(isNew)` (oldest→newest): `youTube.AddToPlaylist(m.PlaylistID, v.ID)`.
      - if `!dryRun`: `state.SetLastSeen(m.ChannelID, newest.ID, newest.PublishedAt)`.
      - result `{NewCount:len(isNew), AddedIDs:IDs(isNew)}`.
3. **Output:** `results []ChannelResult` (one per mapping); `err` only on fatal failure.

### Trace: `Run`
1. **Input:** `configPath, secretsPath, tokenPath, statePath, redirectURL, dryRun`.
2. `mappings, err = LoadConfig(configPath)`; on err → return `err`.
3. `youTube, err = NewYouTube(secretsPath, tokenPath, redirectURL)`; on err → return `err`.
4. `state, err = NewState(statePath)`; on err → return `err`.
5. `results, err = SyncAll(youTube, state, mappings, dryRun)`; log each `ChannelResult` (slog.Info); on fatal err → return `err`.
6. If `!dryRun`: `state.Save()`.
7. **Output:** `err` (nil → exit 0; non-nil → exit 1). `func main` parses flags, calls `Run`, maps `err` to exit code.

### Checkpoint Summary
All type-flow checkpoints pass: `LoadConfig`→`[]ChannelMapping` matches `SyncAll` input; `ListUploads`→`[]Video` matches selection; `ResolveUploads`→string feeds `ListUploads`; `SyncAll`→`[]ChannelResult` consumed by `Run`. No mutation/embedding involved. No defects.

---

## Algorithm Design

### `ChannelMapping` / `Video` / `ChannelResult`
Plain data structs (exported fields). `ChannelMapping{ChannelID,PlaylistID}`,
`Video{ID,PublishedAt}`, `ChannelResult{ChannelID,PlaylistID,Seeded,NewCount,AddedIDs,Err}`.
No behavior.

### `LoadConfig`
**Algorithm:**
```
1. read bytes at path
   → on read error: return ([], err)
2. if len(bytes)==0: return ([], nil)
3. unmarshal bytes via yaml.v3 into { channels: [{channel_id, playlist_id}] }
   → on parse error: return ([], err)
4. return (channels, nil)
```
**Errors:** read failure, YAML parse failure.
**Edge cases:** empty file → empty list (no error).

### `State`
**Internal shape:** `path string`; `channels map[string]cursorState` where
`cursorState{Seeded bool; LastSeenID, LastSeenAt, LastSync string}` (unexported).

**Algorithm (NewState):**
```
1. read path; if not-exist or empty: channels={}
2. else json.Unmarshal into channels map → on error return (nil, err)
3. return (&State{path, channels}, nil)
```
**Methods:** IsSeeded→`channels[id].Seeded`; LastSeenID→`channels[id].LastSeenID`
(empty if absent); LastSeenAt→`channels[id].LastSeenAt` (empty if absent);
SetLastSeen→validate RFC3339, store `{true, videoID, publishedAt, now}`.
**Save:** marshal → temp file (same dir) → chmod 0600 → rename over path.
**Errors:** corrupt JSON (NewState); malformed/empty timestamp (SetLastSeen); write failure (Save).
**Edge cases:** missing/empty file → empty store; unseeded channel → empty getters.

### `YouTube`
**Internal shape:** `svc *youtube.Service` (upstream aliased `youtubev3`); scope
`https://www.googleapis.com/auth/youtube`.
**Algorithm (NewYouTube):** per trace — config from secrets, token cache-or-consent
(`google-oauth2` usage), `savingTokenSource`, build service.
**ResolveUploads/ListUploads/AddToPlaylist:** per trace — thin wrappers around
`channels.list` / `playlistItems.list` / `playlistItems.insert` (`youtube-data-api` usage).
**Errors:** `*googleapi.Error` propagation; 403 `quotaExceeded`/`rateLimitExceeded` or
5xx retried with bounded exponential backoff (≤3 attempts); channel-not-found → typed error.
**Edge cases:** empty uploads playlist → `[]Video{}` (not error); non-video items skipped.

### `SyncAll`
**Algorithm:** per trace. Delegates watermark selection to a **pure helper**:
```
selectNew(videos []Video, watermark string) []Video:
  t = parse(watermark, RFC3339)            # empty watermark ⇒ zero time
  return [v for v in videos if parse(v.PublishedAt, RFC3339).After(t)]
```
**Errors:** per-channel errors captured in `ChannelResult.Err` (other channels continue); fatal failures propagate.
**Edge cases:** empty uploads → skip (no cursor); no new videos → 0 inserts; dry-run → no inserts, no state mutation (preview still reports would-be-added); re-run idempotent.

### `Run`
**Algorithm:** per trace. `func main` defines flags with defaults, calls `Run`, exits non-zero on `err`.
**Flag defaults:** `--config config.yaml`, `--secrets client_secrets.json`, `--token token.json`, `--state state.json`, `--redirect http://localhost:8080`, `--dry-run false`.
**Errors:** any fatal step → `err` → exit 1.

---

## Cross-cutting Concerns

- **Error handling:** Go `(result, error)` everywhere. `SyncAll` isolates per-channel failures into `ChannelResult.Err` so one bad channel doesn't abort the run; only construction/config/state-load failures are fatal (`Run` returns). API quota/rate-limit errors retried with bounded backoff inside the `youtube` facade.
- **Logging:** `log/slog` at Info. `Run` logs: run start (dry-run flag), per-channel result (`channel_id`, `seeded`, `new_count`, `added_ids`, `err`), and an estimated API unit cost (channels.list + playlistItems.list + 50×inserts) for quota visibility. Errors at Warn/Error.
- **Validation:** config — YAML parse (no ID-format enforcement; IDs trusted as given). state — missing/empty ⇒ empty; corrupt ⇒ error; `SetLastSeen` validates RFC3339. flags — stdlib defaults; unknown values surface as API errors at runtime.
- **Caching:** the OAuth token is cached at `tokenPath` and auto-refreshed (persisted via `savingTokenSource`). No video/state caching beyond the cursor.
- **Concurrency:** none — channels are processed sequentially within one sync pass. No mutex required. (Contract guarantees sequential safety only.)

---

## Usages Analysis

### `youtube-data-api` (file: `.goga/usages/cooks/youtube-data-api.md`)
- **What it provides:** YouTube Data API v3 call patterns (channels.list for uploads playlist, playlistItems.list paginated, playlistItems.insert) + quota costs.
- **Where used:** `youtube` cell — `ResolveUploads`, `ListUploads`, `AddToPlaylist`, `YouTube` construction.
- **Why chosen:** official client usage; only source that defines the uploads-playlist resolution and insert semantics.
- **How exactly:** `Channels.List(["contentDetails"]).Id(...)`; `PlaylistItems.List([...]).PlaylistId(...).MaxResults(50).Pages(...)`; `PlaylistItems.Insert(["snippet"], item)`.

### `google-oauth2` (file: `.goga/usages/cooks/google-oauth2.md`)
- **What it provides:** desktop OAuth2 flow, token cache, refresh-with-persist.
- **Where used:** `youtube` cell — `NewYouTube` construction.
- **Why chosen:** playlist writes require user consent; mandatory.
- **How exactly:** `google.ConfigFromJSON(b, scope)`, `AuthCodeURL(state, AccessTypeOffline, ApprovalForce)`, local callback server, `Exchange`, `savingTokenSource` wrapping `config.TokenSource`.

### `yaml` (inline, config)
gopkg.in/yaml.v3; top-level `channels:` list with `channel_id`/`playlist_id`. Used by `LoadConfig`.

### `json` (inline, state)
encoding/json; atomic write (temp + rename). Used by `State` load/save.

### Imported Usages
None — no cell uses `Imports.Usages` (cell-level `.usages` are standalone consumer docs; see below).

---

## `.usages/` Update

### Cell: `youtube`
- **`facade.md`** → `youtube/.usages/facade.md` — Status: **current**. Describes `NewYouTube`, `ResolveUploads`, `ListUploads`, `AddToPlaylist` and consumer constraints; matches the contract. No additions/updates needed.

### Cell: `syncer`
- **`sync.md`** → `syncer/.usages/sync.md` — Status: **current**. Describes `SyncAll` invocation and `ChannelResult` fields; matches the contract. No changes needed.

### Cells `config`, `state`, `cmd`
No `.usages/` directory (single trivial routine / root with no consumers). Correct — none required.

---

## Test Stack Trace

### General Setup
- Pure-logic tests (config, state, `selectNew`, SyncAll orchestration with a fake source) run without network.
- `youtube` facade tests use `net/http/httptest` serving canned YouTube API JSON, with the facade's HTTP client pointed at the test server (no real credentials).
- `State`/`Run` tests use `t.TempDir()` for file paths.

### Source File Registry
`config/config.go`, `state/state.go`, `youtube/youtube.go`, `syncer/syncer.go`, `cmd/main.go` (and `syncer`'s pure helper).

---

### Positive Tests

#### `LoadConfig_ParsesMappings`
**Setup:** temp file with `channels: [{channel_id: UCa, playlist_id: PLa}, {channel_id: UCb, playlist_id: PLb}]`.
**Input:** path.
**Trace:**
```
LoadConfig(path)
  → read bytes (ok)
  → yaml.Unmarshal → [{UCa,PLa},{UCb,PLb}]
  → return mappings
assert len==2 && mappings[1].ChannelID=="UCb"
```
**Assertions:** 2 mappings; field mapping correct (snake_case YAML → exported fields).
**Sufficiency:** guards YAML tag mapping and multi-entry parsing.

#### `State_SeedThenSaveThenReload`
**Setup:** empty temp dir; path = temp/state.json.
**Input:** `SetLastSeen("UC1","v1","2026-07-27T10:00:00Z")` then `Save()`.
**Trace:**
```
NewState(path) → missing file → channels={}
IsSeeded("UC1") → false
SetLastSeen("UC1","v1","2026-07-27T10:00:00Z") → stores {Seeded:true,...}
Save() → writes temp, rename
NewState(path) → json.Unmarshal → channels=={UC1:{...}}
IsSeeded("UC1") → true ; LastSeenID=="v1" ; LastSeenAt=="2026-07-27T10:00:00Z"
```
**Assertions:** seeded true after reload; cursor values round-trip.
**Sufficiency:** proves persistence + cursor retrieval.

#### `SyncAll_AddsOnlyNewVideos_AdvancesCursor`
**Setup:** fake source returning uploads `[T+5, T+1, T-2]` (newest-first); `State` seeded with watermark `T`.
**Input:** `SyncAll(fakeYT, state, [{UCa,PLa}], dryRun=false)`.
**Trace:**
```
ResolveUploads(UCa) → "UPa"
ListUploads("UPa") → [{T+5},{T+1},{T-2}]
IsSeeded(UCa) → true ; mark=T
selectNew([...], T) → [{T+5},{T+1}]
AddToPlaylist(PLa, T+1) ; AddToPlaylist(PLa, T+5)   # oldest→newest
SetLastSeen(UCa, T+5-id, T+5)                       # newest
result {NewCount:2, AddedIDs:[T+1,T+5]}
```
**Assertions:** exactly 2 inserts (T+1 then T+5); cursor advanced to T+5; `NewCount==2`.
**Sufficiency:** core watermark logic + insert ordering + cursor advance.

#### `SyncAll_SeedsOnFirstContact_AddsNothing`
**Setup:** fake source returning `[T+5, T+3]`; `State` unseeded for UCa.
**Input:** `SyncAll(fakeYT, state, [{UCa,PLa}], false)`.
**Trace:** `IsSeeded(UCa)→false`; result `{Seeded:true,NewCount:0}`; **no** `AddToPlaylist` calls; `SetLastSeen(UCa, T+5-id, T+5)`.
**Assertions:** zero inserts; cursor set to newest; `Seeded==true`.
**Sufficiency:** first-contact seed-and-skip semantics.

---

### Negative Tests

#### `LoadConfig_MalformedYAML_ReturnsError`
**Setup:** temp file `channels: [ { channel_id: UCa ]` (broken).
**Input:** path. **Trace:** `yaml.Unmarshal` → error → `return ([], err)`.
**Assertions:** error non-nil; mappings empty. **Sufficiency:** parse-failure propagation.

#### `State_CorruptFile_ReturnsError`
**Setup:** temp file `{not valid json`.
**Input:** path. **Trace:** `NewState` → `json.Unmarshal` → error.
**Assertions:** error non-nil; no silent empty store. **Sufficiency:** prevents corruption being treated as fresh (which would re-add everything).

#### `SyncAll_ChannelError_RecordsPerChannel_ContinuesOthers`
**Setup:** fake source: UCa → ResolveUploads error; UCb → normal.
**Input:** `SyncAll(fakeYT, state, [{UCa,PLa},{UCb,PLb}], false)`.
**Trace:** UCa → `ChannelResult{Err}`; UCb → processed normally.
**Assertions:** results[0].Err != nil; results[1] seeded/added as expected. **Sufficiency:** one failing channel doesn't abort the run.

---

### Edge Case Tests

#### `LoadConfig_EmptyFile_ReturnsEmptyList`
**Setup:** temp file with 0 bytes. **Assertions:** err==nil; mappings empty. **Sufficiency:** empty config ≠ error.

#### `SyncAll_EmptyUploads_SkipsChannel_NoCursor`
**Setup:** fake source returning `[]Video{}` for UCa (unseeded).
**Assertions:** `NewCount==0`; **no** `SetLastSeen` call; `IsSeeded(UCa)` still false.
**Sufficiency:** the empty-uploads guard (fix #2) — no panic, channel left for next run.

#### `SyncAll_NoNewVideos_PerformsZeroInserts`
**Setup:** seeded channel; all uploads older than watermark.
**Assertions:** zero `AddToPlaylist` calls; `NewCount==0`. **Sufficiency:** watermark upper-bound; quota-safe no-op.

#### `SyncAll_DryRun_MutatesNothing`
**Setup:** seeded channel with 2 new videos; `state.json` snapshot taken before.
**Input:** `dryRun=true`.
**Assertions:** zero inserts; `state.json` byte-identical after; results still report the 2 would-be-added IDs. **Sufficiency:** dry-run is side-effect-free (acceptance criterion).

#### `SyncAll_ReRunIsIdempotent`
**Setup:** run once (seeds/adds), run again with no new videos.
**Assertions:** second run → zero inserts, cursor unchanged. **Sufficiency:** no duplicate adds across runs.

---

## Additional Instructions for the Implementation Agent

- Create `go.mod` (`module youtube-updater`) and `go get` the five deps; alias the upstream client as `youtubev3` inside package `youtube`.
- Add `.gitignore` for `client_secrets.json`, `token.json`, `state.json` (and the built binary); write token/state with file mode `0600`.
- Keep `SyncAll`'s watermark selection in a pure `selectNew(videos, watermark) []Video` helper so the core logic is unit-testable without the API; test the `youtube` facade via `httptest`.
- Time comparison: parse both `Video.PublishedAt` and `State.LastSeenAt` to `time.Time` (RFC3339) before comparing — do not rely on lexical string ordering.
- `func main` defines flags with the defaults above, calls `Run`, and exits non-zero on error; keep `Run` free of flag parsing so it stays testable.
- Bound API unit cost: never re-insert (cursor guarantees this); process channels sequentially.
