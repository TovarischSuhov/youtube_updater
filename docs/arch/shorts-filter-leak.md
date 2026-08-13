# Architecture Plan — Shorts Filter Leak (Fail-Closed Probe)

Derived from `docs/tasks/shorts-filter-leak.md`. The fix changes **no structure**:
no new cells, types, methods, properties, imports, or signatures. It is a
contract-text change in `youtube/CODEMANIFEST` and a practice-text change in the
`youtube-shorts-detection` usage. `isShort` is unexported and therefore outside the
contract; the contract speaks through `FilterRegularVideos`, whose signature is
preserved.

## 1. Implementation order

Single cell, no dependencies:

| Order | Cell    | Reason                                          |
|-------|---------|-------------------------------------------------|
| 1     | youtube | Leaf cell — no `Imports`; the only cell touched |

`syncer` and `cmd` consume `FilterRegularVideos` by signature only; the signature
is unchanged, so they are not modified.

## 2. Artifacts

### 2.1 MODIFY `youtube/CODEMANIFEST` — `FilterRegularVideos` annotation

Only the `FilterRegularVideos` method annotation changes (Algorithm step 3/4
sharpened + one new `Constraints` bullet). The header, all other types/methods,
`location`, and the footer are unchanged.

**Before** (current `FilterRegularVideos` annotation body):

```yaml
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

**After** (full replacement of the annotation body; only the Algorithm, and the
Constraints block change — the purpose line and parameter lines are unchanged):

```yaml
"FilterRegularVideos(videos: []Video) -> regular:[]Video, err:error": |
  location: youtube.go
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
       `youtube-shorts-detection` (probe of /shorts/{id}): an HTTP 200 response
       means Short and is dropped
    4. Treat any other probe outcome (a non-200 status or a transport error) as
       inconclusive and fall back to duration: a video with duration ≤ 180s is
       treated as a Short

    Requirements:
    - A video absent from the videos.list response (deleted or made private) is dropped
    - A video whose duration fails to parse is kept (not dropped on a guess)

    Constraints:
    - Only an HTTP 200 from the probe means Short; any other probe status MUST fall
      back to duration and MUST NOT be classified as a regular video
    - The probe failure path must never abort the channel; it falls back to duration
```

**Diff summary:** Algorithm step 3 narrowed to "`200` ⇒ Short"; step 4 broadened
"inconclusive" to *any non-200 status or transport error*; one new `Constraints`
bullet pins the fail-closed rule (the regression that was the bug).

### 2.2 MODIFY `.goga/usages/cooks/youtube-shorts-detection.md`

Three targeted edits. The "Domain", "Prerequisites", "The redirect trap (read
first)", "Scenario 2 — Duration fallback", and "Reliability caveats" sections are
otherwise retained (Scenario 2 and the redirect trap remain accurate; light
cross-references added).

**Edit A — Scenario 1 status table + the `isShort` sketch (collapse to 200 vs.
everything-else).**

Before:

```markdown
HEAD `/shorts/{id}`. Interpret the immediate (pre-redirect) status:

| Immediate status | Meaning       |
|------------------|---------------|
| `200`            | Short         |
| `3xx`            | regular video |
| `404`            | regular video |
| transport error  | inconclusive  |

// isShort reports whether id is a Short. err != nil means the probe was
// inconclusive (network/timeout/5xx) — the caller must fall back to a heuristic.
...
	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	return false, nil
```

After:

```markdown
HEAD `/shorts/{id}`. Interpret the immediate (pre-redirect) status. **Only `200`
is a definitive Short signal**; every other outcome is inconclusive and the caller
MUST fall back to the duration heuristic (Scenario 2):

| Immediate status          | Meaning      |
|---------------------------|--------------|
| `200`                     | Short        |
| `3xx`, `4xx`, `5xx`       | inconclusive |
| transport error / timeout | inconclusive |

// isShort reports whether id is a Short. err != nil means the probe was
// inconclusive (ANY non-200 status, or a transport error) — the caller must
// fall back to a heuristic. Only a 200 conclusively identifies a Short.
...
	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	return false, fmt.Errorf("youtube: shorts probe inconclusive (status %d)", resp.StatusCode)
```

**Edit B — add a new section** (after "The redirect trap") explaining why non-200
is not a regular signal. This is the root-cause rationale that prevents the
misunderstanding from recurring:

```markdown
## Why non-200 is not a "regular video" signal

Treat every non-`200` response as **inconclusive** — never as evidence the video
is regular. The `/shorts/{id}` endpoint is unofficial and returns non-`200` for
reasons unrelated to Short-ness:

- **Consent / region redirect (`3xx`)** — `youtube.com` may answer with a `302` to
  `consent.google.com` or a locale domain depending on the caller's IP/region.
- **Bot defense / rate limiting (`403`, `429`)** — repeated unauthenticated HEAD
  requests from a datacenter IP (e.g. a CI runner) are routinely blocked.

A real Short answers `200`; a real non-Short typically `3xx`-redirects to
`/watch`. But a blocked or consent response is indistinguishable from a genuine
redirect at the status-code level, so the only safe reading is: `200` ⇒ Short,
anything else ⇒ fall back to duration. Reading a non-`200` as "regular" lets real
Shorts leak through whenever the probe is blocked.
```

**Edit C — Scenario 2 lead-in** widened to cover the broader inconclusive set:

```markdown
## Scenario 2 — Duration fallback

When the probe is inconclusive — any non-`200` status or a transport error (see
Scenario 1) — classify by `contentDetails.duration` ... a video ≤ 180s is treated
as a Short. ...
```

### 2.3 NOT modified

- `youtube/.usages/facade.md` — consumer-facing; the public behavior ("drops
  Shorts") is unchanged, so the consumer guide is unaffected.
- `youtube/export_test.go` (`TestContractSurface`) — the exported surface is
  unchanged; the existing assertion `func([]Video) ([]Video, error) =
  y.FilterRegularVideos` stays valid as-is.
- `syncer/`, `cmd/`, `config/`, `state/` — unchanged (signature preserved).

## 3. Dependency map

```
                ┌─────────────── youtube (leaf, no Imports) ───────────────┐
   syncer ─────►│  FilterRegularVideos([]Video) -> ([]Video, error)        │
  (signature   │     └─ probe semantics governed by `youtube-shorts-      │
   only; no    │        detection` usage  ◀── MODIFIED                    │
   change)     │     └─ annotation: Algorithm + Constraints ◀── MODIFIED  │
                └──────────────────────────────────────────────────────────┘
                                     │  external (not a cell Import)
                                     ▼
              youtube Data API v3  +  youtube.com /shorts/{id} probe
```

No new cell-to-cell edges. `youtube` remains a leaf.

## 4. Verification checklist (run after applying)

- [ ] `goga schema` — `youtube` cell still present; no new cells/types appear.
- [ ] `goga contract` / `goga lint` — exit 0; the edited annotation parses; backtick
      refs `youtube-shorts-detection` and `youtube-data-api` resolve (both declared
      in the header `Usages`).
- [ ] Every declared `Usages` practice is still referenced in an annotation:
      `youtube-data-api` (FilterRegularVideos step 1, constructor), `google-oauth2`
      (constructor), `youtube-shorts-detection` (FilterRegularVideos step 3).
- [ ] `go test ./...` — including the new regression test (probe `3xx` → Short
      dropped via fallback; probe `4xx` → Short dropped via fallback) and the
      preserved `TestFilterRegularVideos_ProbeRedirectNotFollowed` (now passes via
      the duration fallback: `302` ⇒ inconclusive ⇒ `600s > 180s` ⇒ kept).
- [ ] `TestContractSurface` unchanged and green.
- [ ] `FilterRegularVideos` signature unchanged: `([]Video) ([]Video, error)`.

## 5. Acceptance-criteria mapping (vs. `docs/tasks/shorts-filter-leak.md`)

| Task criterion                                        | How the plan satisfies it                          |
|-------------------------------------------------------|----------------------------------------------------|
| New test: probe `3xx`/`4xx` → Short dropped (fallback)| Architecture preserves the test seam (`isShort` returns error on non-200); caller fallback already drops ≤180s |
| Signature unchanged; `syncer`/`cmd` compile           | No signature, Imports, or type changes             |
| `goga lint`/`contract`/`schema` exit 0                | Annotation-only edit; backtick refs resolve        |
| CODEMANIFEST describes 200-only ⇒ Short               | §2.1 Algorithm step 3 + new Constraint             |
| Usage status table corrected                          | §2.2 Edit A                                        |
| Redirect-trap coverage preserved                      | `isShort` still uses the redirect-blocking client; trap test still passes (via fallback) |

All criteria are covered; none require a structural addition.
