package scanner

import (
	"os"
	"path/filepath"
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
		// "Other" is part of the Jellyfin/Plex extras convention.
		{"/movies/Heat (1995)/Other/making-of.mkv", models.ExtraKindOther, "/movies/Heat (1995)/Other", true},
		// Nested one level below a supplemental dir still classifies.
		{"/movies/Heat (1995)/Extras/Sub/clip.mkv", models.ExtraKindOther, "/movies/Heat (1995)/Extras", true},
		// Title folders own their extras at any depth below the root.
		{"/movies/Collection/Ronin (1998)/Other/interview.mkv", models.ExtraKindOther, "/movies/Collection/Ronin (1998)/Other", true},
		// Suffix classification with no supplemental dir.
		{"/movies/Heat (1995)/Heat (1995)-trailer.mkv", models.ExtraKindTrailer, "", true},
		// Plain movie files are not extras.
		{"/movies/Heat (1995)/Heat (1995).mkv", "", "", false},
		{"/movies/Collection/Ronin (1998)/Ronin (1998).mkv", "", "", false},
		// Ancestor lookup is depth-bounded: a library living under a dir
		// named "Extras" must not classify everything.
		{"/data/Extras/Movies/Heat (1995)/Heat (1995).mkv", "", "", false},
		// A content-scope folder carrying a convention label ("other",
		// "shorts", "extras", ...) owns no media of its own, so titles
		// beneath it stay primary and must not be misclassified as extras
		// (regression for the /movies/other re-probe/defer storm) — at the
		// library root or nested any depth below it. "others" is additionally
		// absent from the convention vocabulary entirely.
		{"/movies/other/Heat (1995)/Heat (1995).mkv", "", "", false},
		{"/movies/others/Heat (1995)/Heat (1995).mkv", "", "", false},
		{"/movies/shorts/Heat (1995)/Heat (1995).mkv", "", "", false},
		{"/movies/4K/other/Alien (1979)/Alien (1979).mkv", "", "", false},
		// Chained convention names at library scope hold no title either:
		// loose clips there stay primary instead of deferring forever.
		{"/movies/extras/behind the scenes/clip.mkv", "", "", false},
		// Loose files directly under a scope-level convention dir are primary
		// too — unless the filename itself carries a convention suffix.
		{"/movies/other/stray file.mkv", "", "", false},
	}
	paths := make([]string, 0, len(cases))
	for _, tc := range cases {
		paths = append(paths, tc.path)
	}
	classifier := newExtrasClassifier("movies", []string{"/movies"}, paths)
	for _, tc := range cases {
		candidate, ok := classifier.classify(tc.path)
		if ok != tc.wantOK {
			t.Errorf("classify(%q) ok = %v, want %v", tc.path, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if candidate.Kind != tc.wantKind || candidate.SupplementalDir != tc.wantDir {
			t.Errorf("classify(%q) = (%q, %q), want (%q, %q)",
				tc.path, candidate.Kind, candidate.SupplementalDir, tc.wantKind, tc.wantDir)
		}
	}
}

func TestClassifyExtraPathSeriesLibrary(t *testing.T) {
	paths := []string{
		"/tv/Show/Season 01/Show S01E01.mkv",
		"/tv/Show/Extras/Show S00E01 Special.mkv",
		"/tv/Show/Trailers/season-preview.mkv",
		"/tv/other/Flat Show/pilot.mkv",
	}
	classifier := newExtrasClassifier("series", []string{"/tv"}, paths)

	// Documented behavior: an episode-tokened file under Extras/ in a series
	// library maps to season 0, so it must NOT classify as an extra.
	if _, ok := classifier.classify("/tv/Show/Extras/Show S00E01 Special.mkv"); ok {
		t.Fatal("SxxExx file under Extras/ must remain a season-0 episode, not an extra")
	}
	// A non-tokened file under a show-level supplemental dir IS an extra;
	// the show folder owns it through its season-level episodes.
	candidate, ok := classifier.classify("/tv/Show/Trailers/season-preview.mkv")
	if !ok || candidate.Kind != models.ExtraKindTrailer {
		t.Fatalf("show trailer dir should classify, got ok=%v kind=%q", ok, candidate.Kind)
	}
	// A scope folder named "other" holding show folders stays primary.
	if _, ok := classifier.classify("/tv/other/Flat Show/pilot.mkv"); ok {
		t.Fatal("show under a scope-level other/ must remain primary")
	}
}

func TestClassifyExtraPathWatchMode(t *testing.T) {
	// Watch-event scans have no walked path list; title ownership is probed
	// from the filesystem.
	root := t.TempDir()
	title := filepath.Join(root, "Heat (1995)")
	other := filepath.Join(title, "Other")
	scopeOther := filepath.Join(root, "other", "Alien (1979)")
	for _, dir := range []string{other, scopeOther} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{
		filepath.Join(title, "Heat (1995).mkv"),
		filepath.Join(other, "making-of.mkv"),
		filepath.Join(scopeOther, "Alien (1979).mkv"),
	} {
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	classifier := newWatchExtrasClassifier("movies", []string{root})
	candidate, ok := classifier.classify(filepath.Join(other, "making-of.mkv"))
	if !ok || candidate.Kind != models.ExtraKindOther {
		t.Fatalf("convention dir beside the movie file should classify, got ok=%v kind=%q", ok, candidate.Kind)
	}
	if _, ok := classifier.classify(filepath.Join(scopeOther, "Alien (1979).mkv")); ok {
		t.Fatal("title under a scope-level other/ must remain primary in watch mode")
	}
}

func TestPartitionExtraPaths(t *testing.T) {
	paths := []string{
		"/movies/Heat (1995)/Heat (1995).mkv",
		"/movies/Heat (1995)/Trailers/tease.mkv",
		"/movies/Heat (1995)/Heat (1995)-featurette.mkv",
	}
	primary, extras := partitionExtraPaths(paths, "movies", []string{"/movies"})
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
