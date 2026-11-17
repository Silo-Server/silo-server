package scanner

import "testing"

func TestSupportsAudioFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"book.m4b", true},
		{"audio.M4A", true},
		{"poster.jpg", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := SupportsAudioFile(tc.path); got != tc.want {
			t.Errorf("SupportsAudioFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
