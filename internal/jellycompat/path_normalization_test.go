package jellycompat

import "testing"

// TestCanonicalizeCompatPathLocalTrailers covers #1097: generated Jellyfin
// clients such as Moonfin send lowercase paths, and /items/{id}/localtrailers
// must reach the /Items/{id}/LocalTrailers route instead of a 404.
func TestCanonicalizeCompatPathLocalTrailers(t *testing.T) {
	for in, want := range map[string]string{
		"/items/abc123/localtrailers":              "/Items/abc123/LocalTrailers",
		"/Items/abc123/LocalTrailers":              "/Items/abc123/LocalTrailers",
		"/emby/items/abc123/localtrailers":         "/Items/abc123/LocalTrailers",
		"/users/u1/items/abc123/localtrailers":     "/Users/u1/Items/abc123/LocalTrailers",
		"/jellyfin/Users/U1/Items/X/localTrailers": "/Users/U1/Items/X/LocalTrailers",
		// Outside the route position the word is an opaque id and keeps its case.
		"/DisplayPreferences/localtrailers": "/DisplayPreferences/localtrailers",
		"/items/localtrailers":              "/Items/localtrailers",
	} {
		if got := canonicalizeCompatPath(in); got != want {
			t.Errorf("canonicalizeCompatPath(%q) = %q, want %q", in, got, want)
		}
	}
}
