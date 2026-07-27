# Architecture Plan — Manage Config Pairs (add/remove/list with names)

Modification to the implemented `youtube-updater` project. **No new cells, no new
dependency edges.** Changes three existing cells (`config`, `youtube`, `cmd`) and
one project usage (`youtube-data-api`). `state` and `syncer` are untouched, and the
sync path is unchanged. The modified CODEMANIFESTs and usage are **already
materialized on disk** and `goga lint`-clean (5 cells, 0 errors); this document is
the recorded plan + diffs.

Task: `docs/tasks/manage-config-pairs.md`.

---

## 1. Implementation order

Leaves first (no new deps), then the dependent cell.

| # | Cell / artifact | Reason |
|---|---|---|
| 1 | `config` | No Imports. Adds name fields + mutation/persistence routines. |
| 2 | `youtube` (+ `youtube-data-api` usage) | No Imports. Adds `ResolveNames`; usage gains the name-resolution scenario. |
| 3 | `cmd` | Depends on `config` and `youtube` (already); adds `RunAdd`/`RunRemove`/`RunList`. |

`config` and `youtube` are independent — either order. `cmd` follows both.

## 2. Dependency map

Unchanged from the existing architecture (no new edges):

```
 config ──┐                         state ──┐
          │                                  │
          ├─ ChannelMapping, LoadConfig,      └─ State
          │  AddMapping, SaveConfig, RemoveMapping
          ▲
          │
        syncer ── imports ── youtube (YouTube, Video)
          ▲                       ▲
          │                       │
        cmd ─── imports ──────────┘   (config: LoadConfig/AddMapping/SaveConfig/RemoveMapping;
                                       youtube: YouTube; syncer: SyncAll, ChannelResult)
```

`cmd`'s `config` import expands (adds `AddMapping`, `SaveConfig`, `RemoveMapping`).

---

## 3. Diffs

### 3.1 `config/CODEMANIFEST` (modified)

**`ChannelMapping`** — added two properties:
```yaml
"ChannelName -> string":  # channel display name; empty for legacy pairs
"PlaylistName -> string": # playlist title; empty for legacy pairs
```
**New routines:**
- `SaveConfig(path: string, mappings: []ChannelMapping) -> err:error` — marshal via `yaml`, write atomically; output readable by `LoadConfig`.
- `AddMapping(mappings, channelID, playlistID, channelName, playlistName) -> updated` — upsert (update in place if `channelID` exists, else append); idempotent.
- `RemoveMapping(mappings, channelID) -> updated` — drop by channel identifier; idempotent no-op if absent.

**Usages:** `yaml` updated — items now carry `channel_name`/`playlist_name`; "Emit writes the list back in the same shape."
**`LoadConfig`** — unchanged (signature stable; no ripple into `syncer`).

### 3.2 `youtube/CODEMANIFEST` (modified)

**`YouTube`** — added one method:
```yaml
"ResolveNames(channelID: string, playlistID: string) -> channelName:string, playlistName:string, err:error"
```
Reads `channels.list` `snippet.title` (channel) and `playlists.list` `snippet.title` (playlist). Fails if either is not found; the playlist must be public or owned.
`Video`, `ResolveUploads`, `ListUploads`, `AddToPlaylist` — unchanged.

### 3.3 `cmd/CODEMANIFEST` (modified)

**Imports:** `config` group expands to `LoadConfig, AddMapping, SaveConfig, RemoveMapping`.
**New routines:**
- `RunAdd(configPath, secretsPath, tokenPath, redirectURL, channelID, playlistID) -> err:error` — `LoadConfig` → `YouTube` → resolve names → `AddMapping` → `SaveConfig`. Requires OAuth; fails if channel/playlist not found.
- `RunRemove(configPath, channelID) -> err:error` — `LoadConfig` → `RemoveMapping` → `SaveConfig`. Offline; no-op if absent.
- `RunList(configPath) -> err:error` — `LoadConfig` → print each pair (id+name). Offline.

**`Run`** (sync) — unchanged. `main` dispatches on flags (`--add-channel`/`--add-playlist` → RunAdd; `--remove-channel` → RunRemove; `--list` → RunList; bare → Run), with paired/mutual-exclusivity validation.

### 3.4 `.goga/usages/cooks/youtube-data-api.md` (updated)

Added **Scenario 4 — Resolve channel and playlist names**: `channels.list`
`part=snippet` (`Snippet.Title`) and `playlists.list` `part=snippet` (`Snippet.Title`),
with the public/owned caveat and 1-unit cost each.

---

## 4. Verification checklist (Phase 6)

| # | Check | Result |
|---|---|---|
| 1 | Completeness — all approved types present | ✅ ChannelMapping(+2), SaveConfig, AddMapping, RemoveMapping, YouTube.ResolveNames, RunAdd, RunRemove, RunList |
| 2 | DSL correctness | ✅ `goga lint` 0 errors |
| 3 | Inter-cell consistency | ✅ no new edges; cmd import expanded |
| 4 | Implementation order | ✅ config/youtube → cmd |
| 5 | No placeholders | ✅ |
| 6 | Every `Imports.Types` used | ✅ cmd: LoadConfig, AddMapping, SaveConfig, RemoveMapping, State, YouTube, SyncAll, ChannelResult |
| 7 | `Imports.Usages` referenced | n/a (none) |
| 8 | Every `Usages` referenced in an annotation | ✅ config: yaml; youtube: youtube-data-api, google-oauth2 |
| 9 | Algorithms present | ✅ new routines + ResolveNames |
| 10 | No impl detail in annotations | ✅ |
| 11 | Backtick references resolvable | ✅ (property names left un-backticked) |
| 12 | `location` bare filename | ✅ config.go, youtube.go, main.go |
| 13 | No cross-imports | ✅ |
| 14–15 | Embedding / mutation | n/a (none) |
| 16 | Entity/Routine correctness | ✅ |
| 17–18 | Base usages/annotations | applied where relevant (youtube cell) — same documented decision as the first arch plan |
| 19 | Language correctness (Go) | ✅ |

## 5. Acceptance criteria coverage

| Task criterion | Covered by |
|---|---|
| `--add` fetches names, stores all four fields, exits 0 no sync | `RunAdd` (ResolveNames → AddMapping → SaveConfig) |
| Re-add updates playlist + refreshes names | `AddMapping` upsert |
| `--remove` drops pair; no-op if absent; offline | `RunRemove` (RemoveMapping, no auth) |
| `--list` prints pairs; offline | `RunList` (LoadConfig → print) |
| Bare run = sync, no regression | `Run` unchanged |
| Paired/mutual-exclusivity validation | `main` dispatch (annotation requirement) |
| Add against non-existent channel/playlist → error | `ResolveNames` failure propagates |
| config.yaml valid YAML after writes; LoadConfig reads names | `SaveConfig` requirement |
| Existing tests pass; new behavior tested | (implementation phase) |
