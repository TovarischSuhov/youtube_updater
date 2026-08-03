# Manifest Reconciliation Audit (overview)

## Current State

Five goga cells back this project: `cmd`, `config`, `state`, `syncer`, `youtube`.
After the last dedicated reconciliation commit (`583cb9e docs: reconcile goga specs
with manual URL/CI changes`), code kept moving and the manifests were updated **inside
the feature commits** rather than in a separate docs pass:

- `5533d7e feat: add offline --status command` — added `state.LastSync` and
  `cmd.RunStatus` to their CODEMANIFESTs.
- `cc2ab14 feat: filter Shorts/streams and run sync from a cached binary` — added
  `youtube.FilterRegularVideos` to the youtube manifest, documented it in the syncer
  manifest and `.usages`, and updated `youtube/export_test.go`.

The automated contract check passes:

- `goga contract` → `{}` (every signature/type/location lines up).
- `goga lint` / `goga schema` → **1 error**: in `syncer/CODEMANIFEST`, the `SyncAll`
  annotation uses a dotted backtick link `` `YouTube.FilterRegularVideos` `` that the
  DSL link checker cannot resolve (only the imported **type** `YouTube` is resolvable;
  the manifest's own convention elsewhere is to link the type).

No commit after `cc2ab14` touches Go source or manifests (only `.github/workflows/`).

## Description

Independently re-derive each cell's public API from the Go source and verify the
corresponding CODEMANIFEST is faithful — types, methods, properties, routines,
signatures, locations, annotations, and imports/usages. Reconcile any drift. Fix the
known `syncer` dotted-link lint error so `goga lint`, `goga contract`, and `goga
schema` all pass clean. Audit the two cell-level `.usages` consumer guides and
`youtube/export_test.go` against the current API.

This is a verification task. Go logic changes are **not** in scope unless the audit
surfaces a genuine contract gap — in which case it is raised as a decision point, not
a silent edit.

## Scope

**In scope:**
- Per-cell reconciliation — see the five sub-task docs below.
- Fix the `syncer/CODEMANIFEST` `` `YouTube.FilterRegularVideos` `` dotted link.
- Audit `syncer/.usages/sync.md` and `youtube/.usages/facade.md`.
- Audit `youtube/export_test.go` (TestContractSurface: `NewWithService`).

**Out of scope:**
- Go behaviour/logic changes (verify, don't reimplement).
- `.goga/usages/cooks/` (`youtube-data-api.md`, `google-oauth2.md`) — external library
  docs, unaffected by these manual updates.
- New features; `.github/` CI changes.
- "Fixing" accepted DSL conventions the contract check already accepts (e.g. the
  `Type(path)` constructor form that maps to Go's `NewType`).

## Acceptance Criteria

- `goga lint` exits 0 with **0 errors**.
- `goga schema` succeeds (exit 0).
- `goga contract` exits 0 (unchanged — still passes).
- `go test ./...` passes.
- For each cell, every exported symbol in code appears in its CODEMANIFEST and
  vice-versa (no orphans either direction), with discrepancies reconciled.
- The `syncer` `SyncAll` annotation no longer references the unresolvable dotted link.

## Stack

- **Frameworks:** none (plain Go).
- **Libraries:** none new.
- **Infrastructure:** none.
- **Tooling:** goga CLI (`lint`, `contract`, `schema`), the Cell DSL.

## External Dependencies

| Component        | Usage file                               | Status              |
|------------------|------------------------------------------|---------------------|
| youtube-data-api | `.goga/usages/cooks/youtube-data-api.md` | existing (unchanged)|
| google-oauth2    | `.goga/usages/cooks/google-oauth2.md`    | existing (unchanged)|

## Risks and Constraints

- The audit may surface a real gap the automated checks miss (stale annotation,
  unexported-but-relevant helper). Treat as a decision point, not an auto-edit.
- **DSL link resolution:** only imported type names resolve; method references must
  use the type link, never `Type.Method`.
- **Constructor naming:** `Type(path)` ↔ `NewType` is an accepted convention; do not
  rename to "fix" it.

## Scope Estimate

Six artifacts: this overview + one sub-task per cell. Each cell is small; the bulk is
verification, not editing. The only known manifest edit is the one-line `syncer` link
fix. Sub-tasks:

- [manifest-reconcile-cmd](manifest-reconcile-cmd.md)
- [manifest-reconcile-config](manifest-reconcile-config.md)
- [manifest-reconcile-state](manifest-reconcile-state.md)
- [manifest-reconcile-syncer](manifest-reconcile-syncer.md) ← carries the lint fix
- [manifest-reconcile-youtube](manifest-reconcile-youtube.md)

## Existing Architecture

Import graph: `cmd → {config, state, youtube, syncer}`; `syncer → {config, state,
youtube}`; `youtube →` external YouTube Data API v3 + OAuth2 (via the `cooks`). All
touched cells are expected to be widen-only manifest edits — no signature changes.

## Notes

- Pre-audit evidence already indicates the manifests are faithful (contract passes;
  `go doc` API matches). Expect the audit to confirm this and fix the one lint error.
  The deliverable is the verification record plus a clean toolchain.
- Decisions locked: full independent audit (not trusting the in-feature edits);
  cell-level `.usages` and `export_test.go` in scope; `cooks` out of scope; no Go
  logic edits unless surfaced as a decision point.
