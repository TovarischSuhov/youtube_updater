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
	videos, err := yt.ListUploads(uploadsID)
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
	r.AddedIDs = idsOf(toAdd)

	if !dryRun {
		// Insert oldest→newest to keep playlist chronological. toAdd is
		// newest-first (same order as ListUploads), so walk it in reverse.
		for _, v := range slices.Backward(toAdd) {
			if _, err := yt.AddToPlaylist(m.PlaylistID, v.ID); err != nil {
				r.Err = fmt.Errorf("add %s/%s: %w", m.ChannelID, v.ID, err)
				break
			}
		}
		if err := st.SetLastSeen(m.ChannelID, newest.ID, newest.PublishedAt); err != nil {
			r.Err = fmt.Errorf("advance %s: %w", m.ChannelID, err)
		}
	}
	return r
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
