package scanner

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestClassifyExtraPathMovieLibrary(t *testing.T) {
	movieRoots := walkRootSet([]string{"/movies"})
	cases := []struct {
		path     string
		wantKind models.ExtraKind
		wantDir  string
		wantOK   bool
	}{
		{"/movies/Heat (1995)/Trailers/teaser.mkv", models.ExtraKindTrailer, "/movies/Heat (1995)/Trailers", true},
		{"/movies/Heat (1995)/Behind The Scenes/doc.mkv", models.ExtraKindBehindTheScenes, "/movies/Heat (1995)/Behind The Scenes", true},
		{"/movies/Heat (1995)/Extras/Making Of.mkv", models.ExtraKindOther, "/movies/Heat (1995)/Extras", true},
		// "Other" is part of the Jellyfin/Plex extras convention.
		{"/movies/Heat (1995)/Other/making-of.mkv", models.ExtraKindOther, "/movies/Heat (1995)/Other", true},
		// Nested one level below a supplemental dir still classifies.
		{"/movies/Heat (1995)/Extras/Sub/clip.mkv", models.ExtraKindOther, "/movies/Heat (1995)/Extras", true},
		// Suffix classification with no supplemental dir.
		{"/movies/Heat (1995)/Heat (1995)-trailer.mkv", models.ExtraKindTrailer, "", true},
		// Plain movie files are not extras.
		{"/movies/Heat (1995)/Heat (1995).mkv", "", "", false},
		// Ancestor lookup is depth-bounded: a library living under a dir
		// named "Extras" must not classify everything.
		{"/data/Extras/Movies/Heat (1995)/Heat (1995).mkv", "", "", false},
		// A content-scope folder carrying a convention label ("other",
		// "shorts", "extras", ...) directly under a library root is content
		// organization: titles beneath it stay primary and must not be
		// misclassified as extras (regression for the /movies/other
		// re-probe/defer storm). "others" is additionally absent from the
		// convention vocabulary entirely.
		{"/movies/other/Heat (1995)/Heat (1995).mkv", "", "", false},
		{"/movies/others/Heat (1995)/Heat (1995).mkv", "", "", false},
		{"/movies/shorts/Heat (1995)/Heat (1995).mkv", "", "", false},
		// Loose files directly under a scope-level convention dir are primary
		// too (their "parent" would be the library root, which never binds) —
		// unless the filename itself carries a convention suffix.
		{"/movies/other/stray file.mkv", "", "", false},
	}
	for _, tc := range cases {
		candidate, ok := classifyExtraPath(tc.path, "movies", movieRoots)
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
	tvRoots := walkRootSet([]string{"/tv"})
	// Documented behavior: an episode-tokened file under Extras/ in a series
	// library maps to season 0, so it must NOT classify as an extra.
	if _, ok := classifyExtraPath("/tv/Show/Extras/Show S00E01 Special.mkv", "series", tvRoots); ok {
		t.Fatal("SxxExx file under Extras/ must remain a season-0 episode, not an extra")
	}
	// A non-tokened file under a series-root supplemental dir IS an extra.
	candidate, ok := classifyExtraPath("/tv/Show/Trailers/season-preview.mkv", "series", tvRoots)
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
	primary, extras := partitionExtraPaths(paths, "movies", walkRootSet([]string{"/movies"}))
	if len(primary) != 1 || primary[0] != paths[0] {
		t.Fatalf("primary = %v, want just the main feature", primary)
	}
	if len(extras) != 2 {
		t.Fatalf("extras = %d entries, want 2", len(extras))
	}
}

func TestSupplementalDirAtScopeDepth(t *testing.T) {
	roots := walkRootSet([]string{"/movies"})
	cases := []struct {
		dir  string
		want bool
	}{
		// Inside a title folder: real extras dirs.
		{"/movies/Heat (1995)/Other", false},
		{"/movies/Heat (1995)/Extras", false},
		// At library-root depth: content organization.
		{"/movies/other", true},
		{"/movies/shorts", true},
		// Nested supplemental chain that bottoms out at the root.
		{"/movies/extras/behind the scenes", true},
		// The walk root itself.
		{"/movies", true},
	}
	for _, tc := range cases {
		if got := supplementalDirAtScopeDepth(tc.dir, roots); got != tc.want {
			t.Errorf("supplementalDirAtScopeDepth(%q) = %v, want %v", tc.dir, got, tc.want)
		}
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
