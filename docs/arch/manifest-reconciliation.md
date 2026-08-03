# Architecture Plan — Manifest Reconciliation

## Context

Reconcile the five existing cells (`cmd`, `config`, `state`, `syncer`, `youtube`) with
the manual code updates made since the last reconciliation commit (`583cb9e`). Source
task: [manifest-reconciliation](../tasks/manifest-reconciliation.md).

The architecture is already designed and `goga contract`-verified faithful. This plan
therefore specifies **one manifest edit** — the `syncer` dotted-link lint fix — plus a
per-cell **verification** pass. It introduces **no** new cells, types, or signatures.

## Design baseline (approved as-is)

The five current CODEMANIFESTs are the approved baseline. Their type map, detailing,
cell distribution, usages, and annotations are retained unchanged, including:

- Entity vs Routine modelling and the `(result, error)` idiom.
- The type-name constructor form (`State(path)`, `YouTube(...)`) — consistent with the
  `goga-cell-go` example (`Server(host string)`) and accepted by `goga contract`.
- The import graph below.

## Dependency map

```
config  ──(ChannelMapping)─────────────┐
state   ──(State)────────────────────  ├──▶ syncer ──(SyncAll, ChannelResult)──▶ cmd
youtube ──(YouTube, Video, ChannelRef)─┘
```

No cross-imports; acyclic.

## Implementation order (leaves → root)

| # | Cell    | Reason / action                                                                                  |
|---|---------|--------------------------------------------------------------------------------------------------|
| 1 | config  | Leaf (no Imports). Verify only — untouched by the manual updates.                               |
| 2 | state   | Leaf. Verify only — `LastSync` already documented in `5533d7e`.                                  |
| 3 | youtube | Leaf (external API via `cooks`). Verify only — `FilterRegularVideos`, `.usages/facade.md`, `export_test.go` already updated in `cc2ab14`. |
| 4 | syncer  | Depends on config/state/youtube. **Apply the one manifest edit** (below), then verify.           |
| 5 | cmd     | Root; depends on all. Verify only — `RunStatus` already documented in `5533d7e`.                 |

## Artifacts

### config — `config/CODEMANIFEST` (unchanged)

Verify only. Confirm `ChannelMapping` (ChannelID, PlaylistID, ChannelName, PlaylistName)
and `LoadConfig` / `SaveConfig` / `AddMapping` / `RemoveMapping` match `config/config.go`.
**No edit.**

### state — `state/CODEMANIFEST` (unchanged)

Verify only. Confirm `State`, the `Path` property, and `IsSeeded` / `LastSeenID` /
`LastSeenAt` / `LastSync` / `SetLastSeen` / `Save` match `state/state.go`. **No edit.**

### youtube — `youtube/CODEMANIFEST` (unchanged) + `.usages/facade.md` + `export_test.go`

Verify only. Confirm `Video`, `YouTube` (six methods incl. `FilterRegularVideos`),
`ChannelRef` + `IsID`, `ParsePlaylistID`, `ParseChannelRef` match `youtube/youtube.go`
+ `youtube/parse.go`; that `.usages/facade.md`'s `FilterRegularVideos` section matches
the implementation; and that `export_test.go` still exposes only `NewWithService`.
**No edit.**

### syncer — `syncer/CODEMANIFEST` (one edit) + `.usages/sync.md`

Verify `ChannelResult` (ChannelID, PlaylistID, Seeded, NewCount, AddedIDs, Err) and
`SyncAll`; confirm `.usages/sync.md` (Shorts/stream skip + watermark advances) is
accurate.

**Diff** — in the `SyncAll` annotation, make the `FilterRegularVideos` reference resolvable:

```diff
        - read the watermark from `State`
        - select `Video` items whose PublishedAt is greater than the watermark
-       - drop Shorts and live streams via `YouTube.FilterRegularVideos`, keeping
+       - drop Shorts and live streams via `YouTube` (FilterRegularVideos), keeping
          only regular long-form uploads
        - unless `dryRun`, insert each kept `Video` into the playlist via
```

**Rationale:** the DSL resolves the imported **type** `YouTube`, not `Type.Method` —
`` `YouTube.FilterRegularVideos` `` is what breaks `goga lint`/`goga schema`. The
parenthetical names the method in prose (no backticks), matching the manifest's own
convention elsewhere ("Resolve the channel's uploads playlist via `YouTube`"). The
watermark-advances-past-excluded requirement is unchanged.

### cmd — `cmd/CODEMANIFEST` (unchanged)

Verify only. Confirm the seven routines — `Run`, `RunAdd`, `RunRemove`, `RunList`,
`RunStatus`, `SetupLogging`, `EnsureConfig` — match `cmd/main.go`. **No edit.**

## Verification checklist (run after applying the `syncer` diff)

- [ ] `goga lint` → exit **0**, 0 errors (currently 1: the dotted link).
- [ ] `goga schema` → succeeds (currently fails on the same error).
- [ ] `goga contract` → exit 0 (unchanged; already passes).
- [ ] `go test ./...` → passes.
- [ ] config: no orphan symbols either direction.
- [ ] state: no orphans; `LastSync` present.
- [ ] youtube: no orphans; `FilterRegularVideos` annotation matches impl (Shorts ≤ 60s;
      streams via `liveStreamingDetails`; ≤ 50-id batching; deleted/private dropped;
      unparseable duration kept).
- [ ] syncer: no orphans; `SyncAll` annotation has no `Type.Method` backtick links.
- [ ] cmd: no orphans; `RunStatus` present.

## Plan-level verification (Phase 6)

DSL correctness, inter-cell consistency, implementation order, no placeholders,
`Imports.Types` usage, reference resolvability, `location` restrictions, no
cross-imports, Entity/Routine correctness, and language correctness are all satisfied
by the baseline (already `goga lint`-clean except the one fix) and preserved by the
diff. Base usages `youtube-data-api` and `google-oauth2` are scoped to the `youtube`
cell per the config annotations; `goga lint` reports no missing-usage errors elsewhere.
