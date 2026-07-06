package scanner

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestClassifyExtraPathMovieLibrary(t *testing.T) {
	cases := []struct {
		path     string
		wantKind models.ExtraKind
		wantDir  string
		wantOK   bool
	}{
		{"/movies/Heat (1995)/Trailers/teaser.mkv", models.ExtraKindTrailer, "/movies/Heat (1995)/Trailers", true},
		{"/movies/Heat (1995)/Behind The Scenes/doc.mkv", models.ExtraKindBehindTheScenes, "/movies/Heat (1995)/Behind The Scenes", true},
		{"/movies/Heat (1995)/Extras/Making Of.mkv", models.ExtraKindOther, "/movies/Heat (1995)/Extras", true},
		// Nested one level below a supplemental dir still classifies.
		{"/movies/Heat (1995)/Extras/Sub/clip.mkv", models.ExtraKindOther, "/movies/Heat (1995)/Extras", true},
		// Suffix classification with no supplemental dir.
		{"/movies/Heat (1995)/Heat (1995)-trailer.mkv", models.ExtraKindTrailer, "", true},
		// Plain movie files are not extras.
		{"/movies/Heat (1995)/Heat (1995).mkv", "", "", false},
		// Ancestor lookup is depth-bounded: a library living under a dir
		// named "Extras" must not classify everything.
		{"/data/Extras/Movies/Heat (1995)/Heat (1995).mkv", "", "", false},
	}
	for _, tc := range cases {
		candidate, ok := classifyExtraPath(tc.path, "movies")
		if ok != tc.wantOK {
			t.Errorf("classifyExtraPath(%q) ok = %v, want %v", tc.path, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if candidate.Kind != tc.wantKind || candidate.SupplementalDir != tc.wantDir {
			t.Errorf("classifyExtraPath(%q) = (%q, %q), want (%q, %q)",
				tc.path, candidate.Kind, candidate.SupplementalDir, tc.wantKind, tc.wantDir)
		}
	}
}

func TestClassifyExtraPathSeriesKeepsSeasonZeroBehavior(t *testing.T) {
	// Documented behavior: an episode-tokened file under Extras/ in a series
	// library maps to season 0, so it must NOT classify as an extra.
	if _, ok := classifyExtraPath("/tv/Show/Extras/Show S00E01 Special.mkv", "series"); ok {
		t.Fatal("SxxExx file under Extras/ must remain a season-0 episode, not an extra")
	}
	// A non-tokened file under a series-root supplemental dir IS an extra.
	candidate, ok := classifyExtraPath("/tv/Show/Trailers/season-preview.mkv", "series")
	if !ok || candidate.Kind != models.ExtraKindTrailer {
		t.Fatalf("series-root trailer dir should classify, got ok=%v kind=%q", ok, candidate.Kind)
	}
}

func TestPartitionExtraPaths(t *testing.T) {
	paths := []string{
		"/movies/Heat (1995)/Heat (1995).mkv",
		"/movies/Heat (1995)/Trailers/tease.mkv",
		"/movies/Heat (1995)/Heat (1995)-featurette.mkv",
	}
	primary, extras := partitionExtraPaths(paths, "movies")
	if len(primary) != 1 || primary[0] != paths[0] {
		t.Fatalf("primary = %v, want just the main feature", primary)
	}
	if len(extras) != 2 {
		t.Fatalf("extras = %d entries, want 2", len(extras))
	}
}

func TestMovieSupplementalDirsNoLongerSkipExtras(t *testing.T) {
	// The walk must still hard-skip noise dirs...
	for _, dir := range []string{"/m/Movie/Sample", "/m/Movie/Subs"} {
		if !shouldSkipMovieSupplementalDir(dir) {
			t.Errorf("expected %q to remain skipped", dir)
		}
	}
	// ...but extras-shaped dirs are walked now (classified downstream).
	for _, dir := range []string{"/m/Movie/Trailers", "/m/Movie/Extras", "/m/Movie/Behind The Scenes"} {
		if shouldSkipMovieSupplementalDir(dir) {
			t.Errorf("expected %q to be walked for extras classification", dir)
		}
	}
}
