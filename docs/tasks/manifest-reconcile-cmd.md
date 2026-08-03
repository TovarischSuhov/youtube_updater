# Manifest Reconcile — cmd

Parent: [manifest-reconciliation](manifest-reconciliation.md).

## Current State

`cmd` is the CLI entry and composition root (`package main`). Its CODEMANIFEST lists
seven routines. The `--status` command and `RunStatus` were added in `5533d7e` and the
manifest was updated in the same commit.

## Description

Verify `cmd/CODEMANIFEST` against the actual exported declarations in `cmd/main.go`.

## Scope

**In scope:**
- Confirm these seven routines exist in `main.go` with matching signatures and that
  the manifest documents each faithfully (params, return, algorithm, requirements):

| Manifest routine | Go signature |
|---|---|
| `Run(configPath, secretsPath, tokenPath, statePath, redirectURL, dryRun) -> err` | `Run(configPath, secretsPath, tokenPath, statePath, redirectURL string, dryRun bool) error` |
| `RunAdd(configPath, secretsPath, tokenPath, redirectURL, channelInput, playlistInput) -> err` | `RunAdd(configPath, secretsPath, tokenPath, redirectURL, channelInput, playlistInput string) error` |
| `RunRemove(configPath, channelInput) -> err` | `RunRemove(configPath, channelInput string) error` |
| `RunList(configPath) -> err` | `RunList(configPath string) error` |
| `RunStatus(configPath, statePath) -> err` | `RunStatus(configPath, statePath string) error` |
| `SetupLogging(level) -> err` | `SetupLogging(level string) error` |
| `EnsureConfig(configPath) -> created, err` | `EnsureConfig(configPath string) (bool, error)` |

- Confirm the `Imports` block (`config`, `state`, `youtube`, `syncer` types) still
  matches the symbols `main.go` actually consumes.

**Out of scope:** `main.go` logic; the other cells; CLI flag wiring beyond what the
routine docs cover.

## Acceptance Criteria

- All seven routines above are present in `main.go` and documented in
  `cmd/CODEMANIFEST` with no orphan on either side.
- No `cmd` entries are reported by `goga lint`.
- `go test ./cmd` passes.

## Stack

Plain Go (`flag`, `fmt`, `log/slog`). No new dependencies.

## External Dependencies

None.

## Risks and Constraints

- `RunStatus` is the newest routine; double-check its annotation against the
  implemented offline behaviour (missing state → all unseeded).

## Scope Estimate

Single sub-task — small. One file to verify (`cmd/main.go` ↔ `cmd/CODEMANIFEST`).

## Existing Architecture

`cmd` imports `config`, `state`, `youtube`, `syncer` and is the composition root.

## Notes

Expected outcome: faithful, no edit required.
