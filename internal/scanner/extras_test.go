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
	classifier := newExtrasClassifier("movies", []string{"/movies"}, []string{"/movies"}, paths)
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
		"/tv/Show/Extras/01 - Making Of.mkv",
		"/tv/Show/Season 01/Trailers/E02 - Preview.mkv",
		"/tv/Show/Trailers/season-preview.mkv",
		"/tv/other/Flat Show/pilot.mkv",
	}
	classifier := newExtrasClassifier("series", []string{"/tv"}, []string{"/tv"}, paths)

	// Documented behavior: an episode-tokened file under Extras/ in a series
	// library maps to season 0, so it must NOT classify as an extra.
	if _, ok := classifier.classify("/tv/Show/Extras/Show S00E01 Special.mkv"); ok {
		t.Fatal("SxxExx file under Extras/ must remain a season-0 episode, not an extra")
	}
	for _, filePath := range []string{
		"/tv/Show/Extras/01 - Making Of.mkv",
		"/tv/Show/Season 01/Trailers/E02 - Preview.mkv",
	} {
		if _, ok := classifier.classify(filePath); !ok {
			t.Errorf("numbered supplemental file should remain an extra: %s", filePath)
		}
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

func TestClassifyExtraPathScopedBelowTitle(t *testing.T) {
	// A subtree scan of the extras dir itself (an autoscan event for a late
	// extra) never walks the movie file beside it, so the title folder's
	// ownership comes from the filesystem.
	root := t.TempDir()
	title := filepath.Join(root, "Heat (1995)")
	extras := filepath.Join(title, "extras")
	orphanExtras := filepath.Join(root, "No Feature (2001)", "extras")
	for _, file := range []string{
		filepath.Join(title, "Heat (1995).mkv"),
		filepath.Join(extras, "making-of.mkv"),
		filepath.Join(orphanExtras, "clip.mkv"),
	} {
		writeTestFile(t, file, "media")
	}

	for _, tc := range []struct {
		walkRoot string
		path     string
		wantOK   bool
	}{
		{extras, filepath.Join(extras, "making-of.mkv"), true},
		// The probe still requires the title folder to hold media itself.
		{orphanExtras, filepath.Join(orphanExtras, "clip.mkv"), false},
	} {
		primary, found := partitionExtraPaths([]string{tc.path}, "movies", []string{root}, []string{tc.walkRoot})
		if gotOK := len(found) == 1; gotOK != tc.wantOK {
			t.Errorf("%s: extra=%v (primary=%v), want extra=%v", tc.path, gotOK, primary, tc.wantOK)
		}
	}
}

func TestClassifyExtraPathScopedBelowSymlinkedSeason(t *testing.T) {
	// The library walk follows directory symlinks, so a show whose only
	// season folder is a symlink still owns its Extras/ dir when a subtree
	// scan covers just that dir.
	root := t.TempDir()
	show := filepath.Join(root, "tv", "Show (2020)")
	extras := filepath.Join(show, "Extras")
	realSeason := filepath.Join(root, "elsewhere", "Season 01")
	writeTestFile(t, filepath.Join(realSeason, "Show S01E01.mkv"), "media")
	writeTestFile(t, filepath.Join(extras, "making-of.mkv"), "media")
	if err := os.Symlink(realSeason, filepath.Join(show, "Season 01")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	path := filepath.Join(extras, "making-of.mkv")
	if _, found := partitionExtraPaths([]string{path}, "series", []string{filepath.Join(root, "tv")}, []string{extras}); len(found) != 1 {
		t.Fatalf("extra under a show with a symlinked season was not classified")
	}
}

func TestClassifyExtraPathLibraryRootNamedLikeExtrasDir(t *testing.T) {
	// A library rooted at a folder named like an extras dir must never take
	// ownership from the folder above it, even when that folder holds media.
	parent := t.TempDir()
	root := filepath.Join(parent, "shorts")
	movie := filepath.Join(root, "Movie (2020)", "Movie (2020).mkv")
	loose := filepath.Join(root, "Loose (2021).mkv")
	writeTestFile(t, movie, "media")
	writeTestFile(t, loose, "media")
	writeTestFile(t, filepath.Join(parent, "stray.mkv"), "media")

	walked := []string{movie, loose}
	if _, found := partitionExtraPaths(walked, "movies", []string{root}, []string{root}); len(found) != 0 {
		t.Errorf("full scan classified %v as extras", found)
	}
	scoped := filepath.Dir(movie)
	if _, found := partitionExtraPaths([]string{movie}, "movies", []string{root}, []string{scoped}); len(found) != 0 {
		t.Errorf("subtree scan classified %v as extras", found)
	}
	if _, ok := newWatchExtrasClassifier("movies", []string{root}).classify(loose); ok {
		t.Errorf("watch scan classified %s as an extra", loose)
	}
}

func TestClassifyExtraPathScopedProbeSkipsUnwalkedFiles(t *testing.T) {
	// A subtree scan below a folder probes that folder's ownership from disk.
	// Files the library walk skips (samples, ignored files) must not make it a
	// title folder, or a matched title under a content-scope "other/" folder
	// would turn into an extra of its sibling.
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{"sample", map[string]string{"sample.mkv": "sample"}},
		{"siloignore", map[string]string{"bonus.mkv": "video", ".siloignore": "bonus.mkv\n"}},
		{"nomedia", map[string]string{"bonus.mkv": "video", ".nomedia": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			collection := filepath.Join(root, "Collection")
			alien := filepath.Join(collection, "other", "Alien (1979)", "Alien (1979).mkv")
			writeTestFile(t, alien, "movie")
			for name, content := range tc.files {
				writeTestFile(t, filepath.Join(collection, name), content)
			}
			for _, walkRoot := range []string{root, filepath.Dir(alien)} {
				if _, found := partitionExtraPaths([]string{alien}, "movies", []string{root}, []string{walkRoot}); len(found) != 0 {
					t.Errorf("walk root %s: classified %v as extras", walkRoot, found)
				}
			}
		})
	}
}

func TestPartitionExtraPaths(t *testing.T) {
	paths := []string{
		"/movies/Heat (1995)/Heat (1995).mkv",
		"/movies/Heat (1995)/Trailers/tease.mkv",
		"/movies/Heat (1995)/Heat (1995)-featurette.mkv",
	}
	primary, extras := partitionExtraPaths(paths, "movies", []string{"/movies"}, []string{"/movies"})
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
	for _, dir := range []string{"/m/Movie/Trailers", "/m/Movie/Extras", "/m/Movie/Behind The Scenes", "/m/Movie/Featurettes", "/m/Movie/Season 1"} {
		if shouldSkipMovieSupplementalDir(dir) {
			t.Errorf("expected %q to be walked for extras classification", dir)
		}
	}
}

// TestSeriesExtrasFoldersBelongToTheirShow covers extras folders in a series
// library: their files bind to the show that owns them, never found a series
// named after the folder, and the same folder names used as category or show
// folders keep their own series. A full scan, a subtree scan of the file's own
// folder, and a single-file event must agree, because a file that flips
// between extra and primary from one scan to the next churns its show.
func TestSeriesExtrasFoldersBelongToTheirShow(t *testing.T) {
	type want = extrasLayoutWant
	checkExtrasLayout(t, "series", map[string]want{
		// A show with episodes in season folders owns every extras folder in
		// it, whatever the files are named.
		"Glimmer Harbor/Season 01/Glimmer Harbor - S01E01.mkv":            {root: "Glimmer Harbor"},
		"Glimmer Harbor/other/Glimmer Harbor - S00E01 - Making of.mkv":    {extra: true, kind: models.ExtraKindOther},
		"Glimmer Harbor/other/Extras/Glimmer Harbor.S00E02.mkv":           {extra: true, kind: models.ExtraKindOther},
		"Glimmer Harbor/Featurettes/Glimmer Harbor S00E04 Featurette.mkv": {extra: true, kind: models.ExtraKindFeaturette},
		"Glimmer Harbor/Extras/Behind The Scenes/clip.mkv":                {extra: true, kind: models.ExtraKindBehindTheScenes},
		"Glimmer Harbor/Trailers/1080p/Glimmer Harbor S00E05.mkv":         {extra: true, kind: models.ExtraKindTrailer},
		"Glimmer Harbor/Season 01/Trailers/Preview.mkv":                   {extra: true, kind: models.ExtraKindTrailer},
		// Directly under the show's own Extras/, an episode code keeps its
		// documented meaning. Under a season's Extras/ it doesn't, so a deleted
		// scene never becomes a second version of the real episode.
		"Glimmer Harbor/Extras/Glimmer Harbor S00E03.mkv":                   {root: "Glimmer Harbor"},
		"Glimmer Harbor/Season 01/Glimmer Harbor - S01E05.mkv":              {root: "Glimmer Harbor"},
		"Glimmer Harbor/Season 01/Extras/Glimmer Harbor S01E05 Deleted.mkv": {extra: true, kind: models.ExtraKindOther},
		// Episodes deeper than a season folder still make the show, and the
		// season folder owns its extras too.
		"Ironvale/Season 01/Disc 1/Ironvale - S01E01.mkv": {root: "Ironvale"},
		"Ironvale/Season 01/Trailers/Preview.mkv":         {extra: true, kind: models.ExtraKindTrailer},
		"Ironvale/other/Behind the camera.mkv":            {extra: true, kind: models.ExtraKindOther},
		// A show with no episodes yet has nothing to own its trailers/. It
		// stays a series of its own until the first episode arrives, when a
		// scan turns the file into an extra.
		"Emberfall (2021)/trailers/Emberfall trailer.mkv": {root: "Emberfall (2021)/trailers"},
		// A bare Season folder (#1660) and its Specials stay with the show.
		"Quillmere/Season/Quillmere S01E01.mkv":          {root: "Quillmere"},
		"Quillmere/Season/Specials/Quillmere S00E01.mkv": {root: "Quillmere"},
		// Category folders own no extras: their extras-named folders hold
		// shows or loose episodes, which keep their own series.
		"Kids/Bluey/Season 01/Bluey S01E01.mkv":              {root: "Kids/Bluey"},
		"Kids/Peppa/Peppa S01E01.mkv":                        {root: "Kids/Peppa"},
		"Kids/Shorts/Season 01/S01E01.mkv":                   {root: "Kids/Shorts"},
		"Kids/Shorts/Specials/S00E01.mkv":                    {root: "Kids/Shorts"},
		"Kids/Clips/Peppa Pig S01E02.mkv":                    {root: "Kids/Clips"},
		"Kids/Other/Other.2024.S01E02.mkv":                   {root: "Kids/Other"},
		"Documentaries/Interviews/Interviews S01E01.mkv":     {root: "Documentaries/Interviews"},
		"Documentaries/Interviews/S01E02.mkv":                {root: "Documentaries/Interviews"},
		"Documentaries/Interviews/The.Interviews.S01E03.mkv": {root: "Documentaries/Interviews"},
		"other/Flat Show/pilot.mkv":                          {root: "other/Flat Show"},
		// A loose episode makes a category look like a show, but a folder
		// named like an extras folder with season folders inside is a show of
		// its own.
		"Toons/Peppa S01E01.mkv":                   {root: "Toons"},
		"Toons/Shorts/Season 01/Shorts S01E01.mkv": {root: "Toons/Shorts"},
		"Toons/Shorts/Specials/Shorts S00E01.mkv":  {root: "Toons/Shorts"},
		// Season-like folders inside a show's extras still hold extras.
		"Glimmer Harbor/Trailers/S01/Preview.mkv":                       {extra: true, kind: models.ExtraKindTrailer},
		"Glimmer Harbor/other/Specials/Making of.mkv":                   {extra: true, kind: models.ExtraKindOther},
		"Glimmer Harbor/Featurettes/Season 1 Featurettes/Making of.mkv": {extra: true, kind: models.ExtraKindFeaturette},
		// A franchise folder holding shows from different years is not an
		// extras owner, even though the shows share its title. (Root inference
		// collapses the same-title folders into Doctor Who, as on main.)
		"Doctor Who/Doctor Who (1963)/Season 01/Doctor Who S01E01.mkv": {root: "Doctor Who"},
		"Doctor Who/Doctor Who (2005)/Season 01/Doctor Who S01E01.mkv": {root: "Doctor Who"},
		"Doctor Who/Interviews/Interview S01E01.mkv":                   {root: "Doctor Who/Interviews"},
		// A same-name wrapper folder owns its extras.
		"Lumen/Lumen/Season 01/Lumen S01E01.mkv": {root: "Lumen"},
		"Lumen/Trailers/teaser.mkv":              {extra: true, kind: models.ExtraKindTrailer},
		// A category's own Extras/ folder doesn't make it a show, and neither
		// does a year in its name.
		"Cartoons/Extras/Peppa S01E01.mkv":                          {root: "Cartoons"},
		"Cartoons/Shorts/Season 01/S01E01.mkv":                      {root: "Cartoons/Shorts"},
		"Cartoons/Clips/Clip S01E02.mkv":                            {root: "Cartoons/Clips"},
		"Christmas (2023)/Shorts/Snowfall/E01.mkv":                  {root: "Christmas (2023)/Shorts/Snowfall"},
		"Christmas (2023)/Elf Tales/Season 01/Elf Tales S01E01.mkv": {root: "Christmas (2023)/Elf Tales"},
		// Shows titled like extras folders or with a number.
		"Comedy/Extras/Season 01/Extras S01E01.mkv": {root: "Comedy/Extras"},
		"Comedy/Extras/Specials/Extras S00E01.mkv":  {root: "Comedy/Extras"},
		"Extras/Season 01/Extras S01E01.mkv":        {root: "Extras"},
		"Extras/Specials/Extras S00E01.mkv":         {root: "Extras"},
		"Drama/1883/Season 01/1883 S01E01.mkv":      {root: "Drama/1883"},
		"Drama/1883/Specials/1883 S00E01.mkv":       {root: "Drama/1883"},
	})
}

// TestMixedLibraryVersionFolderOwnsExtras covers a movie whose file sits in a
// version folder in a mixed library: the title folder still owns its
// trailers, while a category's extras-named folder stays content.
func TestMixedLibraryVersionFolderOwnsExtras(t *testing.T) {
	type want = extrasLayoutWant
	checkExtrasLayout(t, "mixed", map[string]want{
		"Alien (1979)/1080p/Alien (1979).mkv":   {root: "Alien (1979)/1080p/Alien (1979)"},
		"Alien (1979)/Trailers/trailer.mkv":     {extra: true, kind: models.ExtraKindTrailer},
		"Kids/Bluey/Season 01/Bluey S01E01.mkv": {root: "Kids/Bluey"},
		"Kids/Shorts/Season 01/S01E01.mkv":      {root: "Kids/Shorts"},
	})
}

// extrasLayoutWant is the classification a layout test expects for a file.
type extrasLayoutWant struct {
	extra bool
	kind  models.ExtraKind
	root  string // content root of a primary file, relative to the library
}

// checkExtrasLayout writes layout below a fresh library folder and checks
// each file's classification in a full scan, a subtree scan of its own
// folder, and a single-file event, plus the root of every primary file.
func checkExtrasLayout(t *testing.T, libraryType string, layout map[string]extrasLayoutWant) {
	t.Helper()
	library := filepath.Join(t.TempDir(), "library")
	for rel := range layout {
		writeTestFile(t, filepath.Join(library, rel), "media")
	}
	roots := []string{library}
	walked := collectTestFilePaths(t, library, libraryType)
	if len(walked) != len(layout) {
		t.Fatalf("walked %d files, want %d", len(walked), len(layout))
	}

	primary, extras := partitionExtraPaths(walked, libraryType, roots, roots)
	full := make(map[string]extraCandidate, len(extras))
	for _, candidate := range extras {
		full[candidate.Path] = candidate
	}
	assignments := inferRootAssignments(primary, libraryType, 1, nil, library).Assignments
	watch := newWatchExtrasClassifier(libraryType, roots)
	for rel, w := range layout {
		path := filepath.Join(library, rel)
		candidate, isExtra := full[path]
		if isExtra != w.extra || (isExtra && candidate.Kind != w.kind) {
			t.Errorf("full scan: %s extra=%v kind=%q, want extra=%v kind=%q", rel, isExtra, candidate.Kind, w.extra, w.kind)
		}
		if !w.extra {
			if got := assignments[path].RootPath; got != filepath.Join(library, w.root) {
				t.Errorf("full scan: %s root %q, want %q", rel, got, filepath.Join(library, w.root))
			}
		}
		_, subtree := partitionExtraPaths([]string{path}, libraryType, roots, []string{filepath.Dir(path)})
		if got := len(subtree) == 1; got != w.extra {
			t.Errorf("subtree scan of its folder: %s extra=%v, want %v", rel, got, w.extra)
		}
		if _, got := watch.classify(path); got != w.extra {
			t.Errorf("single-file scan: %s extra=%v, want %v", rel, got, w.extra)
		}
	}
}
