# Logging & First-Run Config Bootstrap

> Modification to the implemented `youtube-updater` project. Touches `cmd` only.
> `config` / `youtube` / `syncer` / `state` are unchanged.

## Current State

- **Logging:** `cmd` uses `slog.Default()` at the implicit info level with **no
  level control** and **no startup line** — you can't tell which command/paths a
  run used, and you can't raise verbosity to debug or silence info noise. `youtube`
  prints the one-time OAuth consent URL to stderr via stdlib `log.Printf`.
- **First run:** `LoadConfig` errors on a missing `config.yaml` → `Run` returns
  "load config: …" → exit 1. A brand-new checkout cannot do anything until
  `config.yaml` is hand-created.

## Description

1. **Logging** — add a `--log-level` flag (`debug`/`info`/`warn`/`error`, default
   `info`) that configures the `slog` handler; emit a startup log line (the command
   being run plus the relevant paths).
2. **First-run bootstrap** — before loading config, if `config.yaml` is missing,
   write an empty default (`channels: []`) via `config.SaveConfig`, log
   "first run: created config.yaml", and proceed. Applies to every command
   (sync → no-op, `--list` → empty, `--add-channel` → works immediately).

## Scope

**In scope:**
- `cmd`: `--log-level` flag + `slog` handler setup (text handler to stderr at the
  chosen level); a startup log line; a config bootstrap that creates an empty
  `config.yaml` when missing, then continues.
- Reuse `config.SaveConfig(path, nil)` to write the empty default.

**Out of scope:**
- Log files, JSON/remote logging, log rotation.
- Changing `LoadConfig`'s contract (it still errors on missing — `cmd` bootstraps
  *before* calling it).
- `@handle`/name resolution; multi-account; concurrency safety.

## Acceptance Criteria

- `--log-level debug` increases verbosity (debug messages appear); `--log-level
  error` suppresses info/noise — verified by a startup line that appears at info
  but not at error.
- A startup log line records the command and the config path (plus secrets/state
  for sync).
- Running any command with no `config.yaml` present creates `channels: []`,
  logs "first run: created config.yaml", and exits 0 (or proceeds normally) — not
  exit 1.
- After bootstrap, `--list` shows nothing, `--add-channel …` adds successfully,
  and `sync` is a no-op (no channels).
- The one-time OAuth consent URL is **always** printed to stderr regardless of
  `--log-level`.
- All existing tests pass; the new behavior is unit-tested.

## Stack

- **Frameworks:** none.
- **Libraries:** stdlib `log/slog` (level handler), stdlib `flag`; reuse
  `config.SaveConfig`.
- **Infrastructure:** none.

## External Dependencies

| Component | Usage file | Status |
|-----------|------------|--------|
| _(none new)_ | — | stdlib only; no new packages or usage files |

## Risks and Constraints

- **OAuth URL visibility:** the consent URL must remain visible at every log level
  (it is a user instruction, not a diagnostic) — kept as a direct stderr write.
- **Bootstrap surprise:** silently creating `config.yaml` could surprise users who
  mistyped `--config`. Mitigated by the explicit "first run: created config.yaml"
  log line.
- **`slog` global state:** configuring `slog.Default()` is process-global; fine for
  a single-purpose CLI.

## Scope Estimate

**Single small task.** `cmd`-only: two helpers (logger setup, config bootstrap) +
the `--log-level` flag + a startup line. No decomposition needed.

## Existing Architecture

- **`cmd` cell** — `Run`/`RunAdd`/`RunRemove`/`RunList` + `main`. This task adds
  the flag, the slog setup, the startup line, and the bootstrap (exact contract
  shape — e.g. an `EnsureConfig` routine or inline in `main` — is decided in the
  architecture brainstorm).
- **`config` / `youtube` / `syncer` / `state`** — unchanged. The sync path and the
  add/remove/list flows are otherwise unaffected.

## Notes

- Locked decisions: `--log-level` (debug/info/warn/error, default info) + startup
  line; **bootstrap** an empty `config.yaml` on first run and proceed; OAuth URL
  always shown; `cmd`-only; no new dependencies.

### CLI examples

```bash
# First run with no config.yaml — creates an empty one, then syncs (no-op)
youtube-updater
#   → "first run: created config.yaml" + "sync done …"

# Raise verbosity
youtube-updater --log-level debug
youtube-updater --log-level error   # suppresses info, keeps errors

# Bootstrap applies to every command
youtube-updater --list               # creates config.yaml if missing, then lists (empty)
youtube-updater --add-channel UCx --add-playlist PLx   # works immediately after bootstrap

# Custom config path is still bootstrapped if missing
youtube-updater --config /etc/yu/config.yaml --list
```
