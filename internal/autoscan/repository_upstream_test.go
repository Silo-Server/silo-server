package autoscan

import "testing"

// Saving the same server's URL spelled differently must not reset the bound
// sources' markers: the next poll would start from now and skip whatever the
// server imported since the last one.
func TestConnectionUpstreamIgnoresTheSameURLSpelledDifferently(t *testing.T) {
	stored := "http://Sonarr.example:8989"
	for _, tc := range []struct {
		next    string
		differs bool
	}{
		{"http://sonarr.example:8989", false},
		{"http://sonarr.example:8989/", false},
		{" HTTP://SONARR.EXAMPLE:8989/ ", false},
		{"http://sonarr.example:8990", true},
		{"https://sonarr.example:8989", true},
		{"http://sonarr.example:8989/sonarr", true},
		{"http://sonarr.example:8989/a%2Fb", true},
		{"http://radarr.example:7878", true},
	} {
		if got := (connectionUpstream{baseURL: &stored}).differsFrom(Connection{BaseURL: tc.next}); got != tc.differs {
			t.Errorf("%q -> %q: differs = %v, want %v", stored, tc.next, got, tc.differs)
		}
	}
}

// An escaped slash keeps two paths apart, and the trailing-slash rule holds
// for escaped paths too.
func TestComparableBaseURLKeepsEscapedPaths(t *testing.T) {
	if comparableBaseURL("http://host.example/a%2Fb") == comparableBaseURL("http://host.example/a/b") {
		t.Error("an escaped slash compared equal to a path separator")
	}
	if comparableBaseURL("http://host.example/a%2Fb/") != comparableBaseURL("http://host.example/a%2Fb") {
		t.Error("a trailing slash after an escaped path changed the comparison")
	}
	// An escaped slash at the end of the path is part of the path, not a
	// trailing slash to drop.
	if comparableBaseURL("http://host.example/a%2F") == comparableBaseURL("http://host.example/a") {
		t.Error("a trailing escaped slash compared equal to no slash")
	}
	if comparableBaseURL("http://host.example/a%2Fb%2F") == comparableBaseURL("http://host.example/a/b") {
		t.Error("an escaped path ending in %2F compared equal to its unescaped form")
	}
}
