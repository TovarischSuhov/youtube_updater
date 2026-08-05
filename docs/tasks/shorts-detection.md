# Improve Shorts Detection (HEAD Probe + Duration Fallback)

## Current State

Shorts/stream filtering landed in `cc2ab14` (`youtube.FilterRegularVideos`). Today a
video is classified as a **Short purely by duration ≤ 60s** (`shortMaxDuration`,
`youtube/youtube.go:117`), and as a live stream if it carries `liveStreamingDetails`.
Classification runs via `videos.list(contentDetails, liveStreamingDetails)`, batched at
≤50 ids, inside `FilterRegularVideos` (`youtube/youtube.go:128`).

The gap: YouTube Shorts run **up to 3 minutes (180s)**, so every Short in the 61–180s
band slips past the 60s cutoff and lands in the user's playlist. The cutoff also
wrongly drops any genuine long-form video that happens to be ≤60s.

**Hard constraint (verified):** the YouTube Data API v3 exposes **no `isShort` field**
— Google Issue Tracker #232112727 is still open. The only reliable detection is an
unofficial, unauthenticated `HEAD https://www.youtube.com/shorts/{id}` (200 = Short,
3xx/404 = regular). See `.goga/usages/cooks/youtube-shorts-detection.md`.

Coverage note: `shortMaxDuration` and the new HEAD path have **no covering tests**
today; `FilterRegularVideos` itself is exercised in `youtube/youtube_test.go`.

## Description

Replace the duration-only Short classification in `FilterRegularVideos` with the
accurate HEAD probe, and keep a duration heuristic **only as a fallback** when the
probe is inconclusive. Stream/premiere detection (`liveStreamingDetails`) is unchanged.

Per video (after dropping streams):
1. **Primary** — `HEAD /shorts/{id}`: immediate `200` → Short (drop); `3xx`/`404` →
   regular (keep).
2. **Fallback** — on probe error (network/timeout/5xx): treat duration ≤ 180s
   (`shortMaxDuration`, raised 60s → 180s) as a Short.
3. A video whose duration fails to parse is kept (defensive — same as today).

## Scope

**In scope:**
- Reimplement Short classification in `FilterRegularVideos` around the HEAD probe with
  the duration fallback (`shortMaxDuration` 60s → 180s).
- Add an unauthenticated `net/http` client (redirect-blocking) to the `youtube` cell
  with an **injectable base URL + client test seam** (mirrors the existing
  `newWithService` httptest pattern), so the HEAD path is unit-testable.
- Tests: HEAD `200`→dropped, HEAD `3xx`/`404`→kept, probe-error→duration-fallback
  applied, stream detection unchanged.
- Update the `youtube` **CODEMANIFEST** `FilterRegularVideos` annotation (what a Short
  is) and reference the new `youtube-shorts-detection` usage.
- Create `.goga/usages/cooks/youtube-shorts-detection.md` (done as part of this task).

**Out of scope:**
- Stream/premiere logic (already correct).
- Sync orchestration, state, config, `cmd`, CI — `FilterRegularVideos`'s signature is
  unchanged, so `syncer`/`cmd` callers are untouched.
- Any new CLI flag / configurable cutoff (a pure-HEAD approach was chosen).
- HEAD-request concurrency/backoff tuning (sequential is fine for current volume).

## Acceptance Criteria

- `go test ./...` passes, including new tests for HEAD `200`/`3xx`/`404` and the
  duration fallback on probe error.
- A Short in the 61–180s band is no longer synced (verified by a test that points the
  HEAD seam at an httptest server returning `200`).
- `FilterRegularVideos`'s public signature stays `([]Video) ([]Video, error)` —
  `syncer`/`cmd` compile unchanged.
- `goga lint`, `goga contract`, and `goga schema` all exit 0.
- The `youtube` CODEMANIFEST `FilterRegularVideos` annotation describes the HEAD
  primary + duration-fallback and references `youtube-shorts-detection`.
- `youtube/export_test.go` (`TestContractSurface`) stays green — the exported test seam
  shape (`NewWithService`) is preserved; the new seam is additive.

## Stack

- **Frameworks:** none (plain Go).
- **Libraries:** none new — standard library `net/http` for the HEAD probe.
- **Infrastructure:** none.
- **Tooling:** goga CLI (`lint`, `contract`, `schema`), the Cell DSL.

## External Dependencies

| Component                 | Usage file                                        | Status   |
|---------------------------|---------------------------------------------------|----------|
| youtube.com `/shorts/{id}`| `.goga/usages/cooks/youtube-shorts-detection.md`  | created  |
| youtube-data-api          | `.goga/usages/cooks/youtube-data-api.md`          | existing |
| google-oauth2             | `.goga/usages/cooks/google-oauth2.md`             | existing |

## Risks and Constraints

- **Redirect trap (correctness-critical):** the HEAD client must NOT follow redirects.
  A non-Short returns a 3xx to `/watch?v={id}` that ultimately 200s; following it
  makes every regular video read as a Short. Use `CheckRedirect → http.ErrUseLastResponse`.
- **Unofficial endpoint:** YouTube may change `/shorts/{id}` behaviour; the duration
  fallback is the mitigation. Detect drift via the tests' httptest harness, not live.
- **Per-video HTTP cost:** one HEAD per candidate video. Volume is bounded by
  new-since-watermark counts (normally a few). Sequential is acceptable now.
- **Probe-failure semantics:** inconclusive → fall back (chosen policy). Never abort
  the channel or silently treat failure as "Short".

## Scope Estimate

Single task — one cell (`youtube`), one method's internals, a test seam + tests, one
CODEMANIFEST annotation, one new usage file. No decomposition needed.

## Existing Architecture

Import graph: `cmd → {config, state, youtube, syncer}`; `syncer → {config, state,
youtube}`; `youtube →` external YouTube Data API v3 + OAuth2 (via the `cooks`), and now
also the unauthenticated `youtube.com/shorts/{id}` probe. `FilterRegularVideos` is
called only from `syncer/syncer.go` (`syncOne`); its signature is unchanged, so this is
an internal-only change to the `youtube` cell plus a contract-text update.

## Notes

Decisions locked during formulation:
- **Method:** HEAD `/shorts/{id}` (accurate), not a configurable cutoff or a pure
  duration widening.
- **Failure policy:** fall back to the duration rule (≤180s); do not fail the channel,
  do not silently keep.
- **Usage doc:** new dedicated `youtube-shorts-detection.md` (the HEAD method is a
  different system from the OAuth Data API), not a scenario appended to
  `youtube-data-api.md`.

### Target API (Go sketch)

```go
// defaultShortsBase is the web origin probed to classify Shorts. Unauthenticated and
// unofficial — see the youtube-shorts-detection usage. NOT the Data API.
const defaultShortsBase = "https://www.youtube.com"

// shortMaxDuration is the FALLBACK Short cutoff, used only when the HEAD probe is
// inconclusive: videos ≤ 180s (current Shorts max) are treated as Shorts. Coarse.
const shortMaxDuration = 180 * time.Second

// YouTube gains two fields for the probe (set in both NewYouTube and newWithService):
//   shortsHTTP *http.Client // redirect-blocking, with Timeout
//   shortsBase string       // default defaultShortsBase; overridable in tests

// isShort reports whether id is a Short. err != nil => probe inconclusive; the caller
// must fall back to the duration heuristic. Does NOT follow redirects.
func (y *YouTube) isShort(ctx context.Context, id string) (bool, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodHead, y.shortsBase+"/shorts/"+id, nil)
    if err != nil {
        return false, err
    }
    resp, err := y.shortsHTTP.Do(req)
    if err != nil {
        return false, err
    }
    defer resp.Body.Close()
    return resp.StatusCode == http.StatusOK, nil
}
```

`FilterRegularVideos` keeps the existing `videos.list(contentDetails,
liveStreamingDetails)` call (duration is needed for the fallback; `liveStreamingDetails`
for streams), then per kept video calls `isShort`; on error it applies the ≤180s
fallback, preserving today's "keep on unparseable duration" behaviour.

```go
for _, v := range videos {
    md, ok := meta[v.ID]
    if !ok {
        continue // deleted/private since listing: drop (as today)
    }
    if md.stream {
        continue // live stream / premiere
    }
    isShort, err := y.isShort(context.Background(), v.ID)
    switch {
    case err != nil:
        // Probe inconclusive → duration fallback.
        if md.durOK && md.dur <= shortMaxDuration {
            continue
        }
        out = append(out, v) // unrecognised duration: keep (as today)
    case isShort:
        continue
    default:
        out = append(out, v)
    }
}
```
