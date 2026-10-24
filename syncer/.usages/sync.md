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

Failure semantics: AddedIDs lists only the videos actually inserted, in insertion
order (in dry-run, the would-be list). If an insert fails mid-batch, insertion
stops, Err carries the failure, and the watermark parks at the newest processed
video strictly older than the failed one (inserted plus the skipped
Shorts/streams) — so the failed and not-yet-inserted videos are retried on the
next run. An inverted upload newer than the failed one (newer timestamp sitting
deeper in the list) is never parked over: re-inserting it on the retry leaves a
visible, cleanable duplicate — the accepted price of never silently losing the
failed video. If no processed video is older than the failure (nothing was
processed, or every processed timestamp is newer via inversion), the cursor is
left untouched and the whole batch is retried. On full success the cursor still
advances to the newest upload.
