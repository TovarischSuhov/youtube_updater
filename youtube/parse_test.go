package youtube

import "testing"

func TestParsePlaylistID(t *testing.T) {
	const pl = "PL1234567890abcdefghij" // 22 chars, matches playlistIDRe
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"bare id", pl, pl, true},
		{"bare id with underscore and dash", "PL_abc-def1234567890", "PL_abc-def1234567890", true},
		{"playlist url", "https://www.youtube.com/playlist?list=" + pl, pl, true},
		{"watch url with list", "https://www.youtube.com/watch?v=abc12345678&list=" + pl, pl, true},
		{"short url with list", "https://youtu.be/abc12345678?list=" + pl, pl, true},
		{"schemeless playlist url", "youtube.com/playlist?list=" + pl, pl, true},
		{"watch url without list", "https://www.youtube.com/watch?v=abc12345678", "", false},
		{"short url without list", "https://youtu.be/abc12345678", "", false},
		{"empty", "", "", false},
		{"whitespace only", "   ", "", false},
		{"url with no list param", "https://www.youtube.com/feed/trending", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParsePlaylistID(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseChannelRef(t *testing.T) {
	const ch = "UC1234567890abcdefghijkl" // UC + 22 = 24 chars
	tests := []struct {
		name string
		in   string
		kind string
		id   string
		slug string
		ok   bool
	}{
		{"bare id", ch, "id", ch, "", true},
		{"channel url", "https://www.youtube.com/channel/" + ch, "id", ch, "", true},
		{"schemeless channel url", "youtube.com/channel/" + ch, "id", ch, "", true},
		{"channel url with trailing slash", "https://www.youtube.com/channel/" + ch + "/", "id", ch, "", true},
		{"handle url", "https://www.youtube.com/@SomeHandle", "handle", "", "SomeHandle", true},
		{"schemeless handle", "youtube.com/@SomeHandle", "handle", "", "SomeHandle", true},
		{"handle url with trailing path", "https://www.youtube.com/@SomeHandle/videos", "handle", "", "SomeHandle", true},
		{"custom url", "https://www.youtube.com/c/CustomName", "custom", "", "CustomName", true},
		{"user url", "https://www.youtube.com/user/LegacyName", "user", "", "LegacyName", true},
		{"watch url", "https://www.youtube.com/watch?v=abc12345678", "", "", "", false},
		{"empty", "", "", "", "", false},
		{"garbage", "not a url or id", "", "", "", false},
		{"bare too short", "UCa", "", "", "", false},
		{"bare channel-like wrong length", "UC1234567890", "", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref, ok := ParseChannelRef(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got %+v)", ok, tc.ok, ref)
			}
			if !ok {
				return
			}
			if ref.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", ref.Kind, tc.kind)
			}
			if ref.ID != tc.id {
				t.Errorf("id = %q, want %q", ref.ID, tc.id)
			}
			if ref.Slug != tc.slug {
				t.Errorf("slug = %q, want %q", ref.Slug, tc.slug)
			}
		})
	}
}
