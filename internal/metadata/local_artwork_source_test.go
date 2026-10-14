package metadata

import (
	"testing"
	"time"
)

func TestLocalImageSourceFingerprintRoundTrip(t *testing.T) {
	modTime := time.Date(2025, 3, 4, 5, 6, 7, 891, time.UTC)
	cases := []struct {
		name   string
		source string
		want   string
		ok     bool
	}{
		{"fingerprinted", LocalImageSourceURL("/media/Film/poster.jpg", 48213, modTime), "/media/Film/poster.jpg", true},
		{"question mark in name", LocalImageSourceURL("/media/What If?/poster.jpg", 1, modTime), "/media/What If?/poster.jpg", true},
		{"mtime before 1970", LocalImageSourceURL("/media/Old/poster.jpg", 1, time.Unix(-5, 0)), "/media/Old/poster.jpg", true},
		{"legacy unfingerprinted", "file:///media/Film/poster.jpg", "/media/Film/poster.jpg", true},
		{"upper-case scheme", "FILE:///media/Film/poster.jpg?v=1-2", "/media/Film/poster.jpg", true},
		{"only a trailing fingerprint strips", "file:///media/a?v=1-2/poster.jpg", "/media/a?v=1-2/poster.jpg", true},
		{"remote", "https://example.com/poster.jpg?v=1-2", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := localImageSourceFilePath(tc.source)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("localImageSourceFilePath(%q) = %q, %v; want %q, %v", tc.source, got, ok, tc.want, tc.ok)
			}
		})
	}

	a := LocalImageSourceURL("/media/Film/poster.jpg", 10, modTime)
	if b := LocalImageSourceURL("/media/Film/poster.jpg", 10, modTime.Add(time.Second)); a == b {
		t.Fatal("a newer mtime must change the source")
	}
	if b := LocalImageSourceURL("/media/Film/poster.jpg", 11, modTime); a == b {
		t.Fatal("a different size must change the source")
	}
	if !isLocalImageSourcePath(a) || isCachedImagePath(a) || isRemoteImageSourcePath(a) {
		t.Fatalf("fingerprinted source %q must classify as local only", a)
	}
}
