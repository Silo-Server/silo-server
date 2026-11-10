package usercollections

import (
	"encoding/json"
	"strings"
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

// TestImportedDefinitionsAreCanonical: an imported collection's
// definitions go through the editors' canonicalizers, and one an editor
// would refuse leaves the collection out with a reason.
func TestImportedDefinitionsAreCanonical(t *testing.T) {
	huge := json.RawMessage(`{"x":"` + strings.Repeat("y", maxDefinitionBytes) + `"}`)
	for name, c := range map[string]struct {
		query, display, sort, source json.RawMessage
		ok                           bool
	}{
		"valid":             {query: json.RawMessage(`{"library_ids":[7],"match":"all","groups":[]}`), sort: json.RawMessage(`{}`), source: json.RawMessage(`{}`), ok: true},
		"manual":            {query: json.RawMessage(`{}`), ok: true},
		"library ids":       {query: json.RawMessage(`{"library_ids":1}`)},
		"oversized rules":   {query: huge},
		"oversized source":  {source: huge},
		"unsupported sort":  {sort: json.RawMessage(`{"field":"nope","order":"asc"}`)},
		"source not object": {source: json.RawMessage(`[1]`)},
	} {
		t.Run(name, func(t *testing.T) {
			def, problem := canonicalDefinition(c.query, c.display, c.sort, c.source)
			if (problem == "") != c.ok {
				t.Fatalf("problem %q, want ok %v", problem, c.ok)
			}
			if c.ok && (def.query == "" || def.sort == "" || def.source == "") {
				t.Fatalf("definition %+v has an empty part", def)
			}
		})
	}
}
