# Design Document: `shorts-detection`

Source: [`docs/tasks/shorts-detection.md`](../tasks/shorts-detection.md) ·
Architecture: [`docs/arch/shorts-detection.md`](../arch/shorts-detection.md)

Goal: accurate Short classification in `youtube.FilterRegularVideos` via an
unauthenticated `HEAD youtube.com/shorts/{id}` probe, with a duration (≤180s)
fallback when the probe is inconclusive. Stream/premiere detection unchanged.

> **Workflow note.** The CODEMANIFEST changes are *planned* in the architecture
> plan (Edits A–D), not yet applied to the working tree. This design is derived
> from those planned changes. Application of both the contract and the code is
> performed by `/goga:apply`. `goga lint`/`goga contract` currently pass on the
> unmodified tree and will be re-run after apply.

## Contract Changes

### Changed CODEMANIFEST Files
- `youtube/CODEMANIFEST` — `Usages` gains `youtube-shorts-detection`; global
  `Annotations` add the probe line and qualify "Data API"; `YouTube` construction
  `Algorithm` gains step 3; `FilterRegularVideos` annotation rewritten. No
  signature change. No new CODEMANIFEST files.

### New Entities
- None at the contract level. The probe helper `isShort`, the `shortsHTTP` client,
  the `shortsBase` field, and the test seam are **unexported** — Go manifests list
  exported identifiers only, so they are implementation detail, not contract.

### Changed Entities
- `YouTube` (entity) — construction now also builds an unauthenticated,
  redirect-blocking probe client (Algorithm step 3). No new exported methods/properties.
- `YouTube.FilterRegularVideos` (method) — behavior changes (HEAD primary +
  duration fallback); signature `([]Video) -> ([]Video, error)` unchanged.

### Deleted Entities
- None.

### Usages and Annotations Changes
- `youtube-shorts-detection` added to `Usages` (path `.goga/usages/cooks/youtube-shorts-detection.md`, already created).
- Global `Annotations`: new line `Use \`youtube-shorts-detection\` to classify Shorts…` and `youtube-data-api` qualified to "every Data API call".
- `FilterRegularVideos` annotation: new 4-step `Algorithm:` + `Requirements:`/`Constraints:`.

## Applied Fixes

### Fixed CODEMANIFEST Defects
- None. The planned changes were validated analytically (architecture plan Phase 6):
  all backtick references resolve; base usages `youtube-data-api` and
  `google-oauth2` remain declared and referenced; Go naming and `location: youtube.go`
  are correct; leaf cell, no cross-imports.

## Entity Interaction and Data Flow

### Interaction Diagram
```
syncer.syncOne
   │ calls (signature unchanged)
   ▼
YouTube.FilterRegularVideos(videos []Video)
   │
   ├──► videos.list(contentDetails, liveStreamingDetails)   ──▶ YouTube Data API v3  [youtube-data-api]  (OAuth client)
   │       returns per-id {duration, liveStreamingDetails}
   │
   └──► for each non-stream video:
          YouTube.isShort(id)  (unexported)
            │ HEAD {shortsBase}/shorts/{id}
            ▼
            youtube.com  (UNAUTHENTICATED, redirect-blocking)   [youtube-shorts-detection]
            │
            ├─ 200      → Short   (drop)
            ├─ 3xx/404  → regular (keep)
            └─ err      → FALLBACK: duration ≤ 180s ? drop : keep
```

### Data Flows
1. **Classification flow** — `syncer` passes the new-since-watermark `[]Video`
   (newest-first) into `FilterRegularVideos`. The facade fetches duration + stream
   flags in one batched `videos.list` pass, drops streams, then probes each
   remaining id. Output is the regular subset in input order, returned to `syncer`
   for playlist insertion.
2. **Probe flow** — each probe is an independent `HEAD` to `shortsBase+"/shorts/"+id`.
   No body read. The immediate (pre-redirect) status is the sole signal.
3. **Fallback flow** — on probe transport error, the already-fetched duration
   decides: ≤180s treated as a Short, else kept.

### Entity Dependencies
- `YouTube` depends on: the OAuth `youtubev3.Service` (Data API), and the new
  unauthenticated `shortsHTTP`/`shortsBase` pair.
- `syncer` depends on `youtube.Video` and `youtube.YouTube` — **unchanged**
  (signature preserved).
- Initialization order: `NewYouTube` constructs the OAuth client (`google-oauth2`),
  the Data API service, **and** the probe client + base (defaults). Test seam
  `newWithShorts` overrides the probe pair.

## Code Stack Trace

### Trace: `YouTube.FilterRegularVideos`

#### Chain
1. **Input** — `videos []Video` (newest-first; empty allowed). → checkpoint: type `[]Video`, ok.
2. **Empty short-circuit** — if `len(videos)==0` → return `(nil, nil)`, no API/probe. → checkpoint: no network on empty (existing contract preserved).
3. **Batch fetch meta** — collect ids; for each ≤50-id batch, `videos.list(contentDetails, liveStreamingDetails)` via `withRetry`. Build `meta[id] = {dur, durOK, stream}`. → checkpoint: uses `youtube-data-api`; on `withRetry` failure return `(nil, err)` (existing behavior).
4. **Walk videos in order** — for each `v`:
   - `v.ID` not in `meta` (deleted/private since listing) → **drop**. → checkpoint: matches existing "absent ⇒ dropped".
   - `meta[v.ID].stream` (liveStreamingDetails present) → **drop**. → checkpoint: stream logic unchanged.
   - else probe: `isShort(ctx, v.ID)` → checkpoint: returns `(bool, error)`.
     - `err != nil` → **fallback**: if `durOK && dur ≤ 180s` → drop (Short); else keep.
     - `true` → drop (Short).
     - `false` → keep.
5. **Output** — `regular []Video`, kept videos in original (newest-first) order. → checkpoint: order preserved; type `[]Video`; errors follow `(result, error)`.

#### Checkpoint Summary
- Empty input ⇒ no network: **passed** (early return).
- Stream drop behavior: **passed** (unchanged branch).
- Probe result type `(bool, error)`: **passed** — consumer handles all three cases.
- Fallback uses already-fetched duration (no second API call): **passed**.
- Order preservation: **passed** (single in-order walk).

### Trace: `YouTube.isShort` (unexported)

#### Chain
1. **Input** — `ctx`, `id`. → checkpoint: `id` is a non-empty video id.
2. **Build request** — `http.NewRequestWithContext(ctx, HEAD, shortsBase+"/shorts/"+id, nil)`. → checkpoint: method HEAD; URL well-formed.
3. **Send** — `shortsHTTP.Do(req)` (client does **not** follow redirects). → checkpoint: redirect-blocking (redirect trap).
4. **Interpret immediate status** — `resp.StatusCode == 200` ⇒ `(true, nil)`; else `(false, nil)`; transport error ⇒ `(false, err)`. `defer resp.Body.Close()`.
5. **Output** — `(isShort, err)` to `FilterRegularVideos`. → checkpoint: `err != nil` ⇒ caller falls back; never aborts.

#### Checkpoint Summary
- Redirect trap: **passed** — `CheckRedirect ⇒ http.ErrUseLastResponse`; a 3xx is read as "not a Short" without following to a 200.
- Failure surfacing: **passed** — error distinguishes "inconclusive" (err) from "regular" (false, nil).

### Trace: `YouTube` construction (`NewYouTube` / `newWithService` / `newWithShorts`)

#### Chain
1. **Input** — secrets/token/redirect paths (production) or an existing `*youtubev3.Service` (tests).
2. **OAuth client** — via `google-oauth2` (cached token or consent flow). → unchanged.
3. **Data API service** — `youtubev3.NewService`. → unchanged.
4. **Probe client + base** — set `shortsHTTP = defaultShortsClient()` (redirect-blocking, ~10s timeout) and `shortsBase = defaultShortsBase` ("https://www.youtube.com"). `newWithShorts(svc, base, httpc)` overrides both for tests.
5. **Output** — `*YouTube` safe for sequential use across one sync pass. → checkpoint: no new exported constructor parameter; `NewYouTube` signature unchanged.

## Algorithm Design

### `YouTube.FilterRegularVideos`

**Responsibility**: drop Shorts and live streams from a candidate set, returning
only regular long-form uploads in input order.

**Algorithm:**
```
1. IF videos is empty → return (nil, nil)
2. ids ← idsOf(videos)
3. meta ← {}
   FOR each batch of ≤50 ids:
     resp ← videos.list(contentDetails, liveStreamingDetails)        [youtube-data-api]
     FOR each item in resp:
       dur, ok ← parseISODuration(item.contentDetails.duration)
       meta[item.id] ← {dur, durOK: ok, stream: item.liveStreamingDetails != nil}
4. regular ← []
   FOR each v in videos (in order):
     m, exists ← meta[v.ID]
     IF not exists → CONTINUE                      # deleted/private: drop
     IF m.stream → CONTINUE                         # live stream / premiere: drop
     isShort, err ← isShort(ctx, v.ID)              # HEAD probe [youtube-shorts-detection]
     IF err != nil:                                 # probe inconclusive → fallback
       IF m.durOK AND m.dur ≤ 180s → CONTINUE       # fallback Short
       ELSE → regular ← append(regular, v)          # keep (incl. unparseable duration)
     ELSE IF isShort → CONTINUE                     # probe says Short
     ELSE → regular ← append(regular, v)            # probe says regular
5. RETURN (regular, nil)
```

**Errors:**
- `videos.list` failure (after `withRetry`) → `(nil, err)` propagated to `syncer`, which records a per-channel error. Unchanged.
- Probe failure → **never** an error to the caller; resolved by the duration fallback in-step.

**Edge Cases:**
- Empty input → `(nil, nil)`, no calls.
- Video absent from `videos.list` response → dropped (cannot be added anyway).
- Duration unparseable (`durOK == false`) + probe error → **kept** (do not drop on a guess — existing contract).
- Probe says regular but the video is actually a Short in the 61–180s band with a working probe → correctly dropped by the probe (the whole point).
- Probe says regular via 3xx → the redirect is **not** followed.

### `YouTube.isShort` (unexported)

**Responsibility**: classify one video id as a Short via the `/shorts/{id}` probe.

**Algorithm:**
```
1. req ← HEAD {shortsBase}/shorts/{id}   (context-aware)
2. resp ← shortsHTTP.Do(req)             (NO redirect following)
3. IF transport error → RETURN (false, err)
4. ok ← (resp.StatusCode == 200)
5. close resp.Body
6. RETURN (ok, nil)
```

**Errors:**
- Transport/timeout/5xx-as-error path → `(false, err)` ⇒ caller falls back.

**Edge Cases:**
- 3xx (redirect to `/watch`) → `false` (regular) — must not follow.
- 404 → `false` (regular).
- 200 → `true` (Short).

### `defaultShortsClient` / construction

**Responsibility**: build the production probe client.

**Algorithm:**
```
defaultShortsClient():
  RETURN &http.Client{
    CheckRedirect: (_, _) → http.ErrUseLastResponse,
    Timeout: 10s
  }
```
Construction sets `shortsBase = "https://www.youtube.com"` and
`shortsHTTP = defaultShortsClient()` unless overridden by `newWithShorts`.

## Cross-cutting Concerns

- **Error handling**: probe failures are **non-fatal** — resolved locally by the
  duration fallback; never surfaced to the caller, never abort the channel. Only
  `videos.list` (Data API) failures propagate as `(nil, err)`. The `(result, error)`
  idiom is preserved on `FilterRegularVideos`.
- **Logging**: none added at the facade; `syncer` continues to log per-channel
  outcomes. (The probe is silent on success/fallback — fallback is a normal path.)
- **Validation**: `id` is taken from `videos.list` output, always a valid non-empty
  id. Malformed URL is impossible (constant base + path join of an id). `parseISODuration` validation reused as-is.
- **Caching**: none. Probes are per-video per-sync; the watermark guarantees only
  genuinely-new videos are probed, bounding volume.
- **Concurrency**: **sequential** probes within one `FilterRegularVideos` call.
  Safe for sequential use across one sync pass (matches the existing facade
  contract). Concurrent probing is explicitly out of scope.

## Usages Analysis

### `youtube-data-api`
- **What it provides**: the authenticated YouTube Data API v3 client operations (here: `videos.list` with `contentDetails` + `liveStreamingDetails`).
- **Where used**: `FilterRegularVideos` step 3 (batched ≤50 ids, via `withRetry`).
- **Why chosen**: the only way to fetch duration (fallback) and `liveStreamingDetails` (stream detection). Base annotation mandates it for every Data API interaction.
- **How exactly**: `svc.Videos.List(["contentDetails","liveStreamingDetails"]).Id(batch...).Do()`, paginated in batches of 50; durations parsed with `parseISODuration`.

### `google-oauth2`
- **What it provides**: the authenticated `*http.Client` backing the Data API service.
- **Where used**: `YouTube` construction (step 2). Unchanged by this design.
- **Why chosen**: base annotation; the Data API requires it. (The probe client is deliberately **not** OAuth — it is unauthenticated.)
- **How exactly**: unchanged — cached-token reuse / consent flow.

### `youtube-shorts-detection`
- **What it provides**: the unofficial, unauthenticated `HEAD /shorts/{id}` probe; the redirect-blocking client requirement; the immediate-status decision table; the duration-fallback contract.
- **Where used**: `YouTube.isShort` (probe), `FilterRegularVideos` (fallback policy), construction (probe client).
- **Why chosen**: the Data API has no `isShort` field (Issue Tracker #232112727); this usage is the only reliable detection method and documents its caveats.
- **How exactly**: redirect-blocking `*http.Client`; `200` ⇒ Short, `3xx`/`404` ⇒ regular, transport error ⇒ inconclusive ⇒ caller applies the ≤180s fallback.

### Imported Usages
- None. `youtube` is a leaf cell with no `Imports`.

## `.usages/` Update

### Cell: `youtube`

#### Existing Files — Consistency
- **`facade.md`** → `youtube/.usages/facade.md`
  - Status: **outdated** — states "Removes Shorts (duration ≤ 60s)…".
  - Updates needed: replace the "Keep only regular videos" paragraph to describe
    the probe-primary + duration-fallback behavior (see architecture plan §2.2).
    The code snippet is unchanged (signature preserved).
- **No new `.usages/` files** — the change is within the existing "regular videos"
  domain; no new functional domain is introduced (per cookbook: supplement, do not split).

## Test Stack Trace

### General Setup
- Tests live in `youtube_test` (external) via the existing `newTestYouTube` helper.
- **New helper `newTestYouTubeWithShorts(t, dataHandler, shortsHandler)`**: spins up
  **two** `httptest.Server`s — one for the Data API (wired via
  `option.WithEndpoint` + `youtubev3.NewService`, as today) and one for the probe.
  Builds a **redirect-blocking** client for the probe server
  (`CheckRedirect ⇒ http.ErrUseLastResponse`, transport = shorts server's), and
  constructs the facade via the new unexported `newWithShorts(svc, shortsBase=shortsTS.URL, shortsHTTP=thatClient)`.
- `newWithShorts` is re-exported for the external test package via a new
  test-only var in `export_test.go` (e.g. `var NewWithShorts = newWithShorts`),
  mirroring the existing `NewWithService` alias.
- **Migration**: the 3 existing `FilterRegularVideos` tests move from
  `newTestYouTube` to `newTestYouTubeWithShorts` (they now exercise the probe).
  `TestFilterRegularVideos_EmptyInput_NoServerHit` may stay on `newTestYouTube`
  (empty input returns before any probe), but migrating it is harmless.
  Unrelated tests (~14) keep using `newTestYouTube` unchanged.

### Source File Registry
- `youtube/youtube.go` — `YouTube` struct, `FilterRegularVideos`, new `isShort`,
  new `defaultShortsClient`, `defaultShortsBase`; `newWithService`/`newWithShorts`.
- `youtube/export_test.go` — add `NewWithShorts` alias.
- `youtube/youtube_test.go` — `newTestYouTubeWithShorts` helper; migrated + new tests.
- `youtube/CODEMANIFEST` — Edits A–D (applied in `/goga:apply`).
- `youtube/.usages/facade.md` — paragraph update.

---

### Positive Tests

#### `TestFilterRegularVideos_DropsShortViaProbe`

**Setup**: `newTestYouTubeWithShorts`. Data API handler serves durations
(`v_regular` PT10M, `v_short` PT30S, neither carries liveStreamingDetails). Shorts
handler: `HEAD /shorts/v_short` → `200`; `HEAD /shorts/v_regular` → `302`
(`Location: /watch?v=v_regular`).

**Input**: `[]Video{{ID:"v_regular"}, {ID:"v_short"}}` (newest-first).

**Trace**:
```
FilterRegularVideos([v_regular, v_short])
  → videos.list(contentDetails, liveStreamingDetails)  → meta{v_regular:{PT10M,ok,false}, v_short:{PT30S,ok,false}}
  → v_regular: not stream → isShort(v_regular)
       HEAD /shorts/v_regular → 302 (NOT followed) → (false, nil) → keep
  → v_short:   not stream → isShort(v_short)
       HEAD /shorts/v_short   → 200            → (true, nil)  → drop
  → return [v_regular]
```

**Assertions**:
```
len(got)==1 && got[0].ID=="v_regular"
err==nil
```

**Sufficiency**: proves a 61–180s Short is now caught by the probe (the core
regression — previously leaked because PT30S<60s only caught ≤60s, and a 90s Short
would have passed). Also proves the redirect (302) path is treated as regular
without following it.

#### `TestFilterRegularVideos_PreservesOrder`

**Setup**: all-regular; shorts handler returns `302` for every id. Data API returns `PT5M` each.

**Input**: `[a, b, c]` (newest-first).

**Trace**: each id probes → 302 → kept; order preserved.

**Assertions**: `len==3`, `got[0]=="a"`, `got[2]=="c"`.

**Sufficiency**: the in-order walk + append preserves newest-first input order (regression guard for the syncer's chronological insert).

#### `TestFilterRegularVideos_DropsStream`

**Setup**: Data API returns `v_stream` with `liveStreamingDetails`; shorts handler
not exercised for it.

**Input**: `[v_regular, v_stream]`.

**Trace**: `v_stream` dropped at the stream branch **before** any probe (proves
stream detection is unchanged and short-circuits the probe).

**Assertions**: `got==[v_regular]`.

**Sufficiency**: stream/premiere handling is unchanged by the refactor.

---

### Negative Tests

#### `TestFilterRegularVideos_FallsBackToDurationOnProbeError`

**Setup**: shorts server returns `503` (or closes the connection) for **all** ids
→ `isShort` returns `(false, err)`. Data API: `v_shortPT90S` (≤180s), `v_longPT10M` (>180s).

**Input**: `[v_shortPT90S, v_longPT10M]`.

**Trace**:
```
v_shortPT90S: isShort → 503 → err → fallback: dur 90s ≤180s → drop
v_longPT10M:  isShort → 503 → err → fallback: dur 600s >180s → keep
return [v_longPT10M]
```

**Assertions**: `got==[v_longPT10M]`, `err==nil` (probe error did NOT propagate).

**Sufficiency**: the chosen failure policy — probe inconclusive ⇒ duration
fallback, never abort — and that a 90s video is correctly treated as a Short by
the fallback (the modern Shorts max). Guards against regressing to "abort channel
on probe failure" or "60s cutoff."

#### `TestFilterRegularVideos_ProbeErrorUnparseableDuration_Keeps`

**Setup**: shorts server → `503` for `v_weird`; Data API returns `v_weird` with
`duration:"P1X"` (unparseable → `durOK==false`).

**Input**: `[v_weird]`.

**Trace**: `isShort → err`; fallback: `durOK==false` → **keep**.

**Assertions**: `got==[v_weird]`, `err==nil`.

**Sufficiency**: preserves the existing "do not drop on an unrecognised duration"
contract under the fallback path.

---

### Edge Case Tests

#### `TestFilterRegularVideos_ProbeRedirectNotFollowed` (redirect trap)

**Setup**: shorts handler: `HEAD /shorts/v_x` → `302` with
`Location: <shortsTS.URL>/watch?v=v_x`; a second handler rule serves `200` at
`/watch?v=v_x`. (If the client followed the redirect, it would see 200 → false
positive.) Data API: `v_x` PT10M.

**Input**: `[v_x]`.

**Trace**: `isShort(v_x)` → sees `302` (does **not** follow) → `(false, nil)` → keep.

**Assertions**: `got==[v_x]` (kept as regular).

**Sufficiency**: the correctness-critical redirect trap — the probe client must
not follow redirects, or every regular video is misclassified as a Short.

#### `TestFilterRegularVideos_VideoAbsentInList_Dropped`

**Setup**: Data API omits `v_gone` from the response (deleted/private between
listing and classify). Shorts handler unused for it.

**Input**: `[v_present, v_gone]` where only `v_present` appears in the list response.

**Trace**: `v_gone` not in `meta` → dropped before probe.

**Assertions**: `got==[v_present]`.

**Sufficiency**: the "absent ⇒ dropped" contract is preserved (and the probe is not
fired for a video that cannot be added anyway).

#### `TestFilterRegularVideos_EmptyInput_NoHit`

**Setup**: neither server should be hit.

**Input**: `nil`.

**Trace**: early return `(nil, nil)`.

**Assertions**: `got==nil`, `err==nil`, no Data API call, no probe.

**Sufficiency**: preserves the empty-input short-circuit (no wasted calls).

#### `TestContractSurface` (existing — must stay green)

**Setup**: compile-time API shape check.

**Assertions**: `NewYouTube` signature `func(string,string,string)(*YouTube,error)`; `FilterRegularVideos` `func([]Video)([]Video,error)` unchanged.

**Sufficiency**: guarantees the public contract surface (and thus `goga contract`
and all `syncer`/`cmd` callers) is unaffected.

## Additional Instructions for the Implementation Agent

- **Do not follow redirects** in the probe client (`CheckRedirect ⇒ http.ErrUseLastResponse`). This is the single most correctness-critical detail; `TestFilterRegularVideos_ProbeRedirectNotFollowed` enforces it.
- **Probe failure is non-fatal.** Never return an error from `FilterRegularVideos`
  for a probe failure; apply the ≤180s duration fallback. Only `videos.list` errors propagate.
- **Keep `videos.list(contentDetails, liveStreamingDetails)`** — duration is still
  needed for the fallback; `liveStreamingDetails` for streams. Do not drop `contentDetails`.
- **Signature stability is mandatory**: `NewYouTube` and `FilterRegularVideos` must
  keep their current signatures (`TestContractSurface` and `goga contract` enforce it). Inject the probe base via the **additive** `newWithShorts` seam, not a new public constructor parameter.
- **`shortMaxDuration` 60s → 180s** (the fallback cutoff). `parseISODuration` is reused unchanged.
- **Sequential probes only.** No goroutines / errgroup. Concurrency tuning is out of scope.
- Apply the 4 CODEMANIFEST edits (architecture plan §2.1) and the `facade.md`
  paragraph (§2.2) in the same change; then run `goga lint`, `goga contract`,
  `goga schema`, and `go test ./...` — all must pass.
- Unexported names (`isShort`, `shortsHTTP`, `shortsBase`, `newWithShorts`,
  `defaultShortsClient`, `defaultShortsBase`) must **not** appear in `youtube/CODEMANIFEST`.
