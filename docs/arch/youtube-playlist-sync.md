# Architecture Plan — YouTube Channel → Playlist Sync

Cells architecture for the task `docs/tasks/youtube-playlist-sync.md`. Greenfield —
all cells below are **new**. The two project-level usages (`youtube-data-api`,
`google-oauth2`, declared in `.goga/config.yml`) apply to the `youtube` cell only.

Design decisions locked during brainstorm:
- Go static binary; OAuth2 merged into the `youtube` facade (keeps contracts
  pointer-free per the Go DSL signature rule).
- Watermark new-video detection: per channel only the **last seen video** is stored
  (publish-time high-water mark); first contact seeds the cursor and adds nothing.
- One-shot CLI; 5 cells.

---

## 1. Implementation order

Leaves first, then dependents. No cell depends on a later cell.

| # | Cell   | Reason                                              |
|---|--------|-----------------------------------------------------|
| 1 | `config`  | No Imports.                                        |
| 2 | `state`   | No Imports.                                        |
| 3 | `youtube` | No Imports. (Independent of 1–2; any order.)       |
| 4 | `syncer`  | Imports `config`, `state`, `youtube`.              |
| 5 | `cmd`     | Imports `config`, `state`, `youtube`, `syncer`.    |

## 2. Dependency map

```
  config        state         youtube
    ▲             ▲             ▲  ▲
    │             │             │  │
    │             │      Video ─┘  │
    ├── syncer ───┴──── YouTube ───┘
    │     ▲
    │     │
    └── cmd └── (also imports State, YouTube, LoadConfig, SyncAll, ChannelResult)
```

`syncer` ← `config` (ChannelMapping), `state` (State), `youtube` (YouTube, Video).
`cmd` ← `config` (LoadConfig), `state` (State), `youtube` (YouTube), `syncer` (SyncAll, ChannelResult).
No cycles.

---

## 3. Artifacts

### 3.1 Cell `config`

**`config/CODEMANIFEST`**

```yaml
Usages:
  yaml: |
    Parse and emit the channel→playlist config with gopkg.in/yaml.v3. The file is a
    top-level "channels:" list whose items carry channel_id and playlist_id.

Annotations: |
  This cell owns the configuration data model and its YAML parsing.

  Use `yaml` to read the config file.
  Return errors following the (result, error) idiom.

---

"ChannelMapping()":
  location: config.go
  annotations: |
    One monitored channel and the target playlist its new uploads are added to.
  properties:
    "ChannelID -> string": |
      The monitored channel identifier (UC…).
    "PlaylistID -> string": |
      The target playlist identifier (PL…) where new uploads are added.

"LoadConfig(path: string) -> mappings:[]ChannelMapping, err:error":
  location: config.go
  annotations: |
    Parse the YAML channel→playlist mapping file into a list of `ChannelMapping`.

    `path`: path to the config file
    `mappings`: the parsed channel→playlist mappings
    `err`: error if the file cannot be read or parsed

    Algorithm:
    1. Read the file at `path`
    2. Unmarshal it with `yaml` into a list of `ChannelMapping`
    3. Return `mappings`, or `err` on read or parse failure

    Requirements:
    - An empty file yields an empty list, not an error

---

Author: Goga
CreatedAt: 27/07/26
Description: |
  Configuration data model and YAML loader for the channel→playlist mapping.
```

No `.usages/` file — the single routine is self-describing.

---

### 3.2 Cell `state`

**`state/CODEMANIFEST`**

```yaml
Usages:
  json: |
    Persist state with encoding/json. Write atomically by creating a temporary file
    in the same directory and renaming it over the target path.

Annotations: |
  This cell owns the persisted per-channel "last seen video" cursor store.

  Use `json` to load and save the state file.
  Return errors following the (result, error) idiom.

---

"State(path: string)":
  location: state.go
  annotations: |
    Persisted per-channel "last seen video" cursor. The sole source of truth for
    which videos have been processed.

    `path`: path to the state file

    Algorithm:
    1. On construction, read `path` with `json`; if absent or empty, start empty
    2. Expose per-channel cursor getters and a setter
    3. Save persists the cursors atomically

    Requirements:
    - A missing or empty state file is not an error; the store starts empty
  properties:
    "Path -> string": |
      Path to the state file.
  methods:
    "IsSeeded(channelID: string) -> seeded:bool": |
      Whether a first contact has ever been recorded for the channel.

      `channelID`: the channel identifier
      `seeded`: true if a cursor has been set for this channel
    "LastSeenID(channelID: string) -> videoID:string": |
      The identifier of the last seen video for the channel.

      `channelID`: the channel identifier
      `videoID`: the last seen video identifier, empty if the channel is unseeded
    "LastSeenAt(channelID: string) -> publishedAt:string": |
      The publish-time watermark for the channel.

      `channelID`: the channel identifier
      `publishedAt`: RFC 3339 timestamp of the last seen video, empty if unseeded
    "SetLastSeen(channelID: string, videoID: string, publishedAt: string) -> err:error": |
      Record the last seen video for a channel and mark it seeded.

      `channelID`: the channel identifier
      `videoID`: the last seen video identifier
      `publishedAt`: RFC 3339 timestamp of that video
      `err`: error if the timestamp is empty or malformed

      Algorithm:
      1. Store `videoID` and `publishedAt` for `channelID`
      2. Mark the channel seeded
    "Save() -> err:error": |
      Persist all cursors to the state file atomically.

      `err`: error if writing fails

      Use `json` for serialization.

      Requirements:
      - The write is atomic: readers and crash recovery never observe a partially-written file

---

Author: Goga
CreatedAt: 27/07/26
Description: |
  Persisted per-channel "last seen video" cursor store.
```

No `.usages/` file — the cursor API is self-describing from the contract.

---

### 3.3 Cell `youtube`

**`youtube/CODEMANIFEST`**

```yaml
Usages:
  youtube-data-api: .goga/usages/cooks/youtube-data-api.md
  google-oauth2: .goga/usages/cooks/google-oauth2.md

Annotations: |
  This cell is the Google integration facade: it owns OAuth2 credentials and all
  YouTube Data API v3 calls, hiding pointer and HTTP types behind a string-based
  contract.

  Use `google-oauth2` to obtain the authenticated client and to cache and refresh
  the token during construction.
  Use `youtube-data-api` for every API call.
  Return errors following the (result, error) idiom.

---

"Video()":
  location: youtube.go
  annotations: |
    A YouTube video as relevant to sync: its identifier and publish time.
  properties:
    "ID -> string": |
      The video identifier.
    "PublishedAt -> string": |
      RFC 3339 publish timestamp; used as the new-video watermark.

"YouTube(secretsPath: string, tokenPath: string, redirectURL: string)":
  location: youtube.go
  annotations: |
    Facade over Google credentials and the YouTube Data API v3.

    `secretsPath`: path to client_secrets.json (Desktop OAuth client)
    `tokenPath`: path to the cached token file
    `redirectURL`: loopback redirect URL for the consent callback

    Algorithm:
    1. On construction, obtain an authenticated HTTP client via `google-oauth2`
       (use the cached token at `tokenPath` if valid, otherwise run the consent
       flow and persist the token)
    2. Build the YouTube service client

    Requirements:
    - The constructed facade is safe for sequential use across one sync pass
  methods:
    "ResolveUploads(channelID: string) -> uploadsPlaylistID:string, err:error": |
      Resolve a channel's uploads playlist identifier.

      `channelID`: the UC… channel identifier
      `uploadsPlaylistID`: the uploads playlist identifier
      `err`: error if the channel is not found or the call fails

      Use `youtube-data-api` (channels.list, contentDetails) to read the uploads
      playlist id.
    "ListUploads(uploadsPlaylistID: string) -> videos:[]Video, err:error": |
      Enumerate a channel's uploads, newest first.

      `uploadsPlaylistID`: the uploads playlist identifier
      `videos`: uploaded `Video` items, newest first
      `err`: error if enumeration fails

      Use `youtube-data-api` (playlistItems.list, paginated) to collect items.
    "AddToPlaylist(playlistID: string, videoID: string) -> itemID:string, err:error": |
      Insert a video into a playlist.

      `playlistID`: the target playlist identifier
      `videoID`: the video to add
      `itemID`: the created playlist-item identifier
      `err`: error if the insert fails

      Use `youtube-data-api` (playlistItems.insert) to add the video.

      Constraints:
      - Do not deduplicate; the caller guarantees the video was not added before

---

Author: Goga
CreatedAt: 27/07/26
Description: |
  Google integration facade: OAuth2 credentials and YouTube Data API v3 access.
```

**`youtube/.usages/facade.md`** — consumer guide (syncer, cmd):

```markdown
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

## Add a video to a playlist

    itemID, err := youTube.AddToPlaylist(playlistID, videoID)

## Constraints for consumers
- Call AddToPlaylist only for videos already deduplicated upstream; the facade does
  not check for duplicates.
- Methods are safe for sequential calls within a single sync pass.
```

---

### 3.4 Cell `syncer`

**`syncer/CODEMANIFEST`**

```yaml
Imports:
  - Types:
      - ChannelMapping
    From: config
  - Types:
      - State
    From: state
  - Types:
      - YouTube
      - Video
    From: youtube

Annotations: |
  This cell orchestrates the per-channel sync: seed on first contact (add nothing),
  otherwise add only videos newer than the channel's watermark.

  Return errors following the (result, error) idiom.

---

"ChannelResult()":
  location: syncer.go
  annotations: |
    Outcome of syncing one channel, for logging.
  properties:
    "ChannelID -> string": |
      The channel identifier.
    "PlaylistID -> string": |
      The target playlist identifier.
    "Seeded -> bool": |
      True if this run only seeded the channel and added nothing.
    "NewCount -> int": |
      Number of new videos detected for the channel.
    "AddedIDs -> []string": |
      Identifiers of videos inserted this run.
    "Err -> error": |
      Error encountered for this channel, if any.

"SyncAll(youTube: YouTube, state: State, mappings: []ChannelMapping, dryRun: bool) -> results:[]ChannelResult, err:error":
  location: syncer.go
  annotations: |
    Sync every configured channel against its watermark.

    `youTube`: the YouTube facade
    `state`: the persisted cursor store
    `mappings`: the channel→playlist mappings
    `dryRun`: if true, detect only — perform no inserts and mutate no state
    `results`: one `ChannelResult` per mapping
    `err`: error on fatal failure

    Algorithm:
    1. For each `ChannelMapping` in `mappings`:
       1. Resolve the channel's uploads playlist via `YouTube`
       2. Enumerate uploads (newest first) as `Video` items via `YouTube`
       3. If the enumerated list is empty, record a `ChannelResult` with NewCount 0
          and continue to the next mapping without setting a cursor
       4. If the channel is not seeded in `State`:
          - build a `ChannelResult` marked seeded with NewCount 0
          - unless `dryRun`, set the channel's last-seen cursor to the newest `Video`
       5. Else:
          - read the watermark from `State`
          - select `Video` items whose PublishedAt is greater than the watermark
          - unless `dryRun`, insert each into the playlist via `YouTube`, oldest
            first, then set the channel's last-seen cursor to the newest `Video`
          - build a `ChannelResult` with NewCount and the added identifiers
    2. Return `results`

    Constraints:
    - When `dryRun`, perform no inserts and call no state-mutating operation
    - Never insert a `Video` whose PublishedAt is not greater than the watermark
    - A channel whose uploads list is empty is skipped: no cursor is set, no videos are added

---

Author: Goga
CreatedAt: 27/07/26
Description: |
  Per-channel sync orchestration with watermark-based new-video detection.
```

**`syncer/.usages/sync.md`** — consumer guide (cmd):

```markdown
# syncer — consumer guide

## Domain
How to drive the orchestrator (package `syncer`) and read its results.

## Run a sync

    results, err := syncer.SyncAll(youTube, state, mappings, dryRun)

- Pass dryRun=true to detect new videos without inserting and without mutating state.
- `State` is mutated in place during the call; the caller persists it afterwards.

## Read ChannelResult
Each result reports ChannelID, PlaylistID, Seeded (first-contact run, nothing
added), NewCount, AddedIDs, and Err (set if that channel failed; other channels are
still processed).
```

---

### 3.5 Cell `cmd`

**`cmd/CODEMANIFEST`**

```yaml
Imports:
  - Types:
      - LoadConfig
    From: config
  - Types:
      - State
    From: state
  - Types:
      - YouTube
    From: youtube
  - Types:
      - SyncAll
      - ChannelResult
    From: syncer

Annotations: |
  This cell is the CLI entry and composition root: it parses flags, wires the
  facade, state, and orchestrator, runs one sync pass, persists state, and logs
  results.

  Return errors following the (result, error) idiom.

---

"Run(configPath: string, secretsPath: string, tokenPath: string, statePath: string, redirectURL: string, dryRun: bool) -> err:error":
  location: main.go
  annotations: |
    Execute one sync pass end to end.

    `configPath`: path to the channel→playlist config
    `secretsPath`: path to client_secrets.json
    `tokenPath`: path to the cached OAuth token
    `statePath`: path to the state file
    `redirectURL`: loopback redirect URL for consent
    `dryRun`: if true, detect only
    `err`: error on fatal failure

    Algorithm:
    1. Parse command-line flags into the parameters above
    2. Load the channel→playlist mappings via `LoadConfig`(`configPath`)
    3. Construct `YouTube`(`secretsPath`, `tokenPath`, `redirectURL`)
    4. Construct `State`(`statePath`)
    5. Run `SyncAll` with the facade, state, the loaded mappings, and `dryRun`, collecting the outcomes
    6. Unless `dryRun`, save state
    7. Log each `ChannelResult` among the outcomes
    8. Return `err` on fatal failure

    Requirements:
    - A main entry point parses flags, calls Run, and maps `err` to a non-zero exit code

---

Author: Goga
CreatedAt: 27/07/26
Description: |
  CLI entry and composition root wiring config, facade, state, and orchestrator.
```

No `.usages/` file — `cmd` is the root with no consumers.

---

## 4. Verification checklist

| # | Check | Result |
|---|---|---|
| 1 | Completeness — all approved types present | ✅ 8/8 |
| 2 | DSL correctness — keys, signatures, structure | ✅ |
| 3 | Inter-cell consistency — Imports reference real cells/types | ✅ |
| 4 | Implementation order — dependents after dependencies | ✅ (§1) |
| 5 | No placeholders (TBD/TODO) | ✅ |
| 6 | Every `Imports.Types` used in body | ✅ syncer: ChannelMapping, State, YouTube, Video; cmd: LoadConfig, State, YouTube, SyncAll, ChannelResult |
| 7 | Every `Imports.Usages` referenced in an annotation | ✅ (none imported) |
| 8 | Every `Usages` practice referenced in an annotation | ✅ config: yaml; state: json; youtube: youtube-data-api, google-oauth2 |
| 9 | Algorithms present for routines/methods with logic | ✅ LoadConfig, State ctor/Save/SetLastSeen, YouTube ctor, SyncAll, Run |
| 10 | Annotations free of implementation detail | ✅ |
| 11 | Backtick references resolvable in-document | ✅ only params, imported/declared types, usages |
| 12 | `location` = bare filename, same level | ✅ config.go, state.go, youtube.go, syncer.go, main.go |
| 13 | No cross-imports (cycles) | ✅ (§2) |
| 14 | Embedding from Imports | n/a (none used) |
| 15 | Mutations from available types | n/a (none used) |
| 16 | Entity/Routine correctness | ✅ Entities have methods/properties; Routines have none |
| 17 | Base usages in all cells | ⚠️ See note |
| 18 | Base annotations in all cells | ⚠️ See note |
| 19 | Language correctness (Go naming, locations) | ✅ |

**Note on 17/18:** the project base usages (`youtube-data-api`, `google-oauth2`) and
base annotations are Google-API-specific. They are declared in the `youtube` cell
where they apply, and intentionally **not** forced into `config`/`state`/`syncer`/`cmd`,
which never call the Google APIs — forcing them would require artificial annotation
references and contradict checks 8 and 10.

## 5. Acceptance criteria coverage

| Task acceptance criterion | Covered by |
|---|---|
| `go build` → single static binary | `cmd/main.go` (package main) + 4 packages |
| Never-seen channel → adds nothing, records current uploads | `SyncAll` step 3.3 + `State.SetLastSeen`; seeded result |
| New video → inserted once, recorded; no duplicates | `SyncAll` step 3.4 + watermark; `State.SetLastSeen` advances cursor |
| Delete state → first-contact behavior again | `State` ctor treats missing file as empty → unseeded |
| No new videos → zero `playlistItems.insert` | `SyncAll` selects only PublishedAt > watermark |
| `--dry-run` → no inserts, no state change | `SyncAll` `dryRun` constraints; `Run` skips Save |
| API unit cost logged | `ChannelResult` per channel (NewCount/AddedIDs) logged by `Run` |
| Secrets/token/state gitignored, mode 0600 | `State.Save` atomic + 0600; token via `google-oauth2` usage (0600) |
