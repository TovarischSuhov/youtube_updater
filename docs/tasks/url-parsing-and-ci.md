# URL & Handle Parsing, CI Workflows, Non-Interactive Refresh

## Current State

The service was implemented under three task docs (`youtube-playlist-sync`,
`manage-config-pairs`, `logging-and-first-run`) via the goga flow, and the cell
CODEMANIFESTs reflected that original design. A subsequent round of **manual**
changes (committed on branch `feat/url-parsing-ci-status`) added capabilities no
spec described, and in places contradicted the specs:

- **URL & handle parsing** — new `youtube/parse.go` adds `ChannelRef`,
  `ParsePlaylistID`, `ParseChannelRef`; `YouTube.ResolveChannelRef` resolves
  `@handle`/`/c/`/`/user/` slugs to a channel ID via `channels.list`
  (`forHandle` then `forUsername`). `cmd.RunAdd`/`RunRemove` now accept a bare ID
  or a pasted URL. The `youtube-playlist-sync` task had listed *"`@handle`
  resolution"* as **Out of scope** and called the config "ID-only".
- **Non-interactive token refresh** — `loadToken` returns a stored token whose
  access token has expired but which still has a refresh token, so scheduled/CI
  runs refresh instead of falling back to the consent flow.
- **CI workflows** — `.github/workflows/sync.yml` (daily scheduled sync) and
  `status.yml` (manual run that marks channels failing to update, without failing
  the job) were not represented in the goga model.

## Description

Reconcile the goga specifications with the manual changes so the docs correctly
describe the as-built `youtube_updater` service. Documentation/spec task only —
the code already exists and is tested (83 tests green); no behavior changes.

The service as built:
- One-shot Go CLI. Modes: **sync** (default; seed-on-first-contact, then add only
  newer-than-watermark videos), **add** (`--add-channel`/`--add-playlist`,
  accepts IDs or URLs; resolves handles via the API), **remove**, **list**,
  **--dry-run**.
- Auth: OAuth2 Desktop, cached token, auto-refresh; works unattended once
  consented.
- State: per-channel JSON cursor (`state.json`), atomic writes.
- CI: scheduled daily sync + manual status run (marks failed channels without
  failing the job), sharing a cached `state.json`.

## Scope

**In scope:**
- This task doc (accurate service description).
- `youtube/CODEMANIFEST`: add `ChannelRef`, `ResolveChannelRef`,
  `ParsePlaylistID`, `ParseChannelRef` (parsers located in `parse.go`).
- `cmd/CODEMANIFEST`: `RunAdd`/`RunRemove` annotations now describe channel/
  playlist input parsing and channel-ref resolution.
- `.goga/usages/cooks/youtube-data-api.md`: add `forHandle`/`forUsername`
  resolution scenario + quota note.
- `.goga/usages/cooks/google-oauth2.md`: add non-interactive-refresh requirement
  and the `oauth2.Token.Valid()` pitfall.
- Mention the CI workflows in the service description (no usage file — GitHub
  Actions is infrastructure, not a library API).

**Out of scope:**
- Any code or behavior change (implementation is complete and tested).
- New features (backfill, multi-playlist-per-channel, binary releases, …).
- Re-architecting cells or changing public signatures.

## Acceptance Criteria

- This doc accurately describes the as-built service.
- `youtube/CODEMANIFEST` lists `ChannelRef`, `ResolveChannelRef`,
  `ParsePlaylistID`, `ParseChannelRef`; `goga schema` surfaces them.
- `cmd/CODEMANIFEST` `RunAdd`/`RunRemove` annotations reflect URL/input resolution.
- `youtube-data-api.md` documents `forHandle`/`forUsername` resolution.
- `google-oauth2.md` documents the non-interactive-refresh behavior.
- `goga lint` passes (exit 0).

## Stack

- **Frameworks:** none (plain Go CLI).
- **Libraries:** `google.golang.org/api/youtube/v3`, `golang.org/x/oauth2`
  (+`/google`), `gopkg.in/yaml.v3`; stdlib (`flag`, `log/slog`, `net/url`,
  `regexp`).
- **Infrastructure:** GitHub Actions (scheduled + manual workflows); local files
  only (`config.yaml`, `state.json`, cached token, user `client_secrets.json`).

## External Dependencies

| Component | Usage file | Status |
|-----------|------------|--------|
| YouTube Data API v3 | `.goga/usages/cooks/youtube-data-api.md` | updated (+`forHandle`/`forUsername`) |
| Google OAuth2 (Go desktop) | `.goga/usages/cooks/google-oauth2.md` | updated (+non-interactive refresh) |

## Risks and Constraints

- **CODEMANIFEST drift** is the core risk; reconciliation must only document what
  the code already exposes — no invented symbols.
- **CI workflows are external to the cell graph** (they shell out to the built
  binary, not Go symbols), so they are described in prose, not as cell deps.
- **Usage edits stay self-contained** (matching the existing scenario style) so
  the cooks remain valid reference inputs for future tasks.

## Scope Estimate

**Single task** — documentation/spec reconciliation across 5 files (2
CODEMANIFESTs, 2 usages, this doc). Mechanical and low-risk; no decomposition.

## Existing Architecture

Five cells (`goga schema`): `youtube` (facade + parsing), `cmd` (composition
root), `config`, `state`, `syncer`. The manual changes widen the `youtube` cell
(parsing + `ResolveChannelRef`) and the `cmd` cell (input resolution in
`RunAdd`/`RunRemove`); no new cells, no new inter-cell dependencies.

## Notes

- Reconciliation applied during formulation: both usage files and both
  CODEMANIFESTs were updated; this doc records the as-built state.
- Code state: branch `feat/url-parsing-ci-status`; `go test ./...` = 83 passed.
