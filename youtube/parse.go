// This file holds the pure (no-API) parsing of channel and playlist references
// from a bare ID or a YouTube URL. API-side resolution of slugs lives on YouTube.
package youtube

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	// channelIDRe matches a bare YouTube channel ID: "UC" followed by 22 chars.
	channelIDRe = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
	// playlistIDRe matches a playlist token loosely (PL, FL, LL, RD, OL, WL, …).
	// The API rejects anything invalid downstream, so this stays permissive.
	playlistIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{10,}$`)
)

// ChannelRef is a parsed channel reference: either a concrete channel ID, or a
// slug (handle / custom / username) that still needs API resolution.
type ChannelRef struct {
	Kind string // "id" | "handle" | "custom" | "user"
	ID   string // set when Kind == "id"
	Slug string // set when Kind != "id" (no leading @, no surrounding slashes)
}

// IsID reports whether the reference is already a concrete channel ID and needs
// no API lookup.
func (r ChannelRef) IsID() bool { return r.Kind == "id" }

// ParsePlaylistID extracts a playlist ID from a bare ID or any YouTube URL
// carrying a ?list= query parameter (a playlist or watch URL). Returns ok=false
// when no ID-shaped value is found.
func ParsePlaylistID(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if !looksLikeYouTubeURL(s) {
		// Bare token: validate as a playlist ID.
		if playlistIDRe.MatchString(s) {
			return s, true
		}
		return "", false
	}
	u, ok := parseYouTubeURL(s)
	if !ok {
		return "", false
	}
	if id := u.Query().Get("list"); playlistIDRe.MatchString(id) {
		return id, true
	}
	return "", false
}

// ParseChannelRef classifies a channel reference from a bare ID or a YouTube URL:
//
//	UC…                  → id
//	…/channel/UC…        → id
//	…/@handle            → handle  (needs API)
//	…/c/CustomName       → custom  (needs API)
//	…/user/LegacyName    → user    (needs API)
//
// Returns ok=false for anything else (watch URLs, garbage, empty input).
func ParseChannelRef(s string) (ChannelRef, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ChannelRef{}, false
	}
	if !looksLikeYouTubeURL(s) {
		// Bare token: validate as a channel ID.
		if channelIDRe.MatchString(s) {
			return ChannelRef{Kind: "id", ID: s}, true
		}
		return ChannelRef{}, false
	}
	u, ok := parseYouTubeURL(s)
	if !ok {
		return ChannelRef{}, false
	}
	seg := splitPath(u.Path)
	switch {
	case len(seg) >= 2 && seg[0] == "channel":
		if channelIDRe.MatchString(seg[1]) {
			return ChannelRef{Kind: "id", ID: seg[1]}, true
		}
		return ChannelRef{}, false
	case len(seg) >= 1 && strings.HasPrefix(seg[0], "@"):
		if slug := strings.TrimPrefix(seg[0], "@"); slug != "" {
			return ChannelRef{Kind: "handle", Slug: slug}, true
		}
	case len(seg) >= 2 && seg[0] == "c":
		return ChannelRef{Kind: "custom", Slug: seg[1]}, true
	case len(seg) >= 2 && seg[0] == "user":
		return ChannelRef{Kind: "user", Slug: seg[1]}, true
	}
	return ChannelRef{}, false
}

// looksLikeYouTubeURL reports whether s should be parsed as a URL rather than a
// bare token. Anything referencing a YouTube host is treated as a URL.
func looksLikeYouTubeURL(s string) bool {
	return strings.Contains(s, "youtube.com") || strings.Contains(s, "youtu.be")
}

// parseYouTubeURL parses a YouTube URL, prepending https:// when the scheme is
// omitted (e.g. "youtube.com/@x"). ok=false if the value cannot be parsed.
func parseYouTubeURL(s string) (*url.URL, bool) {
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u == nil {
		return nil, false
	}
	return u, true
}

// splitPath splits a URL path into its non-empty segments.
func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}
