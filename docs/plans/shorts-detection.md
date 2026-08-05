# Plan: `shorts-detection`

## Purpose

Implement accurate Short classification in `youtube.FilterRegularVideos` via an
unauthenticated `HEAD youtube.com/shorts/{id}` probe, with a duration (≤180s)
fallback when the probe is inconclusive. Stream/premiere detection is unchanged.

After implementation, the `youtube` package classifies Shorts by probing
`/shorts/{id}` (HTTP 200 = Short; 3xx/404 = regular) instead of the brittle 60s
duration heuristic, and degrades gracefully to a ≤180s duration rule when the
probe fails. The most important contract-to-code gap: the public
`FilterRegularVideos` signature is **unchanged**, so `syncer`/`cmd` are untouched —
all new behavior is internal (unexported probe helper + client + test seam).

**Strategy**: one cell (`youtube`), three ordered tasks — (1) probe plumbing +
test seam, (2) `isShort` + restructured `FilterRegularVideos` with full TDD, (3)
`facade.md` consumer-doc update.

`CODEMANIFEST` files are **read-only**; the contracts already lead the code. The
`youtube/CODEMANIFEST` changes (Usages entry, global annotation line, construction
step 3, rewritten `FilterRegularVideos` annotation) are applied from
[`docs/arch/shorts-detection.md`](../arch/shorts-detection.md) by `/goga:apply`,
not by this plan. If implementation does not match the contract, fix the
implementation — never the contract.

Source design: [`docs/design/shorts-detection.md`](../design/shorts-detection.md).

## Context

### Contract Surface (changed entities only)

**Entity: `YouTube`**
- Type: entity (struct) — package `youtube`, facade = the package itself.
- Declared `location`: `youtube.go`.
- Facade obligation: importable from `youtube`; `NewYouTube` signature unchanged.
- Change: construction gains Algorithm step 3 — "Build an unauthenticated probe
  client for Shorts detection (see `youtube-shorts-detection`)".
- Annotation context (file → entity → method):
  - Global: "Use `youtube-shorts-detection` to classify Shorts; it probes
    youtube.com directly (unauthenticated) and is distinct from the OAuth Data
    API." + "Use `youtube-data-api` for every Data API call."
  - Entity `YouTube`: construction Algorithm now has 3 steps (OAuth client →
    service → probe client).

**Entity: `YouTube.FilterRegularVideos` (method)**
- Type: method on `YouTube`.
- Declared `location`: `youtube.go`.
- Signature (UNCHANGED): `FilterRegularVideos(videos []Video) -> regular:[]Video, err:error`.
- Semantic requirements (from the rewritten annotation):
  - Drop Shorts and live streams; return regular long-form uploads in input order.
  - Algorithm: (1) fetch duration + live-stream details via `youtube-data-api`
    (`videos.list contentDetails + liveStreamingDetails`, batched ≤50 ids);
    (2) drop videos carrying `liveStreamingDetails`; (3) classify each remaining
    video as a Short via `youtube-shorts-detection` (probe `/shorts/{id}`); drop
    Shorts; (4) if the probe is inconclusive, fall back to duration ≤ 180s.
  - Requirements: a video absent from the `videos.list` response is dropped; one
    whose duration fails to parse is kept.
  - Constraints: the probe failure path must never abort the channel — it falls
    back to duration.

### Re-exports
- None.

### Usages Context

- **`youtube-data-api`** — the authenticated Data API v3 client. Used here for
  `videos.list(contentDetails, liveStreamingDetails)` to fetch duration (fallback)
  and stream flags. Batched ≤50 ids via `withRetry`. Base annotation mandates it
  for every Data API interaction.
- **`google-oauth2`** — the authenticated client for the Data API service.
  Unchanged by this plan.
- **`youtube-shorts-detection`** — the unofficial, unauthenticated
  `HEAD /shorts/{id}` probe; mandates a **redirect-blocking** client; decision
  table: `200`⇒Short, `3xx`/`404`⇒regular, transport error⇒inconclusive⇒caller
  applies ≤180s fallback. This is the new usage driving the implementation.

### Imported Usages
- None. `youtube` is a leaf cell (no `Imports`).

### Local Usages

- **`youtube/.usages/facade.md`**
  - Functional category: consumer guide for the `youtube` facade.
  - Status: **needs supplement/update** — the "Keep only regular videos" section
    states "Removes Shorts (duration ≤ 60s)…", which is now outdated.
  - Related entities: `YouTube.FilterRegularVideos`.
  - Description: replace the paragraph to describe probe-primary + duration-fallback.
  - Creation task reference: **Task 3**.

### External Dependencies
- Standard library `net/http` (HEAD probe, redirect-blocking client). No new third-party packages.
- `google.golang.org/api/youtube/v3` (existing) for `videos.list`.
- `httptest` (existing, tests) for the probe test server.

## Facts

- `youtube.FilterRegularVideos` exists today (`youtube/youtube.go`), classifying
  Shorts purely by `duration ≤ shortMaxDuration` where `shortMaxDuration = 60s`.
- `parseISODuration` parses ISO-8601 durations; reused unchanged.
- `YouTube` is constructed via `NewYouTube` (production) and `newWithService(svc)`
  (tests). `export_test.go` aliases `var NewWithService = newWithService`.
- `TestContractSurface` asserts the public API shape, including
  `func([]Video)([]Video,error)` for `FilterRegularVideos` and
  `func(string,string,string)(*YouTube,error)` for `NewYouTube`. It does **not**
  reference `newWithService`/`NewWithService`.
- `newTestYouTube(t, h)` points only the Data API at an `httptest.Server`; the HEAD
  probe targets a different origin and is unreachable through it.
- Three existing `FilterRegularVideos` tests (`_DropsShortsAndStreams`,
  `_PreservesOrder`, `_EmptyInput_NoServerHit`) classify via the Data API duration
  field today.
- Baseline: `goga lint` 0 errors, `goga contract` `{}` (pass) on the unmodified tree.

## Gap Analysis

- **Missing behavior**: no HEAD-based Short detection; Shorts in the 61–180s band leak through.
- **Incorrect constant**: `shortMaxDuration` is 60s; the fallback must be 180s.
- **Missing plumbing**: no unauthenticated probe client, no `shortsBase`, no `isShort`.
- **Missing test seam**: no way to point the HEAD probe at a test server.
- **Behavioral mismatch risk**: existing `FilterRegularVideos` tests would make real
  network calls once the probe is added — they must migrate to a shorts-aware seam.
- **Outdated doc**: `facade.md` "duration ≤ 60s" description.
- **Reusable code**: `parseISODuration`, `withRetry`, `newTestYouTube`, `videoIDs`
  helper, and the `httptest` pattern are reused.

---

## Tasks

> **Package ordering rule**: all tasks are within the single `youtube` package.
> Task 1 (plumbing/seam) before Task 2 (behavior); Task 3 (docs) is independent.

### Task 1: `youtube` — probe client + additive test seam (infrastructure)

Add the unauthenticated, redirect-blocking probe client and an additive test seam,
without changing any public behavior. After this task `FilterRegularVideos` is
unchanged (still duration-based); the new struct fields are wired but unused by it.
This is pure plumbing so the behavior task (Task 2) can be built and tested.

**Usages relevant to this task:**
- `youtube-shorts-detection`: the probe client must be **redirect-blocking**
  (`CheckRedirect ⇒ http.ErrUseLastResponse`) with a ~10s timeout; `200`⇒Short,
  `3xx`/`404`⇒regular. The probe is unauthenticated and targets `youtube.com`
  directly — distinct from the OAuth Data API client.
- `google-oauth2`: unchanged — `NewYouTube` still obtains the authenticated Data API
  client via the OAuth flow; this task only adds the probe client/base alongside it.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [x] Add fields to the `YouTube` struct in `youtube/youtube.go`: `shortsHTTP *http.Client` (unauthenticated probe client) and `shortsBase string` (probe origin).
- [x] Add `const defaultShortsBase = "https://www.youtube.com"` in `youtube/youtube.go`.
- [x] Add `defaultShortsClient() *http.Client` returning a client with `CheckRedirect` returning `http.ErrUseLastResponse` and `Timeout: 10 * time.Second`.
- [x] Add unexported constructor `newWithShorts(svc *youtubev3.Service, shortsBase string, shortsHTTP *http.Client) *YouTube` setting `svc`, `shortsBase`, `shortsHTTP`.
- [x] Refactor `newWithService(svc)` to delegate: `return newWithShorts(svc, defaultShortsBase, defaultShortsClient())`.
- [x] Wire the same defaults in `NewYouTube` (it must construct the probe client/base alongside the OAuth service — see construction Algorithm step 3).
- [x] Add `var NewWithShorts = newWithShorts` to `youtube/export_test.go` (test-only alias, mirroring `NewWithService`).
- [x] Verify build + unchanged behavior: `go build ./...`
- [x] Verify facade/contract unchanged: `go test ./youtube/ -run TestContractSurface -count=1`
- [x] Verify existing tests still pass (no behavior change yet): `go test ./...`
- [x] Lint: `gofmt -w youtube/*.go youtube/export_test.go` and `go vet ./...`

### Task 2: `youtube` — `isShort` + `FilterRegularVideos` HEAD classification (TDD)

Implement the probe and restructure `FilterRegularVideos` to classify Shorts via
`HEAD /shorts/{id}` with a ≤180s duration fallback. Migrate the existing
`FilterRegularVideos` tests to the shorts-aware seam and add the new logic tests.
`FilterRegularVideos`'s public signature must remain `func([]Video) ([]Video, error)`.

**Usages relevant to this task:**
- `youtube-shorts-detection`: probe semantics — `HEAD {shortsBase}/shorts/{id}`;
  immediate `200`⇒Short, `3xx`/`404`⇒regular, transport error⇒inconclusive. The
  client does **not** follow redirects. On inconclusive → caller applies the ≤180s
  duration fallback; never abort.
- `youtube-data-api`: keep the `videos.list(contentDetails, liveStreamingDetails)`
  call — duration is needed for the fallback, `liveStreamingDetails` for streams.

**Interaction diagram (from the design — implement to match):**
```
syncer.syncOne ──(signature unchanged)──▶ YouTube.FilterRegularVideos(videos)
   │
   ├──► videos.list(contentDetails, liveStreamingDetails) ──▶ Data API v3 [youtube-data-api] (OAuth client)
   │       returns per-id {duration, liveStreamingDetails}
   └──► for each non-stream video: YouTube.isShort(id)  (unexported)
            │ HEAD {shortsBase}/shorts/{id}  (UNAUTHENTICATED, redirect-blocking) [youtube-shorts-detection]
            ├─ 200      → Short   (drop)
            ├─ 3xx/404  → regular (keep)
            └─ err      → FALLBACK: duration ≤ 180s ? drop : keep
```

**Verified trace (from the design — implement to match):**
```
FilterRegularVideos(videos):
  1. IF empty → return (nil, nil)
  2. ids ← idsOf(videos)
  3. meta ← {}
     FOR each ≤50-id batch: videos.list(contentDetails, liveStreamingDetails)
       meta[id] ← {dur, durOK, stream}
  4. FOR each v in videos (in order):
       m, ok ← meta[v.ID]
       IF !ok → CONTINUE                     # deleted/private: drop
       IF m.stream → CONTINUE                 # live stream / premiere
       isShort, err ← isShort(ctx, v.ID)
       IF err != nil:                         # probe inconclusive → fallback
         IF m.durOK && m.dur ≤ 180s → CONTINUE
         ELSE → keep
       ELSE IF isShort → CONTINUE
       ELSE → keep
  5. RETURN (regular, nil)
```

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [x] **STEP 0 — Declaration**: this task covers `youtube.YouTube.FilterRegularVideos` (method, `youtube.go`, signature unchanged) and the unexported `youtube.YouTube.isShort`.
- [x] **STEP 1 — Contract tests**: `TestContractSurface` already asserts `FilterRegularVideos`'s signature `func([]Video)([]Video,error)` and `NewYouTube`'s shape — run `go test ./youtube/ -run TestContractSurface -count=1` (must pass before and after).
- [x] **STEP 2 — Implementation**: add unexported method `isShort(ctx context.Context, id string) (bool, error)` — `HEAD {shortsBase}/shorts/{id}` via `shortsHTTP`; `200`⇒`(true,nil)`, other status⇒`(false,nil)`, transport error⇒`(false,err)`; `defer resp.Body.Close()`.
- [x] **STEP 2 — Implementation**: change `const shortMaxDuration` from `60 * time.Second` to `180 * time.Second`.
- [x] **STEP 2 — Implementation**: restructure `FilterRegularVideos` to the verified trace above (keep the batched `videos.list(contentDetails, liveStreamingDetails)` + `withRetry`; drop streams; probe each remaining video; apply ≤180s fallback on probe error; keep videos with unparseable duration).
- [x] **STEP 3 — Interface verification**: `go test ./youtube/ -run TestContractSurface -count=1` — signature/facade unchanged.
- [x] **STEP 4 — Logic tests**: add test helper `newTestYouTubeWithShorts(t, dataHandler, shortsHandler)` — two `httptest.Server`s; Data API via `option.WithEndpoint`; a **redirect-blocking** client (transport from the shorts server, `CheckRedirect ⇒ http.ErrUseLastResponse`) wired through `NewWithShorts(svc, shortsTS.URL, thatClient)`.
- [x] **STEP 4 — Logic tests**: migrate `TestFilterRegularVideos_DropsShortsAndStreams` and `TestFilterRegularVideos_PreservesOrder` to `newTestYouTubeWithShorts` (shorts handler: `200` for the short id, `302` for regular ids).
- [x] **STEP 4 — Logic tests (add)**: `TestFilterRegularVideos_DropsShortViaProbe` — `200`⇒dropped, `302`⇒kept.
- [x] **STEP 4 — Logic tests (add)**: `TestFilterRegularVideos_DropsStream` — `liveStreamingDetails` dropped before any probe.
- [x] **STEP 4 — Logic tests (add)**: `TestFilterRegularVideos_FallsBackToDurationOnProbeError` — shorts server returns `503` for all; `≤180s`⇒dropped, `>180s`⇒kept; `err==nil` (probe error not propagated).
- [x] **STEP 4 — Logic tests (add)**: `TestFilterRegularVideos_ProbeErrorUnparseableDuration_Keeps` — probe errors + duration unparseable ⇒ kept.
- [x] **STEP 4 — Logic tests (add)**: `TestFilterRegularVideos_ProbeRedirectNotFollowed` — shorts handler returns `302` with a `Location` that itself serves `200`; assert the video is kept (proves the redirect trap).
- [x] **STEP 4 — Logic tests (add)**: `TestFilterRegularVideos_VideoAbsentInList_Dropped` — id in input, missing from `videos.list` response ⇒ dropped, no probe.
- [x] **STEP 4 — Logic tests**: keep `TestFilterRegularVideos_EmptyInput_NoServerHit` (empty ⇒ nil, no calls).
- [x] **STEP 5 — Debugging**: `go test ./... -count=1` — fix implementation (not tests) until all pass, including `youtube/sync_integration_test.go`.
- [x] **STEP 6 — Contract re-verification**: `FilterRegularVideos` signature, facade accessibility, and behavior match the (read-only) CODEMANIFEST annotation; unexported `isShort`/`shortsHTTP`/`shortsBase` do NOT appear in CODEMANIFEST.
- [x] **STEP 7 — Lint**: `gofmt -w youtube/*.go` and `go vet ./...`; `goga lint`, `goga contract`, `goga schema` — all clean.

### Task 3: `youtube` — supplement `facade.md` consumer guide (docs)

Update the consumer guide so the Shorts-filtering description matches the new
probe-primary + duration-fallback behavior. Pure documentation; the code snippet
is unchanged (signature preserved).

**Usages relevant to this task:**
- `youtube-shorts-detection`: the behavior to describe (probe + fallback) for consumers.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them.**

- [x] In `youtube/.usages/facade.md`, replace the "Keep only regular videos (drop Shorts and streams)" description paragraph with: Shorts are classified by probing `youtube.com/shorts/{id}` (HTTP 200 = Short); if that probe fails, the classifier falls back to duration ≤ 180s. Removes Shorts and live streams (any video with live streaming details) and returns the rest in input order. Pass the new-since-watermark subset so classification batches only what may be added.
- [x] Keep the unchanged code snippet `regular, err := youTube.FilterRegularVideos(videos)`.
- [x] Verify: `goga lint` still clean (facade.md is consumer doc, not a contract source).

---

## Validation Commands

- `go build ./...`: compiles the module
- `go test ./... -count=1`: run all tests (contract + logic + integration)
- `go test ./youtube/ -run TestContractSurface -count=1`: facade/API shape unchanged
- `go vet ./...`: vet
- `gofmt -l youtube/*.go`: formatting clean (no output)
- `goga lint`: CODEMANIFEST syntax + lint, 0 errors
- `goga contract`: code matches contract (exit 0)
- `goga schema`: cell hierarchy parses (exit 0)

## Completion Criteria

- [x] Every contract entity is implemented in the correct `location` (`youtube.go`)
- [x] `FilterRegularVideos` accessible from the facade with signature `func([]Video) ([]Video, error)` (unchanged)
- [x] `FilterRegularVideos` behavior matches the annotation (probe primary, ≤180s fallback, streams dropped, order preserved, absent/unparseable handled)
- [x] `YouTube` construction builds the probe client + base (Algorithm step 3)
- [x] Every coding task followed the TDD workflow (contract tests → code → verification → logic tests → debugging → re-verification → lint)
- [x] Contract + logic tests cover facade, API shape, and behavior within Task 2
- [x] No package boundary was expanded (all changes inside `youtube`; `syncer`/`cmd` untouched)
- [x] `CODEMANIFEST` files were not modified (contract is read-only; applied via `/goga:apply`)
- [x] Unexported names (`isShort`, `shortsHTTP`, `shortsBase`, `newWithShorts`, `defaultShortsClient`, `defaultShortsBase`) do not appear in CODEMANIFEST
- [x] All validation commands pass
- [x] Every Usages entry (`youtube-data-api`, `google-oauth2`, `youtube-shorts-detection`) is referenced in at least one task
