package usercollections

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// An imported poster keeps an image URL or a bundled path, and drops any
// other stored key, which a listing would sign.
func TestCopyPosterDropsForeignKeys(t *testing.T) {
	for poster, want := range map[string]string{
		"https://image.example/p.jpg":             "https://image.example/p.jpg",
		"/images/collection-templates/marvel.jpg": "/images/collection-templates/marvel.jpg",
		"trickplay/movie-1/sheet-0.jpg":           "",
		"subtitles/movie-1/en.vtt":                "",
		posterPrefix + "c1/poster.jpg":            "", // no file to copy here
	} {
		missing := 0
		got, err := copyPoster(t.Context(), userdata.Target{}, personalCollection{ID: "c1", Poster: poster}, "new", &missing)
		if err != nil || got != want {
			t.Errorf("copyPoster(%q) = %q, %v; want %q", poster, got, err, want)
		}
		if dropped := want == ""; dropped != (missing == 1) {
			t.Errorf("copyPoster(%q) counted %d missing", poster, missing)
		}
	}
}
