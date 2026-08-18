# Plan: `makefile-build-and-lint`

Source design: [docs/design/makefile-build-and-lint.md](../design/makefile-build-and-lint.md)
Source task: [docs/tasks/makefile-build-and-lint.md](../tasks/makefile-build-and-lint.md)

## Purpose

Add a self-documenting `Makefile` as the single entry point for build, test,
vet, fmt, and lint; introduce `golangci-lint` (v2.12.2) with a minimal v2
config and a CI lint gate; switch the two GitHub workflows from raw `go`
commands to `make` targets; fix the 18 errcheck findings in production and
test code. After implementation, `make lint && make fmt-check && make vet &&
make test && make build` all pass locally, and the CI build job lints before
building.

No cell contract changes: CODEMANIFESTs are untouched (read-only), no public
API changes, `TestContractSurface` blocks are preserved verbatim.

## Context

### Contract Surface

**No contract entities are added, changed, or deleted.** All five CODEMANIFESTs
(`cmd`, `config`, `state`, `syncer`, `youtube`) are read-only and unaffected.
The work is entirely above the cell layer: build tooling (`Makefile`,
`.golangci.yml`), CI wiring (two workflow files), and mechanical errcheck
fixes inside existing implementations that change no exported identifier.

The Go package facades (exported identifiers per package) must remain exactly
as they are — the 8 production errcheck fixes introduce only `_ =` /
`_, _ =` assignments to already-ignored return values.

### Re-exports

None. No `->Name: {}` blocks exist in this project.

### Usages Context

- **`golangci-lint`** — `.goga/usages/cooks/golangci-lint.md` (created during
  task formulation, registered in `.goga/config.yml`). Provides: v2 config
  format (`version: "2"` first key), local install command, `make lint`
  invocation, CI wiring via `golangci/golangci-lint-action@v8` with pinned
  `version: v2.12.2`. The plan adds the QF1011-exclusion rationale to this
  file (Task 4).
- **`youtube-data-api`**, **`google-oauth2`** — unaffected; no entity in scope
  changes YouTube API or OAuth interaction.

### Imported Usages

None relevant — no `Imports: → Usages:` groups reference lint tooling.

### Local Usages

None. Cell `.usages/` directories (`youtube/.usages/facade.md`,
`syncer/.usages/sync.md`) are current and need no changes (public APIs
unchanged).

### External Dependencies

- **GNU Make 4.x** — preinstalled on ubuntu-latest runners and standard on
  dev machines; no install step needed.
- **golangci-lint v2.12.2** — dev tool, not a Go import. Local:
  `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`.
  CI: fetched by `golangci/golangci-lint-action@v8`.
- **golangci/golangci-lint-action@v8** — GitHub Action; caches the linter
  binary between runs.

## Facts

- Git diff vs HEAD shows zero CODEMANIFEST modifications; `goga lint` →
  `cells: 5 errors: 0`.
- A probe run of golangci-lint v2.12.2 with the proposed config on the current
  tree reports exactly **44 issues**: 26 staticcheck QF1011 (all in
  `TestContractSurface` `var _ func(…) = …` blocks across 5 test files) and
  18 errcheck (8 production, 10 test).
- Production errcheck sites: `config/config.go:95,97,101`;
  `state/state.go:98,100,104`; `youtube/youtube.go:247,456,460`.
- `youtube/youtube.go:459` (`go func() { _ = srv.ListenAndServe() }()`) is
  already clean.
- `config/config.go:104` and `state/state.go:107` (`if err := tmp.Close();
  err != nil { return err }`) are already correct — not part of the findings.
- The sync workflow caches the binary at path `youtube-updater` under key
  `yt-binary-${{ runner.os }}-${{ hashFiles('**/*.go', 'go.mod', 'go.sum') }}`;
  `make build` must write the identical path so the cache contract holds.
- status.yml pipes sync output: `go run ./cmd 2>&1 | tee /tmp/sync.log` — the
  marking step greps `msg=channel` lines from that log; pipefail propagates
  hard failures.
- `.gitignore` already contains `/youtube-updater` — the binary is never
  committed.
- GNU Make 4.3 verified locally; make is preinstalled on ubuntu-latest.
- golangci-lint v2 binary does not parse v1 configs — `version: "2"` must be
  the first key of `.golangci.yml`.

## Gap Analysis

- **Missing**: `Makefile` (no build entry point today), `.golangci.yml` (no
  lint config), lint gate in CI, `make`-based CI steps.
- **Present and correct**: hermetic test suite (`go test ./...` green), both
  workflows functional with raw `go` commands, binary path contract.
- **API mismatches**: none (no contract changes).
- **Behavioral mismatches**: none — errcheck fixes are explicitly non-behavioral.
- **Reusable code**: everything; this plan only adds tooling and prefixes
  `_ =` to existing unchecked calls.
- **Test coverage gaps**: none at the Go level; verification for this feature
  is shell-level (`make` targets) plus the existing suite staying green.

---

## Tasks

> **Package ordering rule**: not applicable — no cell packages are touched by
> contract. Tasks are ordered by dependency: tooling first (Makefile, lint
> config), then fixes, then CI wiring, then docs, then end-to-end validation.

### Task 1: Create `Makefile` with self-documenting targets (infrastructure)

Create `Makefile` at the repo root as the single entry point for building,
testing, and linting. Nine targets; `help` is the first (default) target and
renders the `## `-annotated target list. This task introduces no Go changes.

Context verbatim from the design document (Algorithm Design → `Makefile`):

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
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "$$unformatted"; exit 1; fi

lint: ## Run golangci-lint (requires golangci-lint v2.12.2)
	golangci-lint run

run: ## Run the tool (make run ARGS="--dry-run")
	$(GO) run ./cmd $(ARGS)

clean: ## Remove build artifacts
	rm -f $(BINARY)
```

Key details:
- Recipe lines are **TAB-indented** (verbatim requirement).
- `fmt-check` uses `gofmt -l .` (non-mutating) — NOT `go fmt`, which would
  rewrite files during a "check".
- `ARGS ?=` defaults empty → bare `make run` ≡ `go run ./cmd`.
- `make build` must write the binary to exactly `./youtube-updater` (CI cache
  path contract).

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them.**

- [ ] Create `Makefile` at repo root with the content above (TAB-indented recipes)
- [ ] Verify: `make help` exits 0 and lists all 9 targets with descriptions
- [ ] Verify: `make build && test -x ./youtube-updater` — binary exists at the exact path
- [ ] Verify: `make test` passes (no Go changes yet; suite must stay green)
- [ ] Verify: `make vet` exits 0
- [ ] Verify: `make fmt-check` exits 0 on the current tree
- [ ] Verify: `make clean` removes the binary; `make build` rebuilds it
- [ ] Verify: `make run ARGS="--list"` output is byte-identical to `go run ./cmd --list`
- [ ] Lint: `gofmt -l .` — empty (Makefile is not Go, but confirm no stray files)

### Task 2: Create `.golangci.yml` (v2) with the agreed linter set (infrastructure)

Create `.golangci.yml` at the repo root. This config is the lint contract for
both local `make lint` and the CI gate (Task 5). Design decision: the v2
standard linter set + `misspell` + `copyloopvar`, with exactly one exclusion —
staticcheck QF1011 — because the 26 QF1011 findings all target the
`TestContractSurface` `var _ func(…) = …` blocks, whose explicit signatures
are the project's compile-time public-API check deliberately mirroring
CODEMANIFEST signatures. Weakening them would fight the project convention.

**Usages relevant to this task:**
- `golangci-lint` (`.goga/usages/cooks/golangci-lint.md`): v2 requires
  `version: "2"` as the first key; a v1-style config fails to parse on a v2
  binary.

Content verbatim from the design document:

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

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them.**

- [ ] Create `.golangci.yml` at repo root with the content above
- [ ] Verify: `make lint` runs with the new config and reports exactly the 18 errcheck findings (26 QF1011 suppressed) — findings NOT yet fixed; that is Task 3's scope
- [ ] Verify: config loads without parse errors (a missing `version: "2"` fails loudly)
- [ ] Record the finding count (18) as the baseline for Task 3

### Task 3: Fix 18 errcheck findings — production code (TDD-adjacent mechanical)

Apply explicit-ignore assignments (`_ =` / `_, _ =`) to the 8 production
errcheck sites. No signatures change, no behavior changes, no new logic. The
existing test suite is the contract safety net: it must stay green before and
after.

Production fix table (verbatim from the design document):

| File:line | Today | Fix |
| --- | --- | --- |
| `config/config.go:95` | `defer os.Remove(tmpName)` | `defer func() { _ = os.Remove(tmpName) }()` |
| `config/config.go:97` | `tmp.Close()` (error branch) | `_ = tmp.Close()` |
| `config/config.go:101` | `tmp.Close()` (error branch) | `_ = tmp.Close()` |
| `state/state.go:98` | `defer os.Remove(tmpName)` | `defer func() { _ = os.Remove(tmpName) }()` |
| `state/state.go:100` | `tmp.Close()` (error branch) | `_ = tmp.Close()` |
| `state/state.go:104` | `tmp.Close()` (error branch) | `_ = tmp.Close()` |
| `youtube/youtube.go:247` | `defer resp.Body.Close()` | `defer func() { _ = resp.Body.Close() }()` |
| `youtube/youtube.go:456` | `fmt.Fprintln(w, …)` (OAuth callback) | `_, _ = fmt.Fprintln(w, …)` |
| `youtube/youtube.go:460` | `defer srv.Shutdown(ctx)` | `defer func() { _ = srv.Shutdown(ctx) }()` |

Rationale notes:
- `_ = tmp.Close()` in an error branch intentionally swallows the close error
  — the primary error is returned; closing a temp on the failure path is
  cleanup.
- Line numbers are as of the probe run; match by content if they shifted.
- Do NOT touch `config/config.go:104` / `state/state.go:107` (`if err :=
  tmp.Close(); …` already checked) or `youtube/youtube.go:459` (already `_ =`).

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them. If implementation does not match the contract, fix the implementation — never fix the contract.**

- [ ] **Contract safety net**: run `go test ./...` — all green BEFORE any edit (baseline)
- [ ] **Code**: apply the 3 fixes in `config/config.go` (lines ~95, 97, 101)
- [ ] **Code**: apply the 3 fixes in `state/state.go` (lines ~98, 100, 104)
- [ ] **Code**: apply the 3 fixes in `youtube/youtube.go` (lines ~247, 456, 460)
- [ ] **Interface verification**: `go build ./...` — compiles; no exported identifier changed
- [ ] **Debugging**: `go test ./...` — all green after edits (fix code, not tests, if anything breaks)
- [ ] **Contract re-verification**: `grep -n "var _ func" */*_test.go` — TestContractSurface blocks untouched
- [ ] **Lint**: `make lint` — the 8 production errcheck findings are gone; 10 test findings remain (Task 4 scope)

### Task 4: Fix 10 errcheck findings — test code + usage-file amendment (TDD-adjacent mechanical)

Apply the same `_, _ =` pattern to the 10 test errcheck sites, then amend the
`golangci-lint` usage file with the QF1011 rationale so future implementing
agents find the deviation documented where they read config guidance.

Test fix table (verbatim from the design document):

| File:line | Fix |
| --- | --- |
| `cmd/main_test.go:268` | `w.Close()` → `_ = w.Close()` |
| `cmd/main_test.go:274` | `r.Close()` → `_ = r.Close()` |
| `cmd/main_test.go:287` | `w.Close()` → `_ = w.Close()` |
| `cmd/main_test.go:293` | `r.Close()` → `_ = r.Close()` |
| `youtube/youtube_test.go:97` | `fmt.Fprintf(w, …)` → `_, _ = fmt.Fprintf(w, …)` |
| `youtube/youtube_test.go:102` | `fmt.Fprint(w, …)` → `_, _ = fmt.Fprint(w, …)` |
| `youtube/youtube_test.go:115` | `fmt.Fprint(w, …)` → `_, _ = fmt.Fprint(w, …)` |
| `youtube/youtube_test.go:127` | `fmt.Fprint(w, …)` → `_, _ = fmt.Fprint(w, …)` |
| `youtube/sync_integration_test.go:47` | `fmt.Fprintf(w, …)` → `_, _ = fmt.Fprintf(w, …)` |
| `youtube/sync_integration_test.go:64` | `fmt.Fprintf(w, …)` → `_, _ = fmt.Fprintf(w, …)` |

Absolute rule: **do NOT touch any `var _ func(…) = …` TestContractSurface
line** — QF1011 stays suppressed at the config level, and the convention
survives verbatim.

Usage-file amendment: in `.goga/usages/cooks/golangci-lint.md`, extend the
Configuration section with the exclusion and why:

> The only deviation from defaults: staticcheck's QF1011 ("could omit type
> from declaration") is disabled because the project's `TestContractSurface`
> blocks deliberately pin explicit function signatures as a compile-time
> public-API check mirroring CODEMANIFEST contracts.

**Usages relevant to this task:**
- `golangci-lint` (`.goga/usages/cooks/golangci-lint.md`): amend the
  Configuration section — document `-QF1011` and the TestContractSurface
  rationale.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them.**

- [ ] **Contract safety net**: `go test ./...` — green before edits
- [ ] **Code**: apply the 4 `_ =` fixes in `cmd/main_test.go` (captureStdout/captureStderr helpers)
- [ ] **Code**: apply the 4 `_, _ =` fixes in `youtube/youtube_test.go`
- [ ] **Code**: apply the 2 `_, _ =` fixes in `youtube/sync_integration_test.go`
- [ ] **Docs**: amend `.goga/usages/cooks/golangci-lint.md` Configuration section with the QF1011 rationale
- [ ] **Debugging**: `go test ./...` — all green after edits (fix code, not tests, if anything breaks)
- [ ] **Contract re-verification**: TestContractSurface blocks byte-identical (`git diff` shows no `var _ func` line changed)
- [ ] **Lint**: `make lint` — exit 0, zero findings (this closes the 44-finding baseline)
- [ ] **Lint**: `gofmt -l .` — empty

### Task 5: Wire lint gate + `make` into CI workflows (infrastructure)

Switch both workflows from raw `go` commands to `make` targets and add the
lint gate. Behavior-preserving substitution verified by the design traces:
binary path, exit-code propagation through `make | tee` + pipefail, and
byte-identical stdout for the `--list` step summary.

**`sync.yml` — build job** (runs on push to master): insert the lint step
between `actions/setup-go` and the existing Build step, then rename the build
command:

```yaml
      - name: Lint
        uses: golangci/golangci-lint-action@v8
        with:
          version: v2.12.2

      - name: Build
        run: make build
```

**`sync.yml` — sync job** cold-cache fallback step (guarded by the same
`cache-hit != 'true'` condition):

```yaml
      - name: Build (cache miss)
        if: steps.bin.outputs.cache-hit != 'true'
        run: make build
```

**`status.yml`** — two substitutions:

- List step: `go run ./cmd --list` → `make run ARGS="--list"`
- Sync step: `go run ./cmd 2>&1 | tee /tmp/sync.log` → `make run 2>&1 | tee /tmp/sync.log`

Do NOT touch: cache keys, OAuth credential steps, sync invocation
(`./youtube-updater`), state commit steps, concurrency groups, triggers.

**Usages relevant to this task:**
- `golangci-lint` (`.goga/usages/cooks/golangci-lint.md`): CI wiring via
  `golangci/golangci-lint-action@v8` with `version: v2.12.2`; the action
  caches the linter binary between runs.

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them.**

- [ ] **Code**: `sync.yml` build job — insert Lint step (golangci-lint-action@v8, v2.12.2) after setup-go
- [ ] **Code**: `sync.yml` build job — Build step becomes `run: make build`
- [ ] **Code**: `sync.yml` sync job — Build (cache miss) step becomes `run: make build`
- [ ] **Code**: `status.yml` — List step uses `make run ARGS="--list"`
- [ ] **Code**: `status.yml` — Sync step uses `make run 2>&1 | tee /tmp/sync.log`
- [ ] Verify: `grep -n "go build\|go run" .github/workflows/*.yml` — no raw go build/run commands remain
- [ ] Verify: `grep -n "yt-binary\|hashFiles" .github/workflows/sync.yml` — cache keys untouched
- [ ] Verify YAML validity: `python3 -c "import yaml,sys; [yaml.safe_load(open(f)) for f in ['.github/workflows/sync.yml','.github/workflows/status.yml']]"`
- [ ] Lint (workflow-level sanity): the Lint step references the committed `.golangci.yml` from repo root (action default)

### Task 6: README + end-to-end validation (integration)

Update the README to make `make` the documented entry point and run the full
local verification chain exactly as the design's test scenarios prescribe.
This task is the plan's integration gate: everything before it must be green.

README changes:
- "Build" section: primary `make build` (keep `go build -o youtube-updater ./cmd` as an alternative)
- New short "Development" block: `make test`, `make lint`, `make fmt-check`, `make vet` + the golangci-lint install command (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`)

**CRITICAL: `CODEMANIFEST` files — read-only contract definitions. Do NOT modify them.**

- [ ] **Docs**: README Build section — `make build` primary, `go build` alternative
- [ ] **Docs**: README Development block — test/lint/fmt-check/vet + golangci-lint install
- [ ] **Integration — positive**: `make help` lists all 9 targets
- [ ] **Integration — positive**: `make build && test -x ./youtube-updater`
- [ ] **Integration — positive**: `make test` — all 5 packages ok
- [ ] **Integration — positive**: `make lint` — exit 0, no output
- [ ] **Integration — positive**: `make fmt-check` && `make vet` — exit 0
- [ ] **Integration — edge**: `make clean && make build` round-trips
- [ ] **Integration — edge**: `make run ARGS="--list"` output identical to `go run ./cmd --list` (diff empty)
- [ ] **Integration — negative**: introduce a temp errcheck violation → `make lint` exit 1 with an errcheck finding → revert → exit 0
- [ ] **Integration — negative**: break formatting → `make fmt-check` exit 1 → `make fmt` → `make fmt-check` exit 0
- [ ] Final: `git status` — only intended files changed; no `youtube-updater` binary staged (gitignored)

---

## Validation Commands

- `make lint`: golangci-lint with the committed `.golangci.yml` — must exit 0 with zero findings
- `make fmt-check`: gofmt verification, non-mutating — must exit 0
- `make vet`: go vet across the module — must exit 0
- `make test`: full Go test suite (`go test ./...`) — all 5 packages green
- `make build && test -x ./youtube-updater`: binary produced at the CI-cache path
- `goga lint`: cell manifests still valid — `cells: 5 errors: 0`
- `grep -rn "var _ func" */*_test.go`: TestContractSurface convention preserved verbatim
- `grep -n "go build\|go run" .github/workflows/*.yml`: no raw go commands remain in CI

## Completion Criteria

- [ ] Every contract entity is implemented in the correct `location` — N/A: no contract entities changed; existing implementations untouched except `_ =` prefixes
- [ ] Every contract entity is accessible from the facade — verified by TestContractSurface staying green
- [ ] Properties and methods match the declared API — no exported identifier changed (`go build ./...` + green suite)
- [ ] Descriptions are reflected in behavior — N/A for this plan (no contract descriptions in scope)
- [ ] Contract dependencies are met — untouched
- [ ] Re-exports are accessible from the facade — N/A (none exist)
- [ ] Every coding task followed the TDD workflow — adapted: Tasks 3–4 are mechanical errcheck fixes whose "contract tests" are the pre-existing green suite + TestContractSurface blocks (run before and after); no new Go entities are created, so no new contract tests are writable
- [ ] Contract tests and logic tests cover facade, API, and behavior within each coding task — existing suite serves this role; shell-level positive/negative/edge verifications embedded in Tasks 1 and 6
- [ ] Integration tests exist where cross-entity scenarios require them — Task 6 embeds the design's 9 verification scenarios
- [ ] No package boundary was expanded — no new packages/cells
- [ ] `CODEMANIFEST` files were not modified (contract is read-only)
- [ ] All validation commands pass
- [ ] Every Usages entry is mentioned in at least one task — `golangci-lint` in Tasks 2, 4, 5
