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

func TestCanonicalizeCompatPathAdditionalParts(t *testing.T) {
	for in, want := range map[string]string{
		"/videos/abc123/additionalparts":      "/Videos/abc123/AdditionalParts",
		"/Videos/abc123/AdditionalParts":      "/Videos/abc123/AdditionalParts",
		"/emby/videos/abc123/additionalParts": "/Videos/abc123/AdditionalParts",
		"/DisplayPreferences/additionalparts": "/DisplayPreferences/additionalparts",
		"/items/abc123/additionalparts":       "/Items/abc123/additionalparts",
		"/videos/additionalparts":             "/Videos/additionalparts",
	} {
		if got := canonicalizeCompatPath(in); got != want {
			t.Errorf("canonicalizeCompatPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalizeCompatPathMediaFolders(t *testing.T) {
	for in, want := range map[string]string{
		"/library/mediafolders":            "/Library/MediaFolders",
		"/emby/Library/mediaFolders":       "/Library/MediaFolders",
		"/DisplayPreferences/mediafolders": "/DisplayPreferences/mediafolders",
	} {
		if got := canonicalizeCompatPath(in); got != want {
			t.Errorf("canonicalizeCompatPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCanonicalizeCompatPathDisplayPreferencesID covers #2990: the
// {displayPreferencesId} segment is a case-sensitive storage key, so an id that
// reads like a route word must keep its case.
func TestCanonicalizeCompatPathDisplayPreferencesID(t *testing.T) {
	for in, want := range map[string]string{
		"/displaypreferences/usersettings":  "/DisplayPreferences/usersettings",
		"/DisplayPreferences/items":         "/DisplayPreferences/items",
		"/DisplayPreferences/HLS":           "/DisplayPreferences/HLS",
		"/DisplayPreferences/Stream.Custom": "/DisplayPreferences/Stream.Custom",
		"/emby/displaypreferences/users":    "/DisplayPreferences/users",
		"/DisplayPreferences/items/":        "/DisplayPreferences/items/",
	} {
		if got := canonicalizeCompatPath(in); got != want {
			t.Errorf("canonicalizeCompatPath(%q) = %q, want %q", in, got, want)
		}
	}
}
