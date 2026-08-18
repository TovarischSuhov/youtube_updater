# Makefile for transparent build, test, and lint

## Current State

- The project is a Go CLI (`youtube-updater`, Go 1.26.3, single binary from `./cmd`)
  with no `Makefile`. Build/test/lint commands are typed by hand:
  `go build -o youtube-updater ./cmd`, `go test ./...`, `go vet ./...`.
- CI hardcodes these commands as raw strings:
  - `.github/workflows/sync.yml` — `go build -o youtube-updater ./cmd` in the
    `build` job (line 65) and in the sync job's cold-cache fallback (line 112).
  - `.github/workflows/status.yml` — `go run ./cmd --list` (line 63) and
    `go run ./cmd` (line 70).
  Command drift between local and CI is possible; there is no single point of
  truth for how the project is built and tested.
- No static-analysis tooling: no `golangci-lint` config, no lint gate anywhere.
- All tests are hermetic (httptest mocks, no real API calls); there is no
  `testing.Short` gating — `go test ./...` runs the full suite.

## Description

Add a self-documenting `Makefile` as the single entry point for building,
testing, and linting the project, and switch the CI workflows to invoke `make`
targets instead of raw `go` commands. Introduce `golangci-lint` (v2.12.2) with
a minimal v2 config, expose it as `make lint`, gate the CI `build` job on it,
and fix any findings it reports in the existing Go code.

## Scope

**In scope:**
- `Makefile` at the repo root with targets (first target = `help`):
  - `make help` — self-documenting target list (parses `##` comments)
  - `make build` — `go build -o youtube-updater ./cmd`
  - `make test` — `go test ./...`
  - `make vet` — `go vet ./...`
  - `make fmt` — apply `gofmt` to project sources
  - `make fmt-check` — verify formatting without modifying (CI-style)
  - `make lint` — `golangci-lint run`
  - `make run` — `go run ./cmd`, passing extra flags via `make run ARGS="--dry-run"`
  - `make clean` — remove the `youtube-updater` binary
- `.golangci.yml` — v2 format (`version: "2"`), default linter set
  (errcheck, govet, ineffassign, staticcheck, unused) + `misspell`,
  `copyloopvar`, default settings
- CI switch to make:
  - `sync.yml` build job: replace `go build -o youtube-updater ./cmd` with
    `make build`; sync job cold-cache fallback likewise
  - `status.yml`: replace `go run ./cmd --list` with `make run ARGS="--list"`,
    `go run ./cmd` with `make run`
- CI lint gate: `golangci/golangci-lint-action@v8` (pinned `v2.12.2`) in the
  `sync.yml` `build` job, after checkout and `actions/setup-go`
- Fixing lint findings in existing Go code (small, mechanical fixes; no
  behavior changes)
- `make build` must keep producing the binary at the exact path `youtube-updater`
  in repo root (the sync job's binary cache and `./youtube-updater` invocation
  depend on it)

**Out of scope:**
- Changes to cell public APIs (cmd/config/state/syncer/youtube types and
  signatures) — lint fixes are internal
- New tool behavior or CLI flags
- Additional linters beyond the v2 defaults + misspell + copyloopvar (future task)
- Custom linter settings, `//nolint` policies beyond occasional justified use
- Pre-commit hooks, goreleaser, cross-compilation

## Acceptance Criteria

- `make help` prints the target list; `make build` produces `./youtube-updater`;
  `make test` passes; `make vet` and `make fmt-check` exit 0; `make lint` exits 0
  (after fixes); `make clean` removes the binary; `make run ARGS="--list"`
  executes the tool (with credentials present).
- `golangci-lint run` exits 0 locally with the committed `.golangci.yml`.
- `.github/workflows/sync.yml` contains no raw `go build` string: the build job
  runs the lint action then `make build`; the cold-cache fallback runs
  `make build`.
- `.github/workflows/status.yml` runs the tool via `make run` (incl. `--list`).
- Existing Go tests still pass unmodified in behavior (`make test` green).
- README "Project layout" / "Build" sections mention `make build` / `make test`
  as the primary commands.

## Stack

- **Frameworks:** none (plain Go stdlib toolchain)
- **Libraries:** none new (golangci-lint is a dev tool, not an import)
- **Infrastructure:** GNU Make 4.x (preinstalled on ubuntu-latest runners and
  standard on dev machines); golangci-lint v2.12.2 (local:
  `go install …/cmd/golangci-lint@v2.12.2`, CI: `golangci/golangci-lint-action@v8`)

## External Dependencies

| Component     | Usage file                             | Status  |
|---------------|----------------------------------------|---------|
| golangci-lint | `.goga/usages/cooks/golangci-lint.md` | created |
| make          | —                                      | not needed (standard tool, no consumption patterns) |
| golangci-lint-action | covered by golangci-lint usage    | covered |

## Risks and Constraints

- **Lint findings in existing code** — unknown until first run; expected to be
  minor (the code is young and idiomatic), but errcheck/staticcheck may flag
  ignored errors (e.g. `w.(http.Flusher)` in streaming or JSON writes). Fixes
  must not alter behavior.
- **CI cold-cache path** — the sync job's fallback build step runs `make build`;
  make is preinstalled on GitHub runners, no extra setup needed.
- **golangci-lint version pinning** — local and CI versions must match (both
  pinned to v2.12.2) to avoid result drift.
- **v2 config format** — requires `version: "2"` first key; a v1-style config
  silently fails to parse on a v2 binary.
- **`make run` with pipes** — status.yml pipes sync output through `tee`;
  `make run 2>&1 | tee` works, but the Makefile must not swallow the exit code
  (GNU make propagates the child's exit status).

## Scope Estimate

Single task. All parts serve one goal (transparent build/test/lint entry point),
are verified together by `make lint && make test && make build`, and touch a
small, homogeneous surface: 1 new Makefile, 1 new `.golangci.yml`, 2 edited
workflow files, README touch-up, plus small lint fixes in existing Go code.

## Existing Architecture

No cell changes. The task operates above the cell layer:

- `cmd/` — untouched (binary entry point, `make build`/`make run` target)
- `config/`, `state/`, `syncer/`, `youtube/` — untouched except possible
  mechanical lint fixes that don't change exported signatures
- Cells' CODEMANIFESTs and `.usages` files are unaffected (no public API change;
  see project convention: keep CODEMANIFEST + .usages + TestContractSurface in
  sync with public API changes — none are planned)
- The lint gate is advisory infrastructure, not part of any cell's surface

## Notes

- Original user request: «Давай добавим Makefile для прозрачной сборки и тестов
  проекта» + mid-session addition: «давай добавим еще make lint, подключим
  golangci-lint».
- User approved during formulation: Makefile + CI switch (single point of
  truth), golangci-lint in CI with fixes for findings, default v2 linter set +
  misspell + copyloopvar, single-task scope.
- Usage file `.goga/usages/cooks/golangci-lint.md` was created and registered
  in `.goga/config.yml` (usages + annotations) during task formulation.
- `make help` convention: targets annotated with `## description`; the help
  target greps the Makefile itself.
- Keep the sync.yml binary cache key (`hashFiles('**/*.go', 'go.mod', 'go.sum')`)
  unchanged — `make build` writes the identical artifact path.
