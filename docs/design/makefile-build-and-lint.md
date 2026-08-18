# Design Document: `makefile-build-and-lint`

Source task: [docs/tasks/makefile-build-and-lint.md](../tasks/makefile-build-and-lint.md)

## Contract Changes

### Changed CODEMANIFEST Files

None. `git diff HEAD -- '**/CODEMANIFEST'` is empty; `goga lint` → `cells: 5 errors: 0`.
The task operates **above the cell layer** (build tooling, lint config, CI wiring).

### New Entities

None at the cell level. New top-level artifacts:

- `Makefile` — single entry point for build/test/lint (repo root, not a cell)
- `.golangci.yml` — golangci-lint v2 config (repo root, not a cell)

### Changed Entities

- `.github/workflows/sync.yml` — build job: lint gate + `make build`; sync job
  cold-cache fallback: `make build`
- `.github/workflows/status.yml` — run the tool via `make run` (incl. `--list`)
- Production code, 3 files (errcheck fixes, signatures unchanged):
  `config/config.go`, `state/state.go`, `youtube/youtube.go`
- Test code, 5 files (errcheck fixes, no TestContractSurface changes):
  `cmd/main_test.go`, `youtube/youtube_test.go`, `youtube/sync_integration_test.go`

### Deleted Entities

None.

### Usages and Annotations Changes

- `.goga/usages/cooks/golangci-lint.md` — **created** during task formulation
  (registered in `.goga/config.yml` usages + annotations). The design below
  amends it: adds the QF1011 exclusion rationale to the Configuration section.
- Cell CODEMANIFESTs: untouched — no `Usages`/`Annotations` changes.

## Applied Fixes

### Fixed CODEMANIFEST Defects

None found. Phase 3 validation on all 5 manifests passed: header/body/footer
order, key casing, `location` values (file + extension, same level, no
traversal), Imports graph acyclic (cmd → config/state/syncer/youtube; syncer →
config/state/youtube; youtube leaf), all backtick references resolve,
`goga lint` clean. No `::` mutations or `->` embeddings exist in this project,
so those dimensions are vacuously satisfied.

## Entity Interaction and Data Flow

### Interaction Diagram

```
developer                GitHub Actions runner
   │                            │
   ▼                            ▼
make help/build/test/…    sync.yml build job          status.yml
   │                            │                        │
   │                     golangci-lint-action@v8         │
   │                     (v2.12.2, reads .golangci.yml)  │
   │                            │                        │
   │                     make build                      make run ARGS="--list"
   │                            │                        make run
   ▼                            ▼                        ▼
go build -o youtube-updater ./cmd          go run ./cmd <ARGS>
   │                            │                        │
   └── binary youtube-updater ──┘                        │
       (identical path as before — binary cache key      │
        yt-binary-… unchanged)                           ▼
                                               same binary behavior
                                               (state.json commit steps untouched)
```

### Data Flows

- **Local dev**: `make <target>` → shell → go/golangci-lint toolchain →
  artifacts (`youtube-updater` binary, test results, lint findings).
- **CI build job** (push to master): checkout → setup-go → lint gate →
  `make build` → cache binary (key `yt-binary-…` — unchanged inputs).
- **CI sync job** (schedule/dispatch): restore cached binary; on cache miss
  setup-go + `make build` (identical artifact); run binary; commit state.json
  (unchanged steps).
- **CI status job** (manual dispatch): setup-go → `make run ARGS="--list"`
  → `make run` → same marking/state-commit steps (unchanged).

### Entity Dependencies

- `Makefile` depends on: go toolchain (build/test/vet/fmt), golangci-lint
  (lint target).
- Workflows depend on: `make` (preinstalled on ubuntu-latest), Makefile targets.
- No cell depends on any new artifact; lint fixes are internal to cells.

## Code Stack Trace

The task introduces no new contract entry points. The two traces below cover
the interactions where the substitution `go <cmd>` → `make <target>` must be
behavior-preserving.

### Trace: `make build` (replaces `go build -o youtube-updater ./cmd`)

#### Chain
1. **Input**: `make build` invoked by developer or CI step (`sync.yml` build
   job line 65 replacement; sync job line 112 replacement).
2. **Step**: make parses `Makefile`, resolves target `build` → runs recipe
   `go build -o youtube-updater ./cmd` → checkpoint: identical command, same
   CWD (repo root — make runs where invoked; workflows already `default:
   run: working-directory` is absent, so CWD = repo root, same as before).
3. **Output**: binary at `./youtube-updater`. → checkpoint: path matches the
   `actions/cache` `path: youtube-updater` and the sync step's
   `./youtube-updater` invocation. **Passed.**

#### Checkpoint Summary
- Binary path identical: **passed**
- Cache key inputs unchanged (`hashFiles('**/*.go', 'go.mod', 'go.sum')` —
  Makefile/.golangci.yml are not part of the key; binary depends only on Go
  sources): **passed**

### Trace: `make run ARGS="--list"` (replaces `go run ./cmd --list`)

#### Chain
1. **Input**: `make run ARGS="--list"` in `status.yml` List step.
2. **Step**: target `run` recipe `go run ./cmd $(ARGS)`; `$(ARGS)` defaults to
   empty (defined `ARGS ?=`), so bare `make run` = `go run ./cmd` →
   checkpoint: identical behavior for both call forms. **Passed.**
3. **Step**: stdout flows to the step summary block (`>> "$GITHUB_STEP_SUMMARY"`)
   → checkpoint: make echoes the recipe line before executing
   (`@`-prefix suppresses echo; **decision**: use `@` on run's recipe to keep
   the summary heredoc clean and output byte-identical to today).
   Wait — the recipe output goes to the step's stdout regardless; the recipe
   echo goes to stderr. GitHub summary captures stdout only → byte-identical
   either way; still use `@` for log cleanliness. **Passed.**
4. **Step**: `make run` in the Sync step is piped: `make run 2>&1 | tee
   /tmp/sync.log` → checkpoint: GNU make propagates the recipe's exit code;
   pipefail (GitHub shell default) propagates through tee as today → marking
   step's `grep 'msg=channel'` input unchanged. **Passed.**
5. **Output**: identical to today's `go run ./cmd` output. **Passed.**

#### Checkpoint Summary
- ARGS passthrough (empty default, word-splitting on explicit ARGS): **passed**
- Exit-code propagation through pipe: **passed**
- stdout/stderr shape unchanged for tee/grep consumers: **passed**

## Algorithm Design

### `Makefile`

**Responsibility**: single point of truth for how this project is built,
tested, and linted; self-documenting via `make help`.

**Structure** (full recipe list — implementation copies this verbatim):

```make
# youtube-updater — build/test/lint entry points.
BINARY  := youtube-updater
GO      ?= go
ARGS    ?=

.PHONY: help build test vet fmt fmt-check lint run clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary (./youtube-updater)
	$(GO) build -o $(BINARY) ./cmd

test: ## Run all tests
	$(GO) test ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format all Go sources (gofmt, in place)
	$(GO) fmt ./...

fmt-check: ## Verify formatting without modifying files
	@out=$$($(GO) fmt ./...); if [ -n "$$out" ]; then echo "$$out"; echo "run: make fmt"; exit 1; fi

lint: ## Run golangci-lint (requires golangci-lint v2.12.2)
	golangci-lint run

run: ## Run the tool (make run ARGS="--dry-run")
	$(GO) run ./cmd $(ARGS)

clean: ## Remove build artifacts
	rm -f $(BINARY)
```

Notes:
- `fmt-check` via `go fmt` listing: `go fmt` prints the files it *would*
  reformat when they're already formatted it prints nothing → empty output =
  formatted. (Alternative: `test -z "$$(gofmt -l .)""` — equivalent; `go fmt`
  chosen since it uses the same toolchain resolution as other targets.)
  `go fmt` **rewrites files in place** — for a check-only variant use
  `gofmt -l .` instead (design decision: `fmt-check` uses `gofmt -l`, non-mutating).
- All recipes are plain commands — no shell loops beyond `fmt-check`'s
  emptiness test.
- `MAKEFILE_LIST` + `## `-suffix convention powers `help`.

**Corrected `fmt-check`** (non-mutating — `go fmt` would modify files):

```make
fmt-check: ## Verify formatting without modifying files
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "$$unformatted"; exit 1; fi
```

**Errors:**
- missing go toolchain → recipe exits non-zero, make fails with the tool's message
- `lint` without golangci-lint installed → shell "command not found", exit 127;
  README documents the install command

**Edge Cases:**
- `make run` with no ARGS → `go run ./cmd` (sync-all default command)
- ARGS containing spaces → word-split by shell into multiple flags (intended)
- `make` invoked from a subdirectory → fails to find ./cmd; acceptable (same
  as raw go commands today)

### `.golangci.yml`

**Responsibility**: pin the lint contract so local and CI results match.

```yaml
version: "2"

linters:
  default: standard
  enable:
    - misspell
    - copyloopvar
  settings:
    staticcheck:
      checks:
        - all
        - -QF1011
```

Rationale for `-QF1011` (the only deviation from defaults+2): QF1011 flags
`var _ func(string) error = SetupLogging` in the five `TestContractSurface`
blocks (26 findings). Those explicit signatures are the project's compile-time
public-API check, deliberately mirroring CODEMANIFEST signatures (project
convention: keep CODEMANIFEST + .usages + TestContractSurface in sync).
Weakening them to `var _ = X` would keep type inference but lose nothing —
yet the explicit form is self-documenting and convention-locked; the linter
should not fight the convention. Disabling one quickfix check is cheaper and
more honest than 26 test edits or blanket `//nolint`.

**Errors:**
- missing `version: "2"` → v2 binary refuses to parse (v1 parser removed)
- unknown linter name → config load error, lint target fails fast

**Edge Cases:**
- none (no paths exclusions, no nolint directives)

### `sync.yml` changes

**Build job** — after `actions/setup-go`, before the existing Build step:

```yaml
      - name: Lint
        uses: golangci/golangci-lint-action@v8
        with:
          version: v2.12.2
```

Then `- name: Build\n        run: make build`.

**Sync job** — cold-cache step becomes `run: make build`.

No other steps touched (cache keys, OAuth writes, sync invocation
`./youtube-updater`, state commit all unchanged).

**Edge Cases:**
- cold-cache path now needs make — preinstalled on ubuntu-latest (verified:
  GNU Make 4.x in the runner image); setup-go step still guarded by the same
  cache-miss condition

### `status.yml` changes

- List step: `go run ./cmd --list` → `make run ARGS="--list"`
- Sync step: `go run ./cmd 2>&1 | tee /tmp/sync.log` →
  `make run 2>&1 | tee /tmp/sync.log`

**Edge Cases:**
- none; exit-code propagation through make+tee+pipefail verified in the trace

### Lint fixes (errcheck, 18 findings)

**Production (8) — all explicit-assign, zero behavior change:**

| File:line | Today | Fix |
| --- | --- | --- |
| `config/config.go:95` | `defer os.Remove(tmpName)` | `defer func() { _ = os.Remove(tmpName) }()` |
| `config/config.go:97,101` | `tmp.Close()` in error branches | `_ = tmp.Close()` |
| `state/state.go:98` | `defer os.Remove(tmpName)` | `defer func() { _ = os.Remove(tmpName) }()` |
| `state/state.go:100,104` | `tmp.Close()` in error branches | `_ = tmp.Close()` |
| `youtube/youtube.go:247` | `defer resp.Body.Close()` | `defer func() { _ = resp.Body.Close() }()` |
| `youtube/youtube.go:456` | `fmt.Fprintln(w, …)` (OAuth callback) | `_, _ = fmt.Fprintln(w, …)` |
| `youtube/youtube.go:459` | `go func() { _ = srv.ListenAndServe() }()` | already `_ =` — clean |
| `youtube/youtube.go:460` | `defer srv.Shutdown(ctx)` | `defer func() { _ = srv.Shutdown(ctx) }()` |

Note `config/config.go:104` and `state/state.go:107` (`if err := tmp.Close();
err != nil { return err }`) are **already correct** — the errcheck findings are
only the unchecked `tmp.Close()` calls in the *error branches* (lines 97, 101 /
100, 104) and the deferred `os.Remove`.

**Tests (10):**

| File:line | Fix |
| --- | --- |
| `cmd/main_test.go:268,274,287,293` | `w.Close()` / `r.Close()` → `_ = w.Close()` / `_ = r.Close()` |
| `youtube/youtube_test.go:97,102,115,127` | `fmt.Fprintf/Fprint(w, …)` → `_, _ = fmt.Fprintf(w, …)` / `_, _ = fmt.Fprint(w, …)` |
| `youtube/sync_integration_test.go:47,64` | same `_, _ =` pattern |

No `//nolint` comments; no errcheck function exclusions; QF1011's 26 findings
resolved by the config exclusion above.

**Edge Cases:**
- `_ = tmp.Close()` in an error branch intentionally swallows the close error —
  correct: the primary error is returned, close-of-temp-on-failure is cleanup
- TestContractSurface blocks: **untouched** (protected by `-QF1011`)

## Cross-cutting Concerns

- **Error handling**: lint fixes only reclassify ignored returns as explicit
  `_ =` — no error paths change. Make targets fail with the underlying tool's
  exit code.
- **Logging**: none added. CI lint findings surface in the Actions log/annotations.
- **Validation**: `make lint` in the build job is the new gate — a finding
  fails the build job; the scheduled sync is unaffected (lint runs only on
  push events, same as the build job's `if:`).
- **Caching**: GitHub binary cache key unchanged (`yt-binary-${{ runner.os }}-
  ${{ hashFiles('**/*.go', 'go.mod', 'go.sum') }}`); golangci-lint-action
  caches its own binary between runs.
- **Concurrency**: none introduced.

## Usages Analysis

### `golangci-lint` (project-level usage)
- **What it provides**: how to configure and run golangci-lint v2 in this
  project — install, `.golangci.yml` v2 format, `make lint`, CI action wiring.
- **Where used**: no cell CODEMANIFEST references it (it is build tooling, not
  a cell practice); registered in `.goga/config.yml` global usages.
- **Why chosen**: task requires a lint gate; usage file makes the tooling
  contract discoverable to future implementing agents.
- **How exactly**: `golangci-lint run` from repo root with the committed
  config; `golangci/golangci-lint-action@v8` + `version: v2.12.2` in CI.
- **Amendment needed**: add the QF1011 exclusion rationale (Configuration
  section) so the deviation from pure defaults is documented where agents
  will read it.

### `youtube-data-api`, `google-oauth2` (project-level usages)
- Unaffected. No entity in scope interacts with the YouTube API differently.

### Imported Usages
- None relevant: no `Imports: → Usages:` groups in the affected (i.e., all)
  manifests reference lint tooling.

## `.usages/` Update

### Cell: `youtube`

#### Existing Files — Consistency
- **`facade.md`** → `youtube/.usages/facade.md`
  - Status: current (public API unchanged by the errcheck fixes)
  - Additions needed: none
  - Updates needed: none

### Cell: `syncer`

#### Existing Files — Consistency
- **`sync.md`** → `syncer/.usages/sync.md`
  - Status: current (no syncer code touched)
  - Additions needed: none
  - Updates needed: none

Cells `cmd`, `config`, `state` have no `.usages/` directories → skipped per
Phase 4 Step 6 rules.

## Test Stack Trace

No new cell contract → no new Go unit tests. The lint-fix edits are mechanical
(`_ =` prefixes); existing tests must stay green. Verification tests are
shell-level, executed on the finished tree:

### General Setup
- Repo at the designed state: Makefile, .golangci.yml, fixed sources, switched
  workflows. golangci-lint v2.12.2 on PATH; GNU make 4.x; go 1.26.3.

### Source File Registry
- `Makefile`, `.golangci.yml`, `.github/workflows/sync.yml`,
  `.github/workflows/status.yml`, 3 production .go files, 3 test .go files

---

### Positive Tests

#### `verify-make-help`

**Setup**: terminal at repo root.

**Input**: `make help`

**Trace**:
```
make help
  → parses $(MAKEFILE_LIST)
  → grep '^[a-zA-Z_-]+:.*?## ' filters targets with ## docs
  → awk formats two columns (target, description)
  stdout: 9 lines — build, clean, fmt, fmt-check, help, lint, run, test, vet
```

**Assertions**:
```
exit code 0
output contains "build", "test", "lint", "run", "fmt-check", "help"
every target line ≤ target-col width + description
```

**Sufficiency**: the help target is the Makefile's front door; a broken grep
or awk leaves users with an undocumented entry point.

---

#### `verify-make-build-binary-path`

**Setup**: clean tree (no `youtube-updater` file).

**Input**: `make build`

**Trace**:
```
make build
  → go build -o youtube-updater ./cmd
  → produces executable ./youtube-updater
```

**Assertions**:
```
exit 0
test -x ./youtube-updater
./youtube-updater --help 2>&1 | head -1 does not crash (or flag-usage output)
```

**Sufficiency**: the sync job's binary cache and direct invocation depend on
this exact path; a wrong -o target breaks CI silently.

---

#### `verify-make-test`

**Setup**: repo with all lint fixes applied.

**Input**: `make test`

**Trace**:
```
make test → go test ./...
  → 5 packages compile (including edited *_test.go)
  → all existing tests pass (TestContractSurface untouched, still compiles
    with explicit signatures)
```

**Assertions**:
```
exit 0
"ok" for each of 5 packages
```

**Sufficiency**: proves the 13 test-file `_ =` edits broke nothing and
TestContractSurface survived the QF1011 exclusion decision.

---

#### `verify-make-lint-clean`

**Setup**: all 18 errcheck fixes applied; .golangci.yml with QF1011 exclusion.

**Input**: `make lint`

**Trace**:
```
make lint → golangci-lint run
  → loads .golangci.yml (version: "2")
  → lints ./... — 44 original findings resolved:
      26 QF1011 → excluded by staticcheck.checks
      18 errcheck → fixed via _ = assignments
  → exit 0, no output
```

**Assertions**:
```
exit 0
empty stdout/stderr (no findings)
```

**Sufficiency**: this is the CI gate — if it fails on master, every push breaks.

---

#### `verify-fmt-check-clean`

**Setup**: formatted tree.

**Input**: `make fmt-check`

**Trace**:
```
make fmt-check
  → gofmt -l .
  → empty list (all files formatted)
  → exit 0
```

**Assertions**:
```
exit 0, no output
```

**Sufficiency**: guards against the fmt target being the only check while CI
relies on fmt-check; also proves gofmt -l is non-mutating (unlike go fmt).

---

### Negative Tests

#### `verify-lint-detects-new-violation`

**Setup**: tree at designed state.

**Input**: temporarily append `func f() { _ = os.Open("") }` with unchecked
second return → e.g. add to any .go file:
`var _, _ = fmt.Println` → then `make lint`; then revert.

**Trace**:
```
introduce errcheck violation (unchecked Close)
  → make lint → golangci-lint run → 1 finding (errcheck)
  → exit 1
revert → make lint → exit 0
```

**Assertions**:
```
with violation: exit 1, finding mentions errcheck
after revert: exit 0
```

**Sufficiency**: proves the gate actually gates — a linter that silently
passes everything provides no protection.

---

#### `verify-fmt-check-detects-unformatted`

**Setup**: formatted tree.

**Input**: introduce a misformatted line (extra blank lines), run
`make fmt-check`, then `make fmt`, then `make fmt-check` again.

**Trace**:
```
break formatting → make fmt-check → gofmt -l . lists the file → exit 1
make fmt → file rewritten in place
make fmt-check → exit 0
```

**Assertions**:
```
first fmt-check: exit 1, file listed
after make fmt: fmt-check exit 0
```

**Sufficiency**: verifies the check/format pair round-trips and that fmt-check
doesn't mutate (file mtime/content unchanged by fmt-check itself).

---

### Edge Case Tests

#### `verify-make-run-args-passthrough`

**Setup**: repo with credentials (`client_secrets.json`, `token.json`) or
`ARGS="--list"` (offline-ish — list needs config only).

**Input**: `make run ARGS="--list"`

**Trace**:
```
make run ARGS="--list"
  → $(ARGS) substituted → go run ./cmd --list
  → prints configured pairs from config.yaml
```

**Assertions**:
```
exit 0
output identical to `go run ./cmd --list` run directly (diff = empty)
```

**Sufficiency**: status.yml's List step depends on exact stdout; word-split
ARGS or an echoed recipe line would corrupt the step summary.

---

#### `verify-make-run-exit-code-through-pipe`

**Setup**: same as above; shell with pipefail.

**Input**: `set -o pipefail; make run 2>&1 | tee /tmp/x.log; echo $?`

**Trace**:
```
make run (sync-all with valid creds)
  → tool exits with its code
  → make propagates recipe exit code
  → pipefail propagates through tee
```

**Assertions**:
```
exit code equals direct `go run ./cmd` exit code
/tmp/x.log contains the same content as direct run
```

**Sufficiency**: status.yml's marking step greps this log and pipefail must
fail the job on hard errors — this is the exact CI contract.

---

## Additional Instructions for the Implementation Agent

- Copy the `Makefile` content from the Algorithm Design section verbatim,
  including the corrected `fmt-check` recipe (gofmt -l, non-mutating). Recipe
  lines must be TAB-indented.
- Copy `.golangci.yml` verbatim; keep `version: "2"` as the first key.
- Apply the 18 errcheck fixes exactly as tabulated (production 8, tests 10);
  do NOT touch any `var _ func(…) = …` TestContractSurface line.
- In `sync.yml`, insert the lint step between setup-go and Build; rename build
  commands to `make build` in both jobs. Do not reorder other steps or touch
  the cache keys.
- In `status.yml`, substitute the two `go run` invocations with
  `make run ARGS="--list"` and `make run` respectively.
- Update README: "Build" section mentions `make build` (keep `go build` as
  alternative); add a "Development" block with make test / lint / fmt-check
  and the golangci-lint install command.
- Amend `.goga/usages/cooks/golangci-lint.md` Configuration section: document
  the `-QF1011` staticcheck exclusion and its TestContractSurface rationale.
- Verify locally in order: `make lint && make fmt-check && make vet && make
  test && make build && make clean && make build` — all must pass. The
  `youtube-updater` binary is already gitignored (`/youtube-updater` in
  `.gitignore`) — never commit it.
