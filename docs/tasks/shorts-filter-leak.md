# Shorts Filter Leaks Real Shorts on Non-200 Probe Responses

## Current State

The Shorts filter shipped in `cc2ab14` and was upgraded to the HEAD-probe + duration
fallback in `631926c` (2026-08-05). It is live: the CI binary cache is keyed on
`hashFiles('**/*.go', …)` with a cold-cache recompile fallback (`.github/workflows/sync.yml`),
so a scheduled sync always runs the current source — the probe is present in the deployed
binary.

**Observed defect:** a real Short reached the target playlist.

- Video `823YBRAdPeA` — *"Новое слово в шумоизоляции"*, author **Интерьерно**, `lengthSeconds`
  **55** (a Short; `HEAD /shorts/823YBRAdPeA` answers `200`).
- Channel `UC2JzWVFH-iiehX-FXHkia4w` (Интерьерно) → playlist `PLPmI7JhdNPDE`.
- Published `2026-08-12T08:00:16Z` — **7 days after** the probe deployed, so the filter was
  running when this Short was synced. The channel watermark has since advanced past it
  (`last_seen_at 2026-08-13T06:45:30Z`), confirming it was processed and added by a run in
  that window. This is **not** a pre-filter historical artifact.

**Root cause (deductively pinned, not assumed).** `isShort` (`youtube/youtube.go`)
classifies the probe response as:

| Immediate status | `isShort` returns | Treated as |
|------------------|-------------------|------------|
| `200`            | `(true, nil)`     | Short (drop) |
| `3xx` / `4xx`    | `(false, nil)`    | **regular (keep)** |
| `5xx` / error    | `(false, err)`    | inconclusive → duration fallback |

For a 55s Short whose duration parses (`PT55S`, ≤180s), tracing `FilterRegularVideos` shows
**only one path keeps it**: the `default` branch — i.e. the probe returned `3xx` or `4xx`.
A `200` drops it; an error/`5xx` falls back to duration and `55 ≤ 180s` drops it. Therefore
**the probe returned `3xx`/`4xx` at sync time.**

From the production runtime (GitHub Actions `ubuntu-latest`, US datacenter IP) the unofficial
`youtube.com/shorts/{id}` endpoint answers **real Shorts** with a `3xx` (consent/region/bot
redirect) or a `4xx` (`403`/`429` block) — responses that say *"probe unusable,"* **not**
*"this is a regular video."* The code conflates the two, so the Short leaks. The `5xx`/error
→ duration-fallback path never fires for these statuses.

The `200 = Short` signal itself is sound (verified live: the probe returns `200` for this
video from a working network, under both the Go-client and a browser `User-Agent`). The bug
is treating every non-`200` as definitively regular.

## Description

Make `isShort` **fail closed**: `HTTP 200` is the only definitive Short signal. Every other
status (`3xx`, `4xx`, `5xx`) and every transport error is **inconclusive** — `isShort`
returns an error so the existing duration fallback in `FilterRegularVideos` decides. The
caller's classification loop is already correct and stays unchanged; only `isShort`'s status
handling moves.

Rationale (product risk bias): for a curated long-form playlist, a false negative (Short
leaks in) is the costly failure the user complained about; a false positive (a genuinely
short regular video dropped when the probe is broadly blocked) is benign. Failing closed
aligns with that bias and is robust to the exact triggering status.

## Scope

**In scope:**
- Reclassify non-`200` responses in `isShort` as inconclusive (return an error): merge the
  former `3xx`/`4xx` "regular" branch with the existing `5xx` inconclusive branch.
- A regression test asserting a Short is dropped via the duration fallback when the probe
  answers `3xx` and when it answers `4xx` (probe `302` and, e.g., `403`/`429`), using the
  existing `newTestYouTubeWithShorts` httptest seam.
- Update the `youtube` **CODEMANIFEST** `FilterRegularVideos` annotation: clarify that
  *only* an HTTP `200` means Short; any other status (or error) is inconclusive and falls
  back to the duration rule.
- Update `.goga/usages/cooks/youtube-shorts-detection.md`: the Scenario 1 status table
  (`3xx → regular`, `404 → regular`) is now wrong and must read `3xx`/`4xx` → inconclusive
  → duration fallback; add a "why non-200 is not a regular signal" note (consent/bot
  redirect, `403`/`429` from datacenter IPs).

**Out of scope:**
- The probe transport itself (`HEAD` method, redirect-blocking client, base URL) — the `200`
  signal is reliable; only status interpretation changes.
- `FilterRegularVideos`'s classification loop and signature — unchanged.
- Stream/premiere detection (`liveStreamingDetails`) — already correct.
- `syncer`, `cmd`, `config`, `state` — `FilterRegularVideos` keeps `([]Video) ([]Video, error)`.
- HEAD→GET, concurrency/backoff, or a configurable cutoff — not needed.
- Backfill/removal of `823YBRAdPeA` from the live playlist (the syncer only adds; manual
  cleanup is out of this code task).

## Acceptance Criteria

- `go test ./...` passes, including a new test: probe `3xx` → Short dropped via duration
  fallback, and probe `4xx` (`403` or `429`) → Short dropped via duration fallback.
- `FilterRegularVideos`'s public signature stays `([]Video) ([]Video, error)`; `syncer`/`cmd`
  compile unchanged; `youtube/export_test.go` `TestContractSurface` stays green.
- `goga lint`, `goga contract`, and `goga schema` all exit 0.
- The `youtube` CODEMANIFEST `FilterRegularVideos` annotation states that only HTTP `200`
  means Short and any other status falls back to duration.
- `youtube-shorts-detection.md` status table corrected to mark `3xx`/`4xx` as inconclusive.
- Existing redirect-trap coverage (`TestFilterRegularVideos_ProbeRedirectNotFollowed`) is
  preserved and still passes — fail-closed must not weaken the "do not follow redirects"
  guarantee (a `302` to `/watch` now reads inconclusive and falls back to duration rather
  than being read as regular; the video is still classified correctly because a regular
  video is `>180s` and is kept by the fallback).

## Stack

- **Frameworks:** none (plain Go).
- **Libraries:** none new — standard library `net/http`.
- **Infrastructure:** none.
- **Tooling:** goga CLI (`lint`, `contract`, `schema`), the Cell DSL.

## External Dependencies

| Component                 | Usage file                                        | Status   |
|---------------------------|---------------------------------------------------|----------|
| youtube.com `/shorts/{id}`| `.goga/usages/cooks/youtube-shorts-detection.md`  | updated  |
| youtube-data-api          | `.goga/usages/cooks/youtube-data-api.md`          | existing |
| google-oauth2             | `.goga/usages/cooks/google-oauth2.md`             | existing |

## Risks and Constraints

- **Fail-closed over-drop (accepted trade-off):** if YouTube blocks the probe broadly from
  the runner, every video falls to the duration rule — Shorts (≤180s) dropped correctly,
  but genuinely short regular videos (≤180s) are also dropped. Accepted: a long-form
  playlist tolerates missing a sub-3-minute regular video far better than a leaked Short.
- **Redirect trap (correctness-critical, preserved):** the probe client must still NOT
  follow redirects. Fail-closed changes how a `302` is *interpreted* (inconclusive, not
  regular) but does not permit following it.
- **Unofficial endpoint drift:** YouTube may change `/shorts/{id}` further; the duration
  fallback remains the mitigation. Detect drift via the httptest harness, not live calls.
- **Exact trigger not pinned:** whether production returned a consent `302`, a `403`, or a
  `429` is unconfirmed (not reproducible from this network; not logged by the binary). It
  does not affect the fix — all non-`200` statuses are now inconclusive.

## Scope Estimate

Single task — one cell (`youtube`), one method's status branch, one regression test, one
CODEMANIFEST annotation edit, one usage-file update. The caller loop and signature are
untouched. No decomposition needed.

## Existing Architecture

Import graph: `cmd → {config, state, youtube, syncer}`; `syncer → {config, state, youtube}`;
`youtube →` YouTube Data API v3 + OAuth2 (via the `cooks`) and the unauthenticated
`youtube.com/shorts/{id}` probe. `FilterRegularVideos` is called only from
`syncer/syncer.go` (`syncOne`); its signature is unchanged, so this is an internal-only
change to the `youtube` cell plus contract-text and usage-text updates.

## Notes

Decisions locked during formulation:
- **Posture:** fail closed — only HTTP `200` means Short; everything else is inconclusive
  → duration fallback (user-approved).
- **Blast radius:** the fix is entirely inside `isShort`; `FilterRegularVideos`'s existing
  error → duration-fallback path already does the right thing, so no caller change.
- **Method unchanged:** keep `HEAD` + redirect-blocking client; the `200` signal is reliable.
- **Reproducible case:** video `823YBRAdPeA`, channel `UC2JzWVFH-iiehX-FXHkia4w`
  (Интерьерно), playlist `PLPmI7JhdNPDE`.

### Target API (Go sketch)

```go
// isShort reports whether id is a YouTube Short by issuing an unauthenticated
// HEAD {shortsBase}/shorts/{id} and reading the immediate (pre-redirect) status.
// A 200 response means Short. ANY other status (3xx consent/bot redirect, 4xx
// block, 5xx) or transport error means the probe is inconclusive — the endpoint
// is unofficial and returns non-200 for reasons unrelated to Short-ness — so the
// caller falls back to the duration heuristic. The probe client does not follow
// redirects.
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
    if resp.StatusCode == http.StatusOK {
        return true, nil
    }
    return false, fmt.Errorf("youtube: shorts probe inconclusive (status %d)", resp.StatusCode)
}
```

`FilterRegularVideos`'s classification loop is unchanged: the `case err != nil` branch
already applies the ≤180s duration fallback, which now also covers the former `3xx`/`4xx`
"regular" cases.
