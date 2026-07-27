# Plan: `youtube-playlist-sync`

## Purpose

Implement the greenfield Go CLI that monitors configured YouTube channels and
appends each channel's **new** uploads to a designated playlist, tracking only the
**last seen video** per channel (publish-time watermark). After implementation the
project provides: a one-shot binary (`cmd`) that loads a YAML channel→playlist
config (`config`), authenticates via OAuth2 and calls the YouTube Data API v3
through a single facade (`youtube`), syncs each channel against a persisted
watermark (`state`, `syncer`), and is fully test-covered.

**Strategy:** leaves first (`config`, `state`, `youtube`), then `syncer`, then
`cmd`, then cross-cell integration tests. Every coding task follows TDD (contract
tests → code → verification → logic tests → debug → re-verify → lint).

## Context

### Contract Surface

**Entity: `ChannelMapping`** (cell `config`)
- Type: data Entity. Declared `location`: `config.go`.
- Facade obligation: importable from package `config` (`youtube-updater/config`).
- Properties: `ChannelID -> string` (UC… channel id); `PlaylistID -> string` (PL… target playlist).
- Semantic: one monitored channel→playlist pair.

**Entity: `LoadConfig`** (cell `config`)
- Type: Routine. Declared `location`: `config.go`.
- Signature: `LoadConfig(path: string) -> mappings:[]ChannelMapping, err:error`.
- Behavior: parse YAML `channels:` list into `[]ChannelMapping`; empty file → empty list (no error); read/parse failure → err.
- Usages: `yaml` (inline) — gopkg.in/yaml.v3.

**Entity: `State`** (cell `state`)
- Type: Entity. Declared `location`: `state.go`.
- Signature (factory): `State(path: string)` → impl `NewState(path) (*State, error)`.
- Property: `Path -> string`.
- Methods: `IsSeeded(channelID)->bool`; `LastSeenID(channelID)->string`; `LastSeenAt(channelID)->string` (watermark); `SetLastSeen(channelID,videoID,publishedAt)->err`; `Save()->err`.
- Semantic: missing/empty file → empty store (no error); corrupt JSON → err; Save atomic (temp+rename, 0600); SetLastSeen validates RFC3339.
- Usages: `json` (inline) — encoding/json + atomic write.

**Entity: `Video`** (cell `youtube`)
- Type: data Entity. Declared `location`: `youtube.go`.
- Properties: `ID -> string`; `PublishedAt -> string` (RFC3339, the watermark source).

**Entity: `YouTube`** (cell `youtube`)
- Type: Entity (facade). Declared `location`: `youtube.go`.
- Signature (factory): `YouTube(secretsPath, tokenPath, redirectURL string)` → impl `NewYouTube(...) (*YouTube, error)`.
- Methods: `ResolveUploads(channelID)->uploadsPlaylistID,err`; `ListUploads(uploadsPlaylistID)->[]Video,err` (newest-first, paginated); `AddToPlaylist(playlistID,videoID)->itemID,err`.
- Semantic: construction runs OAuth2 (cached token or consent) then builds the service; safe for sequential use in one pass; quota/rate-limit retried with bounded backoff; channel-not-found → err; empty uploads → `[]Video{}` (not error).
- Usages: `youtube-data-api`, `google-oauth2` (project-level cooks).

**Entity: `ChannelResult`** (cell `syncer`)
- Type: data Entity. Declared `location`: `syncer.go`.
- Properties: `ChannelID`, `PlaylistID` (string); `Seeded` (bool); `NewCount` (int); `AddedIDs` ([]string); `Err` (error).

**Entity: `SyncAll`** (cell `syncer`)
- Type: Routine. Declared `location`: `syncer.go`.
- Signature: `SyncAll(youTube: YouTube, state: State, mappings: []ChannelMapping, dryRun: bool) -> results:[]ChannelResult, err:error`.
- Behavior: per-channel — empty uploads → skip (no cursor); unseeded → seed cursor to newest, add nothing; else add only videos newer than watermark (oldest-first), advance cursor. dryRun → no inserts, no state mutation.
- Imported dependencies: `config.ChannelMapping`, `state.State`, `youtube.YouTube`, `youtube.Video`.
- Internal helper: pure `selectNew(videos []Video, watermark string) []Video`.

**Entity: `Run`** (cell `cmd`)
- Type: Routine. Declared `location`: `main.go` (package `main`).
- Signature: `Run(configPath, secretsPath, tokenPath, statePath, redirectURL string, dryRun bool) -> err:error`.
- Behavior: LoadConfig → NewYouTube → NewState → SyncAll → (unless dryRun) State.Save → log each ChannelResult → return err. `func main` parses flags, calls Run, exits non-zero on err.
- Imported dependencies: `config.LoadConfig`, `state.State`, `youtube.YouTube`, `syncer.SyncAll`, `syncer.ChannelResult`.

### Re-exports
None.

### Usages Context
- `youtube-data-api` (`.goga/usages/cooks/youtube-data-api.md`): YouTube Data API v3 patterns — `channels.list` (contentDetails → uploads playlist), `playlistItems.list` (paginated, 50/page), `playlistItems.insert`; quota costs (insert=50). Used by `youtube` cell.
- `google-oauth2` (`.goga/usages/cooks/google-oauth2.md`): desktop OAuth2 flow, token cache + refresh-with-persist, `savingTokenSource`. Used by `youtube` cell construction.
- `yaml` (inline, `config`): gopkg.in/yaml.v3; top-level `channels:` of `{channel_id, playlist_id}`.
- `json` (inline, `state`): encoding/json; atomic write (temp+rename).

### Imported Usages
None (no cell uses `Imports.Usages`).

### Local Usages
- `youtube/.usages/facade.md` — consumer guide for the YouTube facade. Status: **existing/current** (no creation task; verified consistent in design doc).
- `syncer/.usages/sync.md` — consumer guide for SyncAll/ChannelResult. Status: **existing/current** (no creation task).

### External Dependencies
- `google.golang.org/api/youtube/v3` (alias `youtubev3` inside package `youtube`)
- `golang.org/x/oauth2`, `golang.org/x/oauth2/google`, `google.golang.org/api/option`
- `gopkg.in/yaml.v3`
- Tools: `go`, `gofmt`, `go vet`, `goga`

## Facts
- Module name: `youtube-updater` (go.mod at repo root).
- Packages = cell paths: `config`, `state`, `youtube`, `syncer`, `cmd` (main).
- YouTube scope: `https://www.googleapis.com/auth/youtube`.
- Flag defaults: `--config config.yaml`, `--secrets client_secrets.json`, `--token token.json`, `--state state.json`, `--redirect http://localhost:8080`, `--dry-run false`.
- Secrets/token/state are gitignored and written mode `0600`.
- Time comparison: parse both timestamps to `time.Time` (RFC3339); never lexical string compare.
- No concurrency (sequential per-channel processing).
- All 5 CODEMANIFESTs are lint-clean and review-passed; contracts are READ-ONLY for implementation.

## Gap Analysis
- Missing contract entities: **all** — `ChannelMapping`, `LoadConfig`, `State`, `Video`, `YouTube`, `ChannelResult`, `SyncAll`, `Run` (greenfield).
- Missing facade exposure: all packages absent; nothing importable yet.
- No go.mod / deps / .gitignore yet.
- Existing code to reuse: none.
- Test coverage gaps: full suite required (contract + logic + integration).

---

## Tasks

> **Package ordering rule**: coding tasks for each package are completed before the next. Within each coding task, contract tests are written first (TDD).

### Task 1: Project infrastructure (infrastructure)

Create the Go module, dependencies, and hygiene files so all cell packages can be implemented and imported. No contract entity is created here; this enables the facade (package = facade in Go).

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] Create `go.mod`: `go mod init youtube-updater`
- [ ] Add dependencies: `go get google.golang.org/api/youtube/v3 golang.org/x/oauth2 golang.org/x/oauth2/google google.golang.org/api/option gopkg.in/yaml.v3`
- [ ] Create `.gitignore` with: `client_secrets.json`, `token.json`, `state.json`, `/youtube-updater` (binary)
- [ ] Verify module builds (empty pkgs ok): `go build ./...`
- [ ] Verify `go vet ./...` is clean
- [ ] Lint contracts unchanged: `goga lint` — expect `cells: 5 errors: 0`

### Task 2: `config` cell — ChannelMapping + LoadConfig

Implement the configuration data model and YAML loader at `config/config.go` (package `config`). Covers entities `ChannelMapping` (data) and `LoadConfig` (routine).

**Usages relevant to this task:**
- `yaml`: gopkg.in/yaml.v3. Unmarshal into a wrapper struct `{ Channels []ChannelMapping \`yaml:"channels"\` }`; map YAML `channel_id`→`ChannelID`, `playlist_id`→`PlaylistID` via struct tags.

**Verbatim trace (LoadConfig):** Input `path`; read bytes (read err → `err`); if len==0 → return `([], nil)`; unmarshal via yaml.v3 (parse err → `err`); return `mappings` (empty list if file empty — not an error).

**CRITICAL: `CODEMANIFEST` files — read-only. Fix implementation, never the contract.**

- [ ] **STEP 0 — Declaration**: state this task covers `config.ChannelMapping` and `config.LoadConfig` at `config/config.go`.
- [ ] **Contract tests (STEP 1)**: `config/config_test.go` — assert package imports; `ChannelMapping` fields `ChannelID`,`PlaylistID` exist (string); `LoadConfig` signature `func LoadConfig(path string) ([]ChannelMapping, error)`. (Expected to fail.)
- [ ] **Code (STEP 2)**: create `config/config.go`; define `ChannelMapping{ChannelID,PlaylistID string}`; define `LoadConfig(path string) ([]ChannelMapping, error)` per the verbatim trace (read → empty?→`([],nil)` → yaml unmarshal into `{channels:[...]}` → return).
- [ ] **Interface verification (STEP 3)**: `go test ./config/...` — contract tests pass.
- [ ] **Logic tests (STEP 4)**: `LoadConfig_ParsesMappings` (2 entries, field mapping); `LoadConfig_EmptyFile_ReturnsEmptyList` (0 bytes → nil err, empty); `LoadConfig_MalformedYAML_ReturnsError`.
- [ ] **Debugging (STEP 5)**: `go test ./config/...` — fix code (not tests) until all pass.
- [ ] **Contract re-verification (STEP 6)**: facade importable (`youtube-updater/config`); signatures match CODEMANIFEST.
- [ ] **Lint (STEP 7)**: `gofmt -l .` (empty); `go vet ./config/...`; `goga lint`.
- [ ] **Completion (STEP 8)**: mark checkboxes → review → approval → next task.

### Task 3: `state` cell — State

Implement the persisted per-channel cursor store at `state/state.go` (package `state`). Covers entity `State`.

**Usages relevant to this task:**
- `json`: encoding/json. Marshal/unmarshal the channel map; atomic write = temp file in same dir + `os.Rename`, `os.Chmod 0600`.

**Verbatim traces:**
- `NewState(path)`: read path; not-exist or empty → `channels={}`; else `json.Unmarshal` (corrupt → err); return `*State{path,channels}`.
- `SetLastSeen(channelID,videoID,publishedAt)`: validate `publishedAt` via `time.Parse(time.RFC3339,…)` (invalid/empty → err); store `{Seeded:true, LastSeenID, LastSeenAt, LastSync}`.
- `Save()`: `json.MarshalIndent` → temp file (same dir) → chmod 0600 → rename over `Path`.
- `IsSeeded/LastSeenID/LastSeenAt`: read from map; empty string if channel absent/unseeded.

**CRITICAL: `CODEMANIFEST` files — read-only.**

- [ ] **STEP 0 — Declaration**: covers `state.State` at `state/state.go`.
- [ ] **Contract tests (STEP 1)**: `state/state_test.go` — package imports; `State` has methods `IsSeeded(string)bool`, `LastSeenID(string)string`, `LastSeenAt(string)string`, `SetLastSeen(string,string,string)error`, `Save()error`; property `Path string`; factory `NewState(path string) (*State, error)`. (Expected to fail.)
- [ ] **Code (STEP 2)**: `state/state.go` — `State{path string; channels map[string]cursorState}` (cursorState unexported: `{Seeded bool; LastSeenID,LastSeenAt,LastSync string}`); `NewState`, getters, `SetLastSeen` (RFC3339 validate), `Save` (atomic, 0600).
- [ ] **Interface verification (STEP 3)**: `go test ./state/...` — contract tests pass.
- [ ] **Logic tests (STEP 4)**: `State_SeedThenSaveThenReload` (NewState missing→empty; SetLastSeen; Save; reload→same); `State_CorruptFile_ReturnsError`; `State_MissingFile_StartsEmpty` (IsSeeded false).
- [ ] **Debugging (STEP 5)**: `go test ./state/...` until green.
- [ ] **Contract re-verification (STEP 6)**: signatures match; missing/empty→empty, corrupt→err semantics hold.
- [ ] **Lint (STEP 7)**: `gofmt -l .`; `go vet ./state/...`; `goga lint`.
- [ ] **Completion (STEP 8)**: mark → review → approval → next.

### Task 4: `youtube` cell — Video + YouTube facade

Implement the Google integration facade at `youtube/youtube.go` (package `youtube`; alias upstream `youtubev3`). Covers entities `Video` (data) and `YouTube` (facade).

**Usages relevant to this task:**
- `google-oauth2`: `google.ConfigFromJSON(b, scope)`; `AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)`; local HTTP callback on `redirectURL`; `Exchange`; `savingTokenSource` wrapping `config.TokenSource` to persist refreshed tokens; token file mode 0600.
- `youtube-data-api`: `Channels.List(["contentDetails"]).Id(...)`; `PlaylistItems.List(["snippet","contentDetails"]).PlaylistId(...).MaxResults(50).Pages(ctx, fn)`; `PlaylistItems.Insert(["snippet"], &PlaylistItem{Snippet:{PlaylistId, ResourceId:{Kind:"youtube#video", VideoId}}})`.

**Verbatim traces:**
- `NewYouTube(secretsPath,tokenPath,redirectURL)`: read secrets → `google.ConfigFromJSON(b, youtubeScope)`, set `RedirectURL`; token = `loadToken(tokenPath)` or consent flow (`authorize`) → `saveToken` (0600); `*http.Client` via `savingTokenSource`; `youtube.NewService(ctx, option.WithHTTPClient(client))`.
- `ResolveUploads(channelID)`: `Channels.List(["contentDetails"]).Id(channelID).Do()`; 0 items → err "channel not found"; return `Items[0].ContentDetails.RelatedPlaylists.Uploads`.
- `ListUploads(uploadsPlaylistID)`: `PlaylistItems.List([...]).PlaylistId(id).MaxResults(50).Pages(...)` → collect `Video{ID:item.ContentDetails.VideoId, PublishedAt:item.ContentDetails.VideoPublishedAt}`, skip empty VideoId; return newest-first.
- `AddToPlaylist(playlistID,videoID)`: `PlaylistItems.Insert(["snippet"], item).Do()` → return `created.Id`.

**CRITICAL: `CODEMANIFEST` files — read-only.**

- [ ] **STEP 0 — Declaration**: covers `youtube.Video` and `youtube.YouTube` at `youtube/youtube.go`.
- [ ] **Contract tests (STEP 1)**: `youtube/youtube_test.go` — package imports; `Video{ID,PublishedAt string}`; `YouTube` methods `ResolveUploads(string)(string,error)`, `ListUploads(string)([]Video,error)`, `AddToPlaylist(string,string)(string,error)`; factory `NewYouTube(secretsPath,tokenPath,redirectURL string) (*YouTube, error)`. (Expected to fail.)
- [ ] **Code (STEP 2)**: `youtube/youtube.go` — `Video` struct; `YouTube{svc *youtubev3.Service}`; `NewYouTube` (OAuth per `google-oauth2`); `ResolveUploads`, `ListUploads`, `AddToPlaylist` per traces; bounded backoff retry on 403 `quotaExceeded`/`rateLimitExceeded` and 5xx.
- [ ] **Interface verification (STEP 3)**: `go test ./youtube/...` — contract tests pass.
- [ ] **Logic tests (STEP 4)**: via `net/http/httptest` serving canned YouTube API JSON, facade client pointed at test server — `ListUploads_ParsesPaginatedNewestFirst`; `ResolveUploads_ChannelNotFound_ReturnsError`; `AddToPlaylist_ReturnsItemID`; `ListUploads_EmptyPlaylist_ReturnsEmptyNoError`.
- [ ] **Debugging (STEP 5)**: `go test ./youtube/...` until green.
- [ ] **Contract re-verification (STEP 6)**: facade importable; method signatures match; no pointer/HTTP type leaks across the boundary.
- [ ] **Lint (STEP 7)**: `gofmt -l .`; `go vet ./youtube/...`; `goga lint`.
- [ ] **Completion (STEP 8)**: mark → review → approval → next.

### Task 5: `syncer` cell — ChannelResult + SyncAll (+ selectNew)

Implement orchestration at `syncer/syncer.go` (package `syncer`). Covers entities `ChannelResult` (data) and `SyncAll` (routine), plus internal pure helper `selectNew`.

**Usages relevant to this task:** none (delegates to the `YouTube` facade; no direct library use).

**Verbatim trace (SyncAll):** Input `youTube, state, mappings, dryRun`; for each `ChannelMapping m`: (1) `ResolveUploads(m.ChannelID)` → on err `ChannelResult{Err}`, continue; (2) `ListUploads(uploadsID)` → on err `ChannelResult{Err}`, continue; (3) if empty → `ChannelResult{NewCount:0}`, continue (no cursor); (4) `newest=videos[0]`; (5) if `!IsSeeded` → result `{Seeded:true,NewCount:0}`, unless dryRun `SetLastSeen(newest)`; (6) else `mark=LastSeenAt`, `isNew=selectNew(videos,mark)`, unless dryRun insert each (oldest→newest) and `SetLastSeen(newest)`, result `{NewCount:len(isNew), AddedIDs}`. Return `results`.

**Verbatim `selectNew`:** `t=parse(watermark,RFC3339)` (empty⇒zero time); return videos whose parsed PublishedAt is `After(t)`.

**CRITICAL: `CODEMANIFEST` files — read-only.**

- [ ] **STEP 0 — Declaration**: covers `syncer.ChannelResult` and `syncer.SyncAll` at `syncer/syncer.go`.
- [ ] **Contract tests (STEP 1)**: `syncer/syncer_test.go` — package imports; `ChannelResult` fields; `SyncAll` signature `func SyncAll(youTube *youtube.YouTube, state *state.State, mappings []config.ChannelMapping, dryRun bool) ([]ChannelResult, error)`. (Expected to fail.)
- [ ] **Code (STEP 2)**: `syncer/syncer.go` — `ChannelResult` struct; `selectNew(videos []youtube.Video, watermark string) []youtube.Video` (parse to time.Time, compare); `SyncAll` per the verbatim trace (empty guard, seed/advance, dryRun skips inserts+SetLastSeen).
- [ ] **Interface verification (STEP 3)**: `go test ./syncer/...` — contract tests pass.
- [ ] **Logic tests (STEP 4)**: `selectNew_FiltersByWatermark` (pure: returns only those after mark); `selectNew_EmptyWatermark_SelectsAll`; `selectNew_ParsesToTime_NotLexical` (equal-length-different-order timestamps). (End-to-end SyncAll behavior is covered by Task 7 integration tests.)
- [ ] **Debugging (STEP 5)**: `go test ./syncer/...` until green.
- [ ] **Contract re-verification (STEP 6)**: `SyncAll` signature matches (imports `youtube.YouTube`, `state.State`, `config.ChannelMapping`); dryRun mutates nothing.
- [ ] **Lint (STEP 7)**: `gofmt -l .`; `go vet ./syncer/...`; `goga lint`.
- [ ] **Completion (STEP 8)**: mark → review → approval → next.

### Task 6: `cmd` cell — Run + main

Implement the CLI entry and composition root at `cmd/main.go` (package `main`). Covers entity `Run`.

**Usages relevant to this task:** none (composition only).

**Verbatim trace (Run):** Input flags; (1) `LoadConfig(configPath)` → err? return; (2) `NewYouTube(secretsPath,tokenPath,redirectURL)` → err? return; (3) `NewState(statePath)` → err? return; (4) `results,err=SyncAll(...)`; log each `ChannelResult` (slog.Info: channel_id, seeded, new_count, added_ids, err) + estimated unit cost; (5) unless dryRun `state.Save()`; return err. `func main` defines flags (defaults below), calls `Run`, `os.Exit` non-zero on err.

**Flag defaults:** `--config config.yaml`, `--secrets client_secrets.json`, `--token token.json`, `--state state.json`, `--redirect http://localhost:8080`, `--dry-run false`.

**CRITICAL: `CODEMANIFEST` files — read-only.**

- [ ] **STEP 0 — Declaration**: covers `cmd.Run` at `cmd/main.go`.
- [ ] **Contract tests (STEP 1)**: `cmd/main_test.go` — `Run` signature `func Run(configPath, secretsPath, tokenPath, statePath, redirectURL string, dryRun bool) error`; `Run` is callable independently of `main`. (Expected to fail.)
- [ ] **Code (STEP 2)**: `cmd/main.go` — `Run(...)` per trace (wires LoadConfig/NewYouTube/NewState/SyncAll/Save, slog logging, unit-cost estimate); `func main()` with `flag` package + defaults + `os.Exit`.
- [ ] **Interface verification (STEP 3)**: `go test ./cmd/...` — contract tests pass.
- [ ] **Logic tests (STEP 4)**: `Run_DryRun_DoesNotSaveState` (temp config/state, fake or httptest-backed YouTube, dryRun=true → state.json not written); `Run_ReturnsErrorOnMissingConfig` (bad configPath → err).
- [ ] **Debugging (STEP 5)**: `go test ./cmd/...` until green.
- [ ] **Contract re-verification (STEP 6)**: `Run` signature matches; `main` maps err to non-zero exit; no flag parsing inside `Run`.
- [ ] **Lint (STEP 7)**: `gofmt -l .`; `go vet ./cmd/...`; `goga lint`.
- [ ] **Completion (STEP 8)**: mark → review → approval → next.

### Task 7: Integration tests — SyncAll end-to-end

Verify cross-entity scenarios spanning `syncer` + `youtube` + `state` using an httptest-backed real `YouTube` facade (no real credentials).

**Usages relevant to this task:**
- `youtube-data-api`: canned JSON shapes the httptest server must return (channels.list contentDetails; playlistItems.list pages; playlistItems.insert response).

- [ ] Create `syncer/syncer_integration_test.go` (build tag `integration` if desired) using `net/http/httptest`.
- [ ] `SyncAll_AddsOnlyNewVideos_AdvancesCursor`: server returns uploads `[T+5,T+1,T-2]`; state seeded at `T`; assert exactly 2 inserts (T+1 then T+5), cursor advanced to T+5, `NewCount==2`.
- [ ] `SyncAll_SeedsOnFirstContact_AddsNothing`: unseeded channel; assert zero inserts, cursor set to newest, `Seeded==true`.
- [ ] `SyncAll_EmptyUploads_SkipsChannel_NoCursor`: server returns empty; assert `NewCount==0`, no SetLastSeen, still unseeded.
- [ ] `SyncAll_NoNewVideos_PerformsZeroInserts`: all older than watermark; zero inserts.
- [ ] `SyncAll_DryRun_MutatesNothing`: state.json byte-identical before/after; results still report would-be-added IDs.
- [ ] `SyncAll_ReRunIsIdempotent`: two runs, no new videos between → second run zero inserts, cursor unchanged.
- [ ] `SyncAll_ChannelError_RecordsPerChannel_ContinuesOthers`: UCa ResolveUploads errors, UCb normal; results[0].Err!=nil, results[1] ok.
- [ ] Run validation: `go test ./syncer/...` (and `go test ./...`).

---

## Validation Commands

- `go build ./...`: all packages compile (facade importability for Go)
- `go test ./...`: run all tests
- `go vet ./...`: vet
- `gofmt -l .`: formatting (must output nothing)
- `goga lint`: contract lint — must remain `cells: 5 errors: 0` (CODEMANIFEST untouched)

---

## Completion Criteria

- [ ] Every contract entity is implemented in the correct `location`
- [ ] Every contract entity is accessible from its package facade
- [ ] Properties and methods match the declared API
- [ ] Descriptions are reflected in behavior
- [ ] Contract dependencies are met (`syncer`←config/state/youtube; `cmd`←all)
- [ ] Every coding task followed the TDD workflow (contract → code → verify → logic → debug → re-verify → lint)
- [ ] Contract + logic tests cover facade, API, and behavior within each coding task
- [ ] Integration tests cover cross-entity SyncAll scenarios (Task 7)
- [ ] No package/cell boundary was expanded; no new cells created
- [ ] `CODEMANIFEST` files were not modified (read-only)
- [ ] All validation commands pass (`go build`, `go test`, `go vet`, `gofmt`, `goga lint`)
- [ ] Every Usages entry (`youtube-data-api`, `google-oauth2`, `yaml`, `json`) is referenced in at least one task
