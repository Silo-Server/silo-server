package catalog

import "testing"

func TestParseTraktListURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		user    string
		list    string
		wantErr bool
	}{
		{"full url", "https://trakt.tv/users/jjjonesjr33/lists/saw-cinematic-universe-in-timeline-order", "jjjonesjr33", "saw-cinematic-universe-in-timeline-order", false},
		{"www url", "https://www.trakt.tv/users/someone/lists/best-of", "someone", "best-of", false},
		{"mixed-case host", "https://Trakt.TV/users/someone/lists/best-of", "someone", "best-of", false},
		{"url with query", "https://trakt.tv/users/someone/lists/best-of?sort=rank,asc", "someone", "best-of", false},
		{"no scheme", "trakt.tv/users/someone/lists/best-of", "someone", "best-of", false},
		{"shorthand", "someone/best-of", "someone", "best-of", false},
		{"trailing slash", "https://trakt.tv/users/someone/lists/best-of/", "someone", "best-of", false},
		{"empty", "", "", "", true},
		{"wrong host", "https://example.com/users/someone/lists/best-of", "", "", true},
		{"not a list url", "https://trakt.tv/movies/trending", "", "", true},
		{"missing slug", "https://trakt.tv/users/someone/lists/", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user, list, err := ParseTraktListURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseTraktListURL(%q) = %q/%q, want error", tc.in, user, list)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTraktListURL(%q): %v", tc.in, err)
			}
			if user != tc.user || list != tc.list {
				t.Fatalf("ParseTraktListURL(%q) = %q/%q, want %q/%q", tc.in, user, list, tc.user, tc.list)
			}
		})
	}
}
