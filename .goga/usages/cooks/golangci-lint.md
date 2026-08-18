# golangci-lint (v2)

## Domain

This usage describes how to run **golangci-lint v2** for static analysis of this
Go module: the v2 configuration file format, the lint target in the Makefile,
and the CI integration via the official GitHub Action.

**Audience:** the implementing agent and any developer adding linters or
debugging lint failures. Assumes Go 1.26.3 toolchain (matches `go.mod`).

## Prerequisites

Local install (binary must be v2.x — v1 configs are NOT parsed by a v2 binary):

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
golangci-lint --version   # expect: 2.12.2
```

## Configuration

v2 requires `version: "2"` as the first key of `.golangci.yml`. This project
uses the v2 default linter set (errcheck, govet, ineffassign, staticcheck,
unused) plus `misspell` and `copyloopvar`, with default settings:

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

The only deviation from defaults: staticcheck's QF1011 ("could omit type from
declaration") is disabled because the project's `TestContractSurface` blocks
deliberately pin explicit function signatures as a compile-time public-API
check mirroring CODEMANIFEST contracts. Do not "fix" those declarations.

Migrating a legacy v1 config (if one ever appears):

```bash
golangci-lint migrate   # converts .golangci.yml in place
```

## Running

```bash
make lint              # project entry point → golangci-lint run (repo root)
golangci-lint run      # equivalent direct call
golangci-lint run ./syncer/...   # single package while iterating
```

The linter runs over the whole module, not per-cell. Cell CODEMANIFEST
boundaries are unaffected: lint findings are advisory gates for CI, not part of
any cell's public surface.

## CI integration

In the `build` job of `.github/workflows/sync.yml`, after checkout and
`actions/setup-go`:

```yaml
      - uses: golangci/golangci-lint-action@v8
        with:
          version: v2.12.2
```

The action downloads the pinned version, caches it between runs, and fails the
job on any finding. Pin the version explicitly so local and CI results match.

## Notes

- Fix findings in the code rather than disabling linters; add `//nolint`
  sparingly and always with a reason comment.
- New linters are added to `.golangci.yml` `linters.enable` only — no custom
  settings unless a default proves noisy.
