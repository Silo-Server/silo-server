package metadata

import "testing"

func TestProviderIDFromPluginURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"tvdb plugin url", "tvdb://banners/v4/season/468437/posters/665b8e570c65f.jpg", "tvdb"},
		{"http url", "http://example.com/img.jpg", ""},
		{"https url", "https://image.tmdb.org/t/p/original/abc.jpg", ""},
		{"empty", "", ""},
		{"no scheme", "just-a-path.jpg", ""},
		{"scheme only", "://oops", ""},
		{"unknown plugin", "metadb://poster/x.jpg", "metadb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := providerIDFromPluginURL(tc.url)
			if got != tc.want {
				t.Errorf("providerIDFromPluginURL(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}
