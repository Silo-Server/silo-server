package scanner

import "testing"

func TestSupportsAudioFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"book.m4b", true},
		{"chapter1.mp3", true},
		{"audio.M4A", true},
		{"sample.flac", true},
		{"podcast.opus", true},
		{"track.ogg", true},
		{"poster.jpg", false},
		{"movie.mkv", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := SupportsAudioFile(tc.path); got != tc.want {
			t.Errorf("SupportsAudioFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
