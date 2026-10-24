// Package syncer orchestrates the per-channel sync: seed on first contact (add
// nothing), otherwise add only videos newer than the channel's watermark.
package syncer

import (
	"fmt"
	"slices"
	"time"

	"youtube-updater/config"
	"youtube-updater/state"
	"youtube-updater/youtube"
)

// ChannelResult is the outcome of syncing one channel, for logging.
type ChannelResult struct {
	ChannelID  string
	PlaylistID string
	Seeded     bool     // true if this run only seeded the channel (added nothing)
	NewCount   int      // number of new videos detected
	AddedIDs   []string // identifiers of videos inserted this run
	Err        error    // error encountered for this channel, if any
}

// SyncAll syncs every configured channel against its watermark. dryRun performs
// no inserts and mutates no state, but still reports what would be added.
func SyncAll(yt *youtube.YouTube, st *state.State, mappings []config.ChannelMapping, dryRun bool) ([]ChannelResult, error) {
	results := make([]ChannelResult, 0, len(mappings))
	for _, m := range mappings {
		results = append(results, syncOne(yt, st, m, dryRun))
	}
	return results, nil
}

func syncOne(yt *youtube.YouTube, st *state.State, m config.ChannelMapping, dryRun bool) ChannelResult {
	r := ChannelResult{ChannelID: m.ChannelID, PlaylistID: m.PlaylistID}

	uploadsID, err := yt.ResolveUploads(m.ChannelID)
	if err != nil {
		r.Err = fmt.Errorf("channel %s: %w", m.ChannelID, err)
		return r
	}
	videos, err := yt.ListUploads(uploadsID, st.LastSeenAt(m.ChannelID))
	if err != nil {
		r.Err = fmt.Errorf("uploads %s: %w", m.ChannelID, err)
		return r
	}

	// Empty uploads: skip — no cursor set, channel stays unseeded.
	if len(videos) == 0 {
		return r
	}

	newest := videos[0]

	// First contact: seed the cursor to the newest video and add nothing.
	if !st.IsSeeded(m.ChannelID) {
		r.Seeded = true
		if !dryRun {
			if err := st.SetLastSeen(m.ChannelID, newest.ID, newest.PublishedAt); err != nil {
				r.Err = fmt.Errorf("seed %s: %w", m.ChannelID, err)
			}
		}
		return r
	}

	// Subsequent contact: add only videos newer than the watermark.
	mark := st.LastSeenAt(m.ChannelID)
	isNew := selectNew(videos, mark)

	// Sync only regular long-form uploads: drop Shorts and live streams. The
	// watermark still advances to `newest` regardless, so excluded uploads are
	// never re-scanned on later runs.
	toAdd, err := yt.FilterRegularVideos(isNew)
	if err != nil {
		r.Err = fmt.Errorf("classify %s: %w", m.ChannelID, err)
		return r
	}
	r.NewCount = len(toAdd)
	if dryRun {
		r.AddedIDs = idsOf(toAdd)
		return r
	}

	kept := make(map[string]bool, len(toAdd))
	for _, v := range toAdd {
		kept[v.ID] = true
	}

	// Insert oldest→newest to keep playlist chronological. toAdd is
	// newest-first (same order as ListUploads), so walk it in reverse. On an
	// insert failure, stop: AddedIDs keeps only what actually went in, and the
	// watermark parks at the newest processed video strictly older than the
	// failed one, so the failed and not-yet-inserted ones stay new for the
	// next run instead of being silently dropped.
	var addErr error
	var failed youtube.Video
	processed := make([]youtube.Video, 0, len(isNew))
	for _, v := range slices.Backward(toAdd) {
		if _, err := yt.AddToPlaylist(m.PlaylistID, v.ID); err != nil {
			addErr = fmt.Errorf("add %s/%s: %w", m.ChannelID, v.ID, err)
			failed = v
			break
		}
		r.AddedIDs = append(r.AddedIDs, v.ID)
		processed = append(processed, v)
	}
	if addErr == nil {
		// Full success (including a batch of only Shorts/streams): advance to
		// the newest upload.
		if err := st.SetLastSeen(m.ChannelID, newest.ID, newest.PublishedAt); err != nil {
			r.Err = fmt.Errorf("advance %s: %w", m.ChannelID, err)
		}
		return r
	}
	r.Err = addErr
	// Shorts/streams count as processed only if the walk had already passed
	// them: positionally AFTER the failed video in isNew (the walk runs from
	// the list's end toward its front). Excluded uploads before it were never
	// reached and must not pull the watermark past the failure point, or
	// regular videos newer than it would be lost silently.
	cut := slices.IndexFunc(isNew, func(v youtube.Video) bool { return v.ID == failed.ID })
	if cut < 0 {
		cut = len(isNew) // defensive: the failed ID is always in isNew
	}
	// Augment only when the walk had at least one successful insert: with zero
	// successes there is nothing parked over, and excluded videos alone could
	// pull the watermark past regular videos that were never attempted.
	if len(processed) > 0 {
		for i, v := range isNew {
			if i > cut && !kept[v.ID] {
				processed = append(processed, v)
			}
		}
	}
	if len(processed) == 0 {
		return r // nothing processed: leave the cursor untouched, retry the whole batch
	}
	// Clamp the parking point to processed videos strictly OLDER than the
	// failed one. Deliberate no-silent-loss trade-off: a processed timestamp
	// newer than the failure can only come from list-order inversion (a
	// premiere/late-published upload sitting deeper in the list). Parking over
	// the failed video's timestamp — the raw max — would make the next run's
	// selectNew skip it forever: an episode silently lost. Duplicates of the
	// already-inserted inverted videos on the retry are the accepted price —
	// they are visible in the playlist and cleanable; a lost episode is not.
	older := make([]youtube.Video, 0, len(processed))
	failedAt, failedErr := time.Parse(time.RFC3339, failed.PublishedAt)
	for _, v := range processed {
		at, err := time.Parse(time.RFC3339, v.PublishedAt)
		if failedErr == nil && err == nil && at.Before(failedAt) {
			older = append(older, v)
		}
	}
	// Everything processed (if anything) is newer than the failure: same call
	// as zero processed — untouched cursor, whole batch retried, possible
	// duplicates of what did land included.
	if len(older) == 0 {
		return r
	}
	park := newestOf(older)
	if err := st.SetLastSeen(m.ChannelID, park.ID, park.PublishedAt); err != nil {
		// The insert failure is primary — the advance error must not overwrite it.
		r.Err = fmt.Errorf("%w (watermark advance failed: %v)", addErr, err)
	}
	return r
}

// newestOf returns the video with the maximum PublishedAt (RFC3339). Callers
// pass selectNew output, so timestamps parse; defensively, an unparseable
// timestamp loses to any parseable one.
func newestOf(vs []youtube.Video) youtube.Video {
	best := vs[0]
	bestAt, _ := time.Parse(time.RFC3339, best.PublishedAt)
	for _, v := range vs[1:] {
		at, err := time.Parse(time.RFC3339, v.PublishedAt)
		if err != nil {
			continue
		}
		if bestAt.IsZero() || at.After(bestAt) {
			best, bestAt = v, at
		}
	}
	return best
}

// selectNew returns the subset of videos whose publish time is strictly after the
// watermark. An empty/unparseable watermark selects all (defensive — a seeded
// channel always has a valid watermark). Order is preserved (newest-first).
// Comparison parses to time.Time, never lexical — see TestSelectNew_ParsesToTime.
func selectNew(videos []youtube.Video, watermark string) []youtube.Video {
	if len(videos) == 0 {
		return nil
	}
	mark, err := time.Parse(time.RFC3339, watermark)
	if err != nil {
		mark = time.Time{}
	}
	var out []youtube.Video
	for _, v := range videos {
		t, err := time.Parse(time.RFC3339, v.PublishedAt)
		if err != nil {
			continue // skip unparseable timestamps
		}
		if t.After(mark) {
			out = append(out, v)
		}
	}
	return out
}

func idsOf(vs []youtube.Video) []string {
	ids := make([]string, len(vs))
	for i, v := range vs {
		ids[i] = v.ID
	}
	return ids
}
