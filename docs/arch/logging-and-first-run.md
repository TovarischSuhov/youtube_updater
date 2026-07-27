# Architecture Plan — Logging & First-Run Config Bootstrap

Modification to the implemented `youtube-updater`. **`cmd` cell only** — no other
cells, no new dependency edges, no new packages. The modified CODEMANIFEST is
already on disk and `goga lint`-clean (5 cells, 0 errors); this document is the
recorded plan + diff.

Task: `docs/tasks/logging-and-first-run.md`.

---

## 1. Implementation order

| # | Cell | Reason |
|---|---|---|
| 1 | `cmd` | No new cell deps. Adds `SetupLogging`, `EnsureConfig`, and `main` wiring. |

Single cell — no ordering across cells.

## 2. Dependency map

Unchanged. `cmd` already imports `config` (incl. `SaveConfig`, used by
`EnsureConfig`), `state`, `syncer`, `youtube`. `SetupLogging` uses only stdlib
`log/slog`. No new edges:

```
config ──(LoadConfig, AddMapping, SaveConfig, RemoveMapping)──> cmd
state  ──(State)──────────────────────────────────────────────> cmd
youtube──(YouTube)────────────────────────────────────────────> cmd
syncer──(SyncAll, ChannelResult)──────────────────────────────> cmd
```

---

## 3. Diff — `cmd/CODEMANIFEST`

### Header annotation (updated)
Added "configures logging, bootstraps the config on first run" to the cell's
responsibility statement.

### New routines

```yaml
"SetupLogging(level: string) -> err:error":
  location: main.go
  # Map level (debug/info/warn/error, case-insensitive) to a slog severity;
  # install a stderr text handler at that severity as the default logger.
  # Unrecognized level → err.

"EnsureConfig(configPath: string) -> created:bool, err:error":
  location: main.go
  # If configPath exists → created=false.
  # Else → SaveConfig(configPath, nil) (writes "channels: []") + log
  #        "first run: created config.yaml" → created=true.
  # Idempotent; output is valid YAML readable by LoadConfig.
```

`Run`, `RunAdd`, `RunRemove`, `RunList` — unchanged.

### `main` wiring (implementation; not a contract type)
```
flag.Parse (incl. new --log-level, default "info")
  → SetupLogging(*logLevel)            # on err → exit 2
  → startup log line (command + paths) # slog.Info
  → EnsureConfig(*configPath)          # bootstrap before dispatch
  → dispatch: list / remove / add / sync   (existing switch)
```

---

## 4. Verification checklist (Phase 6)

| # | Check | Result |
|---|---|---|
| 1 | Completeness — approved types present | ✅ `SetupLogging`, `EnsureConfig` |
| 2 | DSL correctness | ✅ `goga lint` 0 errors |
| 3 | Inter-cell consistency | ✅ no new edges; reuses `SaveConfig` |
| 4 | Implementation order | ✅ single cell |
| 5 | No placeholders | ✅ |
| 6 | Every `Imports.Types` used | ✅ `SaveConfig`/`LoadConfig` referenced by new + existing routines |
| 7 | `Imports.Usages` referenced | n/a (none) |
| 8 | Every `Usages` referenced | n/a (cmd has no `Usages`; `slog` is stdlib, no usage file) |
| 9 | Algorithms present | ✅ both new routines |
| 10 | No impl detail in annotations | ✅ |
| 11 | Backtick references resolvable | ✅ (`SaveConfig`, `LoadConfig` imported; params) |
| 12 | `location` bare filename | ✅ main.go |
| 13 | No cross-imports | ✅ |
| 14–16 | Embedding/mutation/Entity-Routine | n/a / ✅ (Routines) |
| 17–18 | Base usages/annotations | n/a for cmd (no Google-API usage applies) |
| 19 | Language correctness (Go) | ✅ |

## 5. Acceptance criteria coverage

| Task criterion | Covered by |
|---|---|
| `--log-level debug`/`error` changes verbosity | `SetupLogging` + startup line at info |
| Startup line records command + paths | `main` startup log |
| Missing `config.yaml` → create `channels: []`, log, exit 0 | `EnsureConfig` (before dispatch) |
| After bootstrap: list empty, add works, sync no-op | `EnsureConfig` + unchanged Run*/dispatch |
| OAuth URL always shown | untouched (youtube direct stderr write) |
| Existing tests pass; new behavior tested | (implementation phase) |
