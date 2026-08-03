# Manifest Reconcile — config

Parent: [manifest-reconciliation](manifest-reconciliation.md).

## Current State

`config` owns the channel→playlist data model, YAML load/save, and upsert/remove. It
was **not** changed by the recent manual updates, so its CODEMANIFEST should be
unchanged and faithful. This sub-task confirms that rather than assuming it.

## Description

Verify `config/CODEMANIFEST` against `config/config.go`.

## Scope

**In scope:**

| Manifest element | Go |
|---|---|
| `ChannelMapping()` props ChannelID, PlaylistID, ChannelName, PlaylistName | `config.ChannelMapping` struct (yaml: channel_id/playlist_id/channel_name/playlist_name) |
| `LoadConfig(path) -> mappings, err` | `LoadConfig(path string) ([]ChannelMapping, error)` |
| `SaveConfig(path, mappings) -> err` | `SaveConfig(path string, mappings []ChannelMapping) error` |
| `AddMapping(mappings, channelID, playlistID, channelName, playlistName) -> updated` | `AddMapping(...) []ChannelMapping` |
| `RemoveMapping(mappings, channelID) -> updated` | `RemoveMapping(mappings []ChannelMapping, channelID string) []ChannelMapping` |

**Out of scope:** the on-disk YAML schema; behaviour changes.

## Acceptance Criteria

- All four routines + the `ChannelMapping` type (4 fields) are present and faithful,
  with no orphans either direction.
- `goga lint` reports nothing for `config`.
- `go test ./config` passes.

## Stack

Go + `gopkg.in/yaml.v3`. No new dependencies.

## External Dependencies

None (the `yaml` practice is inline in the manifest header).

## Risks and Constraints

None material — cell is stable.

## Scope Estimate

Single sub-task — small. One file (`config/config.go` ↔ `config/CODEMANIFEST`).

## Existing Architecture

Leaf cell; imported by `cmd` and `syncer` (`ChannelMapping`).

## Notes

Expected outcome: faithful, no edit required.
