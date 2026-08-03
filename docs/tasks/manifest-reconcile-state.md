# Manifest Reconcile — state

Parent: [manifest-reconciliation](manifest-reconciliation.md).

## Current State

`state` owns the per-channel "last seen video" cursor store. `LastSync` was added in
`5533d7e` and documented in the same commit. The constructor in code is `NewState`;
the manifest models it as `State(path)` — an accepted DSL convention (`goga contract`
passes).

## Description

Verify `state/CODEMANIFEST` against `state/state.go`.

## Scope

**In scope:**

| Manifest element | Go |
|---|---|
| `State(path)` constructor (modelled) | `NewState(path string) (*State, error)` |
| property `Path -> string` | State exposes the path |
| `IsSeeded(channelID) -> seeded` | `IsSeeded(channelID string) bool` |
| `LastSeenID(channelID) -> videoID` | `LastSeenID(channelID string) string` |
| `LastSeenAt(channelID) -> publishedAt` | `LastSeenAt(channelID string) string` |
| `LastSync(channelID) -> lastSync` | `LastSync(channelID string) string` |
| `SetLastSeen(channelID, videoID, publishedAt) -> err` | `SetLastSeen(channelID, videoID, publishedAt string) error` |
| `Save() -> err` | `Save() error` |

**Out of scope:** `state.json` on-disk schema; cursor semantics changes.

## Acceptance Criteria

- All six methods + the `Path` property present and faithful; no orphans.
- `LastSync` specifically verified (newest addition).
- `goga lint` reports nothing for `state`.
- `go test ./state` passes.

## Stack

Go + `encoding/json`. No new dependencies.

## External Dependencies

None (the `json` practice is inline).

## Risks and Constraints

- Do not rename `State(path)` → `NewState(path)`; the constructor-as-type form is
  accepted by the contract check.

## Scope Estimate

Single sub-task — small. One file (`state/state.go` ↔ `state/CODEMANIFEST`).

## Existing Architecture

Leaf cell; imported by `cmd` and `syncer`.

## Notes

Expected outcome: faithful, no edit required.
