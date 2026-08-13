# Plan: `shorts-filter-leak`

## Purpose

Fix the Shorts-filter leak: a real Short (`823YBRAdPeA`, 55s, channel Интерьерно)
reached the target playlist because `isShort` treated `3xx`/`4xx` probe responses
as "regular video." Make classification **fail-closed** — only `HTTP 200` ⇒ Short;
every other probe outcome is inconclusive and falls back to the duration heuristic.

After implementation the `youtube` package will: drop a real Short even when the
unofficial `/shorts/{id}` probe answers `3xx` (consent/bot redirect) or `4xx`
(`403`/`429` block) from a CI datacenter IP. The public surface is unchanged.

- **Contract gap closed**: the `FilterRegularVideos` Constraint "only HTTP 200 ⇒
  Short; any other status MUST fall back to duration" (already applied to
  `youtube/CODEMANIFEST`, lint-clean) is not yet enforced by the code — `isShort`
  still returns `(false, nil)` for non-`200`.
- **Strategy**: one TDD coding task collapses `isShort`'s response handling; one
  documentation task realigns the practice + consumer docs with the contract.

## Context

### Contract Surface

**Entity: `FilterRegularVideos` (method on `YouTube`)**
- Type: method (Entity `YouTube`, `location: youtube.go`)
- Signature (unchanged): `(videos: []Video) -> regular:[]Video, err:error`
- Facade obligation: importable from package `youtube`
- Annotation (APPLIED, read-only now): Algorithm step 3 — "an HTTP 200 response
  means Short and is dropped"; step 4 — "any other probe outcome (non-200 or
  transport error) ⇒ inconclusive ⇒ duration ≤ 180s fallback"; new Constraint —
  "Only an HTTP 200 from the probe means Short; any other probe status MUST fall
  back to duration and MUST NOT be classified as a regular video."
- Semantic requirement: the probe-failure path never aborts the channel; it
  falls back to duration.
- `isShort` is **unexported** → not in CODEMANIFEST; its behavior change is the
  implementation locus (this plan), not a contract edit.

### Usages Context

- **`youtube-shorts-detection`** (`.goga/usages/cooks/youtube-shorts-detection.md`):
  the unauthenticated `HEAD /shorts/{id}` probe + duration fallback. Used by
  `FilterRegularVideos` step 3. Its current status table (`3xx → regular`,
  `404 → regular`) encodes the bug and is updated in Task 2.
- **`youtube-data-api`**: `videos.list(contentDetails, liveStreamingDetails)` for
  duration + stream flags. Unchanged.
- **`google-oauth2`**: facade construction. Unchanged.

### Local Usages

- **`youtube/.usages/facade.md`** — consumer guide; status current, one-line
  tighten in Task 2 ("if that probe fails" → "if the probe does not return HTTP 200").

### External Dependencies
- Go standard library `net/http` only. No new third-party packages.

## Facts

- Video `823YBRAdPeA` is a 55s Short; `HEAD /shorts/823YBRAdPeA` answers `200`.
- Deductive proof (design §Code Stack Trace): for a 55s Short with parseable
  duration, the only `FilterRegularVideos` keep-path is `isShort`'s `default`
  branch — i.e. the probe returned `3xx`/`4xx`. (`200` ⇒ drop; error/`5xx` ⇒
  fallback ⇒ `55 ≤ 180s` ⇒ drop.)
- Existing `FilterRegularVideos` tests stay green under the new path: every
  `3xx`-kept case uses duration `>180s` (kept via fallback); no test combines a
  non-`200` probe with a `≤180s` *regular* video expecting "keep" (design §Test
  Stack Trace re-derives each).

## Gap Analysis

- Missing enforcement: `isShort` `default: return false, nil` contradicts the new
  Constraint.
- Behavioral mismatch: `3xx`/`4xx` read as "regular" → Shorts leak.
- Practice mismatch: `youtube-shorts-detection.md` table + sketch say non-`200` ⇒
  regular.
- Reusable code: `FilterRegularVideos`'s loop already routes `err` to the duration
  fallback — no change needed there.
- Test gap: no test covers a non-`200` probe against a Short.

---

## Tasks

> Coding tasks complete before documentation. Within the coding task, contract
> tests first (TDD). CODEMANIFEST is read-only.

### Task 1: Make `isShort` fail-closed — only HTTP 200 ⇒ Short (TDD coding)

`isShort` (`youtube/youtube.go`, unexported, called only by
`FilterRegularVideos`) currently returns `(false, nil)` for `3xx`/`4xx`, which
hits `FilterRegularVideos`'s keep branch and leaks real Shorts when the
unofficial probe is blocked/redirected. Collapse the response handling so any
non-`200` returns an inconclusive error; the existing caller fallback then drops
Shorts (≤180s) and keeps regulars (>180s). The public surface is unchanged, so
the TDD red→green is driven by the logic tests; `TestContractSurface` is a
baseline that stays green.

**Usages relevant to this task:**
- `youtube-shorts-detection`: `HEAD {shortsBase}/shorts/{id}`, redirect-blocking
  client. Only `200` ⇒ Short; everything else ⇒ inconclusive ⇒ duration fallback.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. The `FilterRegularVideos` annotation is already applied. If code does not match it, fix the code.**

- [ ] **STEP 0 (DECLARATION)**: declare this task (`shorts-filter-leak`, Task 1).
- [ ] **STEP 1 (CONTRACT TESTS)**: the contract surface is unchanged — confirm
      `youtube/export_test.go` `TestContractSurface` still asserts
      `var _ func([]Video) ([]Video, error) = y.FilterRegularVideos`. Run
      `go test ./youtube -run TestContractSurface` — baseline green (no new
      contract test; signature unchanged).
- [ ] **STEP 2 (IMPLEMENTATION)**: in `youtube/youtube.go`, in `isShort`, after
      `defer resp.Body.Close()` replace the three-arm `switch`
      (`200` / `>=500` / `default→return false,nil`) with:
      `if resp.StatusCode == http.StatusOK { return true, nil }`
      `return false, fmt.Errorf("youtube: shorts probe inconclusive (status %d)", resp.StatusCode)`.
      Do NOT touch `defaultShortsClient` (`CheckRedirect → http.ErrUseLastResponse`),
      request construction, or `FilterRegularVideos`.
- [ ] **STEP 3 (INTERFACE VERIFICATION)**: `go test ./youtube -run TestContractSurface` — passes.
- [ ] **STEP 4 (LOGIC TESTS)**: add to `youtube/youtube_test.go` (use
      `newTestYouTubeWithShorts` + `writeVideosList`; both exist):
  - `TestFilterRegularVideos_Probe3xx_FallsBackToDuration` —
    `durFor={"v_short":"PT55S","v_long":"PT10M"}`; shorts handler returns
    `http.Redirect(..., StatusFound)` for every `/shorts/{id}`. Input
    `[v_short, v_long]`. Expect `got==[v_long]`, `err==nil`. (v_short 302⇒err⇒fallback⇒55≤180⇒drop;
    v_long 302⇒err⇒fallback⇒600>180⇒keep.)
  - `TestFilterRegularVideos_Probe4xx_FallsBackToDuration` — same `durFor`;
    shorts handler returns `w.WriteHeader(http.StatusForbidden)` for every probe.
    Expect `got==[v_long]`, `err==nil`.
- [ ] **STEP 5 (DEBUGGING)**: `go test ./...` — fix implementation until all pass.
      Do NOT edit tests green. All pre-existing `FilterRegularVideos` tests
      (incl. `ProbeRedirectNotFollowed`, `DropsShortViaProbe`, `DropsShortsAndStreams`,
      `FallsBackToDurationOnProbeError`) must remain green — re-derived in design
      §Test Stack Trace. If `youtube/sync_integration_test.go` fails, investigate
      against the fail-closed semantics (a `≤180s` regular on a non-`200` probe is
      now dropped by design).
- [ ] **STEP 6 (CONTRACT RE-VERIFICATION)**: `FilterRegularVideos` signature
      intact; the Constraint ("only 200 ⇒ Short") now holds in code. Run
      `goga contract` and `goga schema` — exit 0.
- [ ] **STEP 7 (LINT)**: `goga lint`; `gofmt -w youtube/youtube.go youtube/youtube_test.go`.
- [ ] **STEP 8 (COMPLETION)**: mark checkboxes; submit for review.

### Task 2: Realign Shorts-detection practice + consumer guide (documentation)

The applied CODEMANIFEST Constraint now contradicts the practice doc
(`3xx`/`404 → regular`) and the `isShort` sketch in the cook, and the consumer
guide's wording is slightly loose. Bring both in line. No Go code; no TDD.

**Usages relevant to this task:**
- `youtube-shorts-detection`: the file being corrected — its status table is the
  authoritative "what each probe status means" reference.

**CRITICAL: `CODEMANIFEST` files — read-only. These edits are to `.usages/` practice and consumer docs only.**

- [ ] **Edit `.goga/usages/cooks/youtube-shorts-detection.md`** — Scenario 1:
      replace the status table with the fail-closed table and update the
      `isShort` sketch; add the rationale section; widen Scenario 2. Verbatim:
      - Table:
        `| Immediate status | Meaning |` / `| 200 | Short |` /
        `| 3xx, 4xx, 5xx | inconclusive |` / `| transport error / timeout | inconclusive |`
        with lead-in "**Only `200` is a definitive Short signal**; every other
        outcome is inconclusive and the caller MUST fall back to the duration
        heuristic (Scenario 2)."
      - `isShort` sketch tail:
        `if resp.StatusCode == http.StatusOK { return true, nil }`
        `return false, fmt.Errorf("youtube: shorts probe inconclusive (status %d)", resp.StatusCode)`
      - New section **"Why non-200 is not a regular signal"**: consent/region
        `3xx` (`302` → `consent.google.com`/locale) and bot-defense `403`/`429`
        from datacenter IPs; a blocked response is indistinguishable from a real
        redirect at status-code level, so only `200` is a safe Short signal —
        reading non-`200` as "regular" lets Shorts leak when the probe is blocked.
      - Scenario 2 lead-in: "inconclusive — any non-`200` status or transport
        error (see Scenario 1) —".
- [ ] **Edit `youtube/.usages/facade.md`** — "Keep only regular videos" section:
      change "if that probe fails, the classifier falls back to duration ≤ 180s"
      → "if the probe does not return HTTP 200, the classifier falls back to
      duration ≤ 180s".
- [ ] **Verify**: `goga lint` (exit 0); grep the cook — confirm no row says
      `3xx → regular` or `404 → regular` remains: `grep -n 'regular video' .goga/usages/cooks/youtube-shorts-detection.md` (expect no matches).

---

## Validation Commands

- `go test ./...`: run all tests (unit + `sync_integration_test.go`)
- `go test ./youtube -run TestFilterRegularVideos`: the classification suite
- `go test ./youtube -run TestContractSurface`: facade/API shape
- `goga lint`: DSL + workspace lint
- `goga contract`: contract consistency
- `goga schema`: cell hierarchy intact
- `gofmt -l youtube/`: formatting (expect no output)

---

## Completion Criteria

- [ ] `isShort` returns an inconclusive error for any non-`200` (the `switch`
      collapsed); `FilterRegularVideos`'s loop unchanged.
- [ ] Public surface unchanged — `TestContractSurface` green; `FilterRegularVideos`
      signature `([]Video) ([]Video, error)`.
- [ ] `TestFilterRegularVideos_Probe3xx_FallsBackToDuration` and
      `..._Probe4xx_FallsBackToDuration` added and green.
- [ ] All pre-existing `FilterRegularVideos` tests remain green.
- [ ] `youtube-shorts-detection.md` table/sketch/rationation reflect fail-closed;
      `facade.md` wording tightened.
- [ ] Task 1 followed TDD (contract baseline → code → logic tests → debugging →
      re-verification → lint).
- [ ] `CODEMANIFEST` not modified during implementation (already applied in design).
- [ ] No package boundary expanded; only `youtube` touched (+ two docs).
- [ ] All Validation Commands pass.
