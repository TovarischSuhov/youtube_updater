# syncer — consumer guide

## Domain
How to drive the orchestrator (package `syncer`) and read its results.

## Run a sync

    results, err := syncer.SyncAll(youTube, state, mappings, dryRun)

- Pass dryRun=true to detect new videos without inserting and without mutating state.
- `State` is mutated in place during the call; the caller persists it afterwards.

## Read ChannelResult
Each result reports ChannelID, PlaylistID, Seeded (first-contact run, nothing
added), NewCount, AddedIDs, and Err (set if that channel failed; other channels are
still processed). NewCount and AddedIDs count only regular long-form videos:
Shorts and live streams are skipped, and the watermark still advances past them so
they are never re-added.
