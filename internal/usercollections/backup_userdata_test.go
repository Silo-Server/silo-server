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

// An imported sync schedule is one the collection editor accepts, or none.
func TestImportedScheduleKeepsOnlyAllowedCadences(t *testing.T) {
	text := func(s string) *string { return &s }
	cases := []struct {
		kind     string
		schedule *string
		want     string
		dropped  bool
	}{
		{"tmdb", text("30 4 * * 0"), AllowedSyncSchedules["weekly"], false},
		{"mdblist", text(" 30  4 1 * * "), AllowedSyncSchedules["monthly"], false},
		{"tmdb", text("* * * * *"), "", true},
		{"smart", text(AllowedSyncSchedules["daily"]), "", true},
		{"tmdb", nil, "", false},
	}
	for _, tc := range cases {
		dropped := 0
		got := importedSchedule(personalCollection{Type: tc.kind, SyncSchedule: tc.schedule}, &dropped)
		if (got == nil && tc.want != "") || (got != nil && *got != tc.want) || (dropped == 1) != tc.dropped {
			t.Errorf("importedSchedule(%s, %v) = %v, dropped %d", tc.kind, tc.schedule, got, dropped)
		}
	}
}
