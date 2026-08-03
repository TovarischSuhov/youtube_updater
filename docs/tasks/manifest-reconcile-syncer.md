# Manifest Reconcile — syncer

Parent: [manifest-reconciliation](manifest-reconciliation.md).

## Current State

`syncer` orchestrates the per-channel sync. `cc2ab14` added Shorts/stream filtering
and updated `syncer/CODEMANIFEST` and `syncer/.usages/sync.md` in the same commit.
That edit introduced the **one lint error** in the project: the `SyncAll` annotation
references `` `YouTube.FilterRegularVideos` `` as a dotted backtick link, which the DSL
link checker cannot resolve.

## Description

Verify `syncer/CODEMANIFEST` against `syncer/syncer.go`, audit
`syncer/.usages/sync.md`, and **fix the dotted-link lint error**.

## Scope

**In scope:**

| Manifest element | Go |
|---|---|
| `ChannelResult()` props ChannelID, PlaylistID, Seeded, NewCount, AddedIDs, Err | `syncer.ChannelResult` struct (exact 6 fields) |
| `SyncAll(youTube, state, mappings, dryRun) -> results, err` | `SyncAll(yt *youtube.YouTube, st *state.State, mappings []config.ChannelMapping, dryRun bool) ([]ChannelResult, error)` |

- **Lint fix:** in `syncer/CODEMANIFEST`, replace the `` `YouTube.FilterRegularVideos` ``
  dotted link with the resolvable `` `YouTube` `` type link, naming `FilterRegularVideos`
  in plain text — matching the manifest's existing convention (e.g. "Resolve the
  channel's uploads playlist via `YouTube`"). Keep the cursor-advances-past-excluded
  requirement intact.
- Audit `syncer/.usages/sync.md`: confirm the Shorts/stream-skip and
  watermark-advances statements still match `syncOne` in `syncer/syncer.go`.

**Out of scope:** `syncer.go` logic; the `youtube` side of `FilterRegularVideos`.

## Acceptance Criteria

- `ChannelResult` (6 fields) and `SyncAll` faithful; no orphans.
- `syncer/.usages/sync.md` accurate vs `syncer.go`.
- **`goga lint` reports 0 errors for `syncer`** (and the project overall reaches 0).
- `go test ./syncer` passes.

## Stack

Plain Go (`slices`). No new dependencies.

## External Dependencies

None.

## Risks and Constraints

- **DSL link rule:** only imported type names resolve. The fix must use `` `YouTube` ``
  (imported type), not any `Type.Method` form. Verify by re-running `goga lint`.

## Scope Estimate

Single sub-task — small, but it carries the **only known manifest edit** in the whole
audit.

## Existing Architecture

`syncer` imports `config` (`ChannelMapping`), `state` (`State`), `youtube`
(`YouTube`, `Video`).

## Notes

This is the cell that unblocks `goga lint`/`goga schema`. Expected: `ChannelResult`
and `SyncAll` otherwise faithful; the dotted link is the sole fix.
