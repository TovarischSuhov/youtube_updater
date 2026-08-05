# Architecture Plan — Improve Shorts Detection

Source task: [`docs/tasks/shorts-detection.md`](../tasks/shorts-detection.md)

Goal: accurate Short classification in `youtube.FilterRegularVideos` via an
unauthenticated `HEAD youtube.com/shorts/{id}` probe, with a duration (≤180s)
fallback when the probe is inconclusive. Stream/premiere detection is unchanged.

## Design summary

- **One cell modified** (`youtube`), **no new cells**, **no new types**.
- `FilterRegularVideos`'s **signature is unchanged** → `syncer` and `cmd` are
  untouched; `goga contract` stays green.
- The HEAD probe, the `isShort` helper, the probe client, and the test seam are
  **unexported** → not in CODEMANIFEST (Go manifests list exported identifiers
  only). They are internal to the `youtube` package.
- The probe is a new external interaction, documented by the project-level usage
  `youtube-shorts-detection` (already created at
  `.goga/usages/cooks/youtube-shorts-detection.md` during task formulation).

## 1. Implementation order

Only one cell is touched; it is a leaf (no `Imports`).

| Order | Cell   | Reason                                     |
|-------|--------|--------------------------------------------|
| 1     | youtube | No dependencies; sole modified cell.       |

`syncer` (imports `youtube.Video`, `youtube.YouTube`) and `cmd` need no changes —
they consume `FilterRegularVideos` through its unchanged signature.

## 2. Artifacts

### 2.1 `youtube/CODEMANIFEST` — modification (diff)

**Edit A — `Usages` header: add the new practice**

```yaml
 Usages:
   youtube-data-api: .goga/usages/cooks/youtube-data-api.md
   google-oauth2: .goga/usages/cooks/google-oauth2.md
+  youtube-shorts-detection: .goga/usages/cooks/youtube-shorts-detection.md
```

**Edit B — global `Annotations`: add the probe line, qualify "Data API"**

```yaml
 Annotations: |
   This cell is the Google integration facade: it owns OAuth2 credentials and all
   YouTube Data API v3 calls, hiding pointer and HTTP types behind a string-based
   contract.

   Use `google-oauth2` to obtain the authenticated client and to cache and refresh
   the token during construction.
-  Use `youtube-data-api` for every API call.
+  Use `youtube-data-api` for every Data API call.
+  Use `youtube-shorts-detection` to classify Shorts; it probes youtube.com
+  directly (unauthenticated) and is distinct from the OAuth Data API.
   Return errors following the (result, error) idiom.
```

**Edit C — `YouTube` entity annotation: add Algorithm step 3**

```yaml
     Algorithm:
     1. On construction, obtain an authenticated HTTP client via `google-oauth2`
        (use the cached token at `tokenPath` if valid, otherwise run the consent
        flow and persist the token)
     2. Build the YouTube service client
+    3. Build an unauthenticated probe client for Shorts detection (see
+       `youtube-shorts-detection`)
```

**Edit D — `FilterRegularVideos` method annotation: rewrite (signature unchanged)**

Signature is unchanged — `FilterRegularVideos(videos: []Video) -> regular:[]Video, err:error`,
`location: youtube.go`. Replace the method annotation body with:

```yaml
   annotations: |
     Drop Shorts and live streams, returning only regular long-form uploads in
     their original (newest-first) order.

     `videos`: uploads to classify (e.g. the new-since-watermark subset)
     `regular`: the subset that are regular videos (Shorts/streams removed)
     `err`: error if classification fails

     Algorithm:
     1. Fetch duration and live-stream details via `youtube-data-api`
        (videos.list contentDetails + liveStreamingDetails), batched at ≤50 ids
        per call
     2. Drop any video carrying liveStreamingDetails (live, ended, or premiere)
     3. For each remaining video, classify it as a Short via
        `youtube-shorts-detection` (probe of /shorts/{id}); drop Shorts
     4. If the probe is inconclusive, fall back to duration: treat a video with
        duration ≤ 180s as a Short

     Requirements:
     - A video absent from the videos.list response (deleted or made private) is dropped
     - A video whose duration fails to parse is kept (not dropped on a guess)

     Constraints:
     - The probe failure path must never abort the channel; it falls back to duration
```

Footer (`Author: Goga`, `CreatedAt`, `Description`) and all other types
(`Video`, `ChannelRef`, `ParseChannelRef`, `ParsePlaylistID`, `ResolveUploads`,
`ListUploads`, `AddToPlaylist`, `ResolveNames`, `ResolveChannelRef`) are unchanged.

### 2.2 `youtube/.usages/facade.md` — modification

Rewrite the "Keep only regular videos (drop Shorts and streams)" section's
description paragraph:

```markdown
Removes Shorts and live streams (any video with live streaming details) and
returns the rest in input order. Shorts are classified by probing
`youtube.com/shorts/{id}` (HTTP 200 = Short); if that probe fails, the classifier
falls back to duration ≤ 180s. Pass the new-since-watermark subset so
classification batches only what may be added.
```

(The code snippet `regular, err := youTube.FilterRegularVideos(videos)` is
unchanged — the signature did not change.)

### 2.3 `.goga/usages/cooks/youtube-shorts-detection.md` — already exists

Created during task formulation; referenced by Edit A. No further changes.

## 3. Dependency map

```
cmd ──▶ syncer ──(Video, YouTube)──▶ youtube ──▶ YouTube Data API v3   [youtube-data-api]
 │         │                          │      └─▶ youtube.com/shorts/{id} [youtube-shorts-detection]  ← NEW external interaction
 │         └─▶ youtube ─────────────────┘
 └─▶ config, state
```

youtube remains a **leaf** (no `Imports`). No new cell-to-cell edges. The only
new edge is youtube → the unauthenticated `youtube.com/shorts/{id}` endpoint.

## 4. Verification checklist (post-implementation)

- [ ] `youtube/CODEMANIFEST` Edits A–D applied; `goga lint` exits 0.
- [ ] `goga contract` exits 0 (signature unchanged → must still pass).
- [ ] `goga schema` exits 0.
- [ ] `youtube/.usages/facade.md` paragraph updated.
- [ ] `go test ./...` passes, including new tests: HEAD `200`→dropped,
      `3xx`/`404`→kept, probe-error→duration(≤180s) fallback, stream detection
      unchanged.
- [ ] `youtube/export_test.go` `TestContractSurface` still green
      (`NewYouTube`/`NewWithService` constructor shape preserved — probe base
      injected additively, not via a new constructor parameter).
- [ ] No backtick reference to the unexported `shortMaxDuration`/`isShort` in the
      manifest (value stated literally as "≤ 180s").

## 5. Acceptance-criteria trace (from the task)

| Task acceptance criterion | Covered by |
|---|---|
| `go test ./...` passes incl. new HEAD/fallback tests | Plan §2.1 Edit D algorithm + §4 checklist |
| 61–180s Shorts no longer synced | Edit D Algorithm steps 3–4 (probe catches them) |
| `FilterRegularVideos` signature unchanged | Edit D explicitly preserves signature; §1 notes syncer/cmd untouched |
| `goga lint`/`contract`/`schema` exit 0 | §4 checklist; leaf cell, signature unchanged |
| `FilterRegularVideos` annotation describes HEAD + fallback, references `youtube-shorts-detection` | Edit D |
| `TestContractSurface` stays green | §4 checklist; constructor signature preserved |

All criteria are satisfiable within this single-cell modification.
