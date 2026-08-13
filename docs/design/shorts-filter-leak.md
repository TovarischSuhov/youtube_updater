# Design Document: `shorts-filter-leak`

Fail-closed Shorts probe: only an HTTP `200` classifies a Short; every other
probe outcome falls back to the duration heuristic. Derived from
`docs/arch/shorts-filter-leak.md`; the CODEMANIFEST change is already applied and
lint-clean.

## Contract Changes

### Changed CODEMANIFEST Files
- `youtube/CODEMANIFEST`: `FilterRegularVideos` annotation — Algorithm step 3/4
  narrowed to "`200` ⇒ Short; anything else ⇒ inconclusive ⇒ duration fallback";
  one new `Constraints` bullet pins the fail-closed rule. (`git diff`: +7 −3.)

### New Entities
- None.

### Changed Entities
- `FilterRegularVideos` — annotation only; signature
  `(videos: []Video) -> regular:[]Video, err:error` is unchanged. `isShort` is
  unexported and not in the contract; its behavior change is an implementation
  detail specified in §Algorithm Design.

### Deleted Entities
- None.

### Usages and Annotations Changes
- `youtube-shorts-detection` usage: Scenario 1 status table collapses to `200`
  vs. everything-else; `isShort` sketch returns an error on non-`200`; new
  "Why non-200 is not a regular signal" section added (see §Usages Analysis).
- `FilterRegularVideos` `Constraints`: +1 bullet (the fail-closed rule).

## Applied Fixes

### Fixed CODEMANIFEST Defects
- None. The annotation edit passed `goga lint` (`cells: 5, errors: 0`) on first
  application; backtick refs `youtube-shorts-detection` and `youtube-data-api`
  resolve against declared `Usages`.

## Entity Interaction and Data Flow

### Interaction Diagram
```
syncer.syncOne ──(videos []Video, signature only)──► youtube.FilterRegularVideos
                                                          │
                                                          ├─ videos.list ─► metaByID{dur,stream}   [youtube-data-api]
                                                          │
                                                          └─ per non-stream video ─► isShort(id)   [youtube-shorts-detection]
                                                                200        → Short  → drop
                                                                any other  → err    → duration fallback
                                                                                          ≤180s → drop
                                                                                          >180s/!durOK → keep
                                                          ▼
                                                     regular []Video ──► AddToPlaylist
```

### Data Flows
- **Classify-and-drop**: `videos` → meta fetch → per-video probe → keep/drop →
  `regular`. No type crosses the cell boundary except `[]Video`.

### Entity Dependencies
- `youtube` is a leaf (no `Imports`). `syncer` and `cmd` depend on `youtube` by
  signature only; the signature is unchanged, so neither is affected.

## Code Stack Trace

### Trace: `FilterRegularVideos`
1. **Input**: `videos []Video` (new-since-watermark subset, newest-first).
2. Build `ids` from `videos`.
3. Batched `videos.list(contentDetails, liveStreamingDetails)` ≤50 ids
   (`youtube-data-api`, `withRetry`) → `metaByID[id] = {dur, durOK, stream}`.
   → checkpoint: absent id ⇒ later dropped. ✓
4. For each `v` in `videos` (input order, to preserve newest-first):
   - `m, ok := metaByID[v.ID]`; `!ok` → drop (deleted/private). ✓
   - `m.stream` → drop (live/premiere). ✓
   - `isShort, err := y.isShort(ctx, v.ID)`:
     - `err != nil` → fallback: `m.durOK && m.dur ≤ 180s` → drop; else keep.
     - `isShort` → drop.
     - `default` (false,nil) → keep.
5. **Output**: `regular []Video` in input order; `err` nil unless `videos.list` failed.

#### Checkpoint Summary
- Type flow `isShort → (bool,error)` matches the switch arms. ✓
- Probe error is **deliberately not propagated**: it selects the fallback branch
  and is discarded; `FilterRegularVideos` returns nil error. ✓ (matches
  Constraint "probe failure path must never abort the channel".)
- **Defect surfaced (implementation, not contract)**: `isShort` currently returns
  `(false, nil)` for `3xx`/`4xx`, hitting the `default` keep branch — contradicting
  the new Constraint. Fix in §Algorithm Design (`isShort`).

## Algorithm Design

### `YouTube.isShort` (unexported — the implementation locus)

**Responsibility**: classify one video as a Short via the unauthenticated
`HEAD {shortsBase}/shorts/{id}` probe, without following redirects.

**Algorithm:**
```
1. Build HEAD request to {shortsBase}/shorts/{id}
2. y.shortsHTTP.Do(req)            # redirect-blocking client (unchanged)
   - transport error → return (false, err)            # inconclusive
3. IF resp.StatusCode == 200:
     return (true, nil)                                # Short
   ELSE:
     return (false, inconclusiveError(status))         # ANY non-200 → inconclusive
```
The current three-arm `switch` (200 / ≥500 / default→regular) **collapses** to a
single `if 200 … else error`: the former `5xx` arm and the former `default` arm
both become the inconclusive error.

**Errors:**
- `fmt.Errorf("youtube: shorts probe inconclusive (status %d)", code)` → consumed
  by `FilterRegularVideos`'s fallback branch; never reaches the caller as a
  returned error.

**Edge Cases:**
- `302 → /watch` (genuine regular): now inconclusive → fallback → duration decides.
  A regular video is `>180s` ⇒ kept (so `TestFilterRegularVideos_ProbeRedirectNotFollowed`
  still passes, via fallback rather than via "3xx=regular").
- `403`/`429` (bot block from a datacenter IP): inconclusive → fallback. A real
  Short (≤180s) ⇒ dropped (the leak fixed).
- `404`: inconclusive → fallback (no longer "regular"). Only occurs for non-Shorts;
  accepted over-drop of short regular videos per the fail-closed posture.

### `FilterRegularVideos`
No algorithm change — its loop already handles `err` via the duration fallback.
The contract annotation change (above) makes "inconclusive" mean *any non-200*.

## Cross-cutting Concerns
- **Error handling**: probe errors are swallowed into the fallback; only
  `videos.list` failure propagates as `FilterRegularVideos`'s error. Unchanged.
- **Logging**: slog is used elsewhere (`msg=channel`). The inconclusive status is
  not logged today; optional `slog.Debug("shorts probe inconclusive", "id", id,
  "status", code)` — not required by the contract, leave optional.
- **Concurrency**: probes stay sequential. The cook's future concurrency cap is
  out of scope.
- **Redirect trap**: `defaultShortsClient`'s `CheckRedirect → ErrUseLastResponse`
  is preserved unchanged; fail-closed changes status *interpretation*, not
  redirect-following.

## Usages Analysis

### `youtube-shorts-detection`
- **What it provides**: the unauthenticated `HEAD /shorts/{id}` probe technique
  and the duration fallback.
- **Where used**: `FilterRegularVideos` step 3 (annotation).
- **Why chosen**: the Data API has no `isShort` field; the probe is the only
  reliable positive signal.
- **How exactly**: `200 ⇒ Short`; **every other status and transport error ⇒
  inconclusive ⇒ duration ≤180s fallback**. The file must be updated to match
  (its current table says `3xx`/`404 → regular`, which is the bug).

### Imported Usages
- None (`youtube` has no `Imports`).

## `.usages/` Update

### Cell: `youtube`

#### Existing Files — Consistency
- **`facade.md`** → `youtube/.usages/facade.md`
  - Status: current (already says "HTTP 200 = Short; if that probe fails, falls
    back to duration ≤ 180s").
  - Updates needed: tighten "if that probe fails" → "if the probe does not return
    HTTP 200" for precision. One line.

#### New Files
- None. The probe internals belong to the project-level cook
  (`.goga/usages/cooks/youtube-shorts-detection.md`), not the consumer guide.

## Cook Update — `.goga/usages/cooks/youtube-shorts-detection.md`
- **Scenario 1 table**: collapse to `200 → Short`; `3xx`/`4xx`/`5xx`/transport
  error → inconclusive → duration fallback.
- **`isShort` sketch**: `return false, fmt.Errorf(...)` on non-`200`.
- **New section** "Why non-200 is not a regular signal": consent/region `3xx`,
  `403`/`429` bot-defense from datacenter IPs; a blocked response is
  indistinguishable from a genuine redirect at status-code level, so only `200`
  is a safe Short signal.
- **Scenario 2 lead-in**: widen "inconclusive" to cover any non-`200`.

## Test Stack Trace

### General Setup
- `newTestYouTubeWithShorts(t, dataHandler, shortsHandler)` — Data API + probe both
  pointed at `httptest` servers; probe client does not follow redirects.
  `writeVideosList(w, r, durFor, stream)` serves `/youtube/v3/videos`.

### Source File Registry
- `youtube/youtube.go` — `isShort`, `FilterRegularVideos`.
- `youtube/youtube_test.go` — new + existing tests.

---

### Positive / Regression Tests

#### `TestFilterRegularVideos_Probe3xx_FallsBackToDuration`

**Setup**: `durFor = {"v_short":"PT55S", "v_long":"PT10M"}`; shortsHandler returns
`302` (`http.Redirect`) for every `/shorts/{id}`.

**Input**: `[]Video{{ID:"v_short",...},{ID:"v_long",...}}`.

**Trace**:
```
FilterRegularVideos(in)
  → videos.list → v_short{dur:55s}, v_long{dur:600s}
  → v_short: isShort → probe 302 → err → fallback → 55≤180 → drop
  → v_long:  isShort → probe 302 → err → fallback → 600>180 → keep
  → regular = [v_long]
```

**Assertions**: `len(got)==1 && got[0].ID=="v_long"`; `err == nil`.

**Sufficiency**: reproduces the defect class (a real Short answered `3xx` from the
runner); proves the Short is now dropped via fallback, not leaked.

---

#### `TestFilterRegularVideos_Probe4xx_FallsBackToDuration`

**Setup**: same `durFor`; shortsHandler returns `403` (`w.WriteHeader`) for every
probe.

**Input**: same.

**Trace**:
```
v_short: isShort → probe 403 → err → fallback → 55≤180 → drop
v_long:  isShort → probe 403 → err → fallback → 600>180 → keep
```

**Assertions**: `len(got)==1 && got[0].ID=="v_long"`; `err == nil`.

**Sufficiency**: covers the `4xx` (bot-block/`403`/`429`) trigger path, the most
likely production cause; proves `4xx` no longer reads as "regular."

---

### Preserved Existing Tests (re-derived reasoning)

#### `TestFilterRegularVideos_ProbeRedirectNotFollowed`
Probe `302 → /watch` (which itself `200`s), `v1` dur `PT10M`. New path: `302` ⇒
inconclusive ⇒ fallback ⇒ `600s > 180s` ⇒ **kept**. Assertion unchanged (`got==[v1]`);
the redirect-blocking client is still required (following it would `200` ⇒ Short ⇒
wrongly dropped). **Still passes.**

#### `TestFilterRegularVideos_DropsShortViaProbe` / `..._DropsShortsAndStreams`
The `200`-probe path is unchanged ⇒ Shorts still dropped. Regulars that `302` are
`>180s` ⇒ kept via fallback. **Still pass.**

#### `TestFilterRegularVideos_FallsBackToDurationOnProbeError` / `..._ProbeErrorUnparseableDuration_Keeps`
Use `503`, already an error under both old and new code. **Still pass.**

No existing test combines a non-`200` probe with a ≤180s regular video expecting
*keep*, so none breaks under fail-closed.

## Additional Instructions for the Implementation Agent

- **`isShort`**: replace the `switch` with `if resp.StatusCode == 200 { return true,
  nil }; return false, fmt.Errorf("youtube: shorts probe inconclusive (status %d)",
  resp.StatusCode)`. Keep the redirect-blocking client (`defaultShortsClient`) and
  `defer resp.Body.Close()` exactly as-is.
- **Do not** touch `FilterRegularVideos`'s loop or signature; it already does the
  right thing with the error.
- **Cook**: update `.goga/usages/cooks/youtube-shorts-detection.md` per §Cook Update.
- **Consumer guide**: one-line tighten in `youtube/.usages/facade.md`.
- **Tests**: add the two regression tests above; run the full suite — all existing
  `FilterRegularVideos` tests must stay green.
- **Verify**: `go test ./...`; `goga lint`, `goga contract`, `goga schema` (exit 0);
  `youtube/export_test.go` `TestContractSurface` unchanged and green.
