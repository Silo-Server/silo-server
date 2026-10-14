package naming

import "testing"

func TestParseExtraSuffix(t *testing.T) {
	cases := []struct {
		name     string
		wantKind string
		wantOK   bool
	}{
		{"Movie (2020)-trailer.mkv", "trailer", true},
		{"Movie (2020)-Trailer.mkv", "trailer", true},
		{"Movie.2020.behindthescenes.mkv", "behind_the_scenes", true},
		{"Movie-deleted.mkv", "deleted_scene", true},
		{"Movie-featurette.mp4", "featurette", true},
		{"Movie-interview.mkv", "other", true},
		{"Movie-short.mkv", "other", true},
		// The token must be the final suffix component.
		{"Trailer Park Boys S01E01.mkv", "", false},
		{"The Deleted (2016).mkv", "", false},
		{"Movie (2020).mkv", "", false},
		// A bare token with no preceding title is not a suffix match.
		{"trailer.mkv", "", false},
		{"-trailer.mkv", "", false},
	}
	for _, tc := range cases {
		kind, ok := ParseExtraSuffix(tc.name)
		if ok != tc.wantOK || kind != tc.wantKind {
			t.Errorf("ParseExtraSuffix(%q) = (%q, %v), want (%q, %v)",
				tc.name, kind, ok, tc.wantKind, tc.wantOK)
		}
	}
}

func TestExtraTitleFromFile(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/lib/Movie (2020)/Movie (2020)-trailer.mkv", "Movie (2020)"},
		{"/lib/Movie (2020)/Extras/Making Of.mkv", "Making Of"},
		{"/lib/Movie/Extras/behind.the.scenes_reel.mkv", "behind the scenes reel"},
	}
	for _, tc := range cases {
		if got := ExtraTitleFromFile(tc.path); got != tc.want {
			t.Errorf("ExtraTitleFromFile(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
