package naming

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSeriesLibraryRootDoesNotReplaceShowFolderIdentity(t *testing.T) {
	filePath := "/tv/The X-Files/Pilot - S01E01.mkv"
	for _, roots := range [][]string{nil, {"/tv"}, {"/another/library"}} {
		ctx := ResolvePathContext(filePath, "series", roots...)
		if ctx.Title != "The X-Files" || ctx.RootPath != "/tv/The X-Files" {
			t.Errorf("roots %v: episode title displaced show folder: %+v", roots, ctx)
		}
		_, assignments := InferRootAssignments([]string{filePath}, "series", 1, nil, roots...)
		group := InferGroupIdentity(filePath, "series", assignments[filePath])
		if group.BaseTitle != "The X-Files" || group.State != "resolved" {
			t.Errorf("roots %v: episode title displaced scan identity: %+v", roots, group)
		}
	}
}

func TestSeriesFilenameIdentityUsesConfiguredRootBoundary(t *testing.T) {
	for _, libraryRoot := range []string{"/media/My custom collection", "/media/Season 1", "/media/My Library (2024)", "/media/Archive {tvdb-12345}", "/media/2024"} {
		t.Run(libraryRoot, func(t *testing.T) {
			paths := []string{
				filepath.Join(libraryRoot, "The.X.Files.S01E01.mkv"),
				filepath.Join(libraryRoot, "Seinfeld.S01E01.mkv"),
			}
			roots := []string{"/media", libraryRoot, "/elsewhere"}
			snapshots, assignments := InferRootAssignments(paths, "series", 1, nil, roots...)
			for _, snapshot := range snapshots {
				if snapshot.State != "resolved" {
					t.Errorf("library label made file root ambiguous: %+v", snapshot)
				}
			}
			for i, title := range []string{"The X Files", "Seinfeld"} {
				ctx := ResolvePathContext(paths[i], "series", roots...)
				if ctx.Title != title || ctx.Year != 0 || ctx.RootPath != paths[i] || ctx.LibraryRootPath != libraryRoot || ctx.HasSeasonStructure {
					t.Fatalf("library path became media identity: %+v", ctx)
				}
				assignment := assignments[paths[i]]
				if assignment.LibraryRootPath != libraryRoot || assignment.RootPath != paths[i] {
					t.Errorf("configured root context lost: %+v", assignment)
				}
				group := InferGroupIdentity(paths[i], "series", assignment)
				if group.BaseTitle != title || group.BaseYear != 0 || group.TvdbID != "" || group.State != "resolved" {
					t.Errorf("library path became scan identity: %+v", group)
				}
			}
		})
	}
}

func TestSeriesUnnamedEpisodeAtLibraryRootStaysUnidentified(t *testing.T) {
	filePath := "/media/My Library (2024)/E02.mkv"
	ctx := ResolvePathContext(filePath, "series", "/media/My Library (2024)")
	if ctx.Title != "" || ctx.Year != 0 || ctx.SeasonKnown || ctx.EpisodeNum != 2 {
		t.Fatalf("library name became unnamed episode identity: %+v", ctx)
	}
	_, assignments := InferRootAssignments([]string{filePath}, "series", 1, nil, ctx.LibraryRootPath)
	group := InferGroupIdentity(filePath, "series", assignments[filePath])
	if group.BaseTitle != "" || group.State != "ambiguous" {
		t.Fatalf("unnamed episode acquired scan identity: %+v", group)
	}
}

func TestSeriesLibraryBoundaryDoesNotBorrowAncestorSeason(t *testing.T) {
	filePath := "/archive/Season 8/My Collection/Show - 12.mkv"
	ctx := ResolvePathContext(filePath, "series", "/archive/Season 8/My Collection")
	if ctx.Title != "Show" || ctx.HasSeasonStructure || ctx.SeasonKnown || ctx.RootPath != filePath {
		t.Fatalf("ancestor season escaped library boundary: %+v", ctx)
	}
}

func TestSeriesBareFilenameIdentity(t *testing.T) {
	for _, filename := range []string{"The.Show.S01E02.mkv", "/The.Show.S01E02.mkv"} {
		hints := ParseFilename(filename, "series")
		if hints.Title != "The Show" || hints.SeasonNum != 1 || hints.EpisodeNum != 2 {
			t.Errorf("bare filename lost identity: %+v", hints)
		}
	}
}

func TestConfiguredMovieAndMixedRootSuppliesNoIdentity(t *testing.T) {
	for _, libraryType := range []string{"movies", "mixed"} {
		for _, libraryRoot := range []string{"/media/My Library (2024)", "/media/Archive {tmdb-12345}", "/media/Arrival (2016)"} {
			t.Run(libraryType+"/"+libraryRoot, func(t *testing.T) {
				filePath := filepath.Join(libraryRoot, "Arrival.2016.1080p.mkv")
				wantRoot := strings.TrimSuffix(filePath, filepath.Ext(filePath))
				ctx := ResolvePathContext(filePath, libraryType, libraryRoot)
				if ctx.Type != "movie" || ctx.Title != "Arrival" || ctx.Year != 2016 || ctx.RootPath != wantRoot {
					t.Fatalf("configured root supplied movie identity: %+v", ctx)
				}
				snapshots, assignments := InferRootAssignments([]string{filePath}, libraryType, 1, nil, libraryRoot)
				if len(snapshots) != 1 || snapshots[0].RootPath != wantRoot || snapshots[0].State != "resolved" {
					t.Errorf("configured root changed movie ownership: %+v", snapshots)
				}
				group := InferGroupIdentity(filePath, libraryType, assignments[filePath])
				if group.BaseTitle != "Arrival" || group.BaseYear != 2016 || group.TmdbID != "" || group.State != "resolved" {
					t.Errorf("configured root supplied movie group identity: %+v", group)
				}
			})
		}
	}
	filePath := "/media/Archive {tmdb-12345}/The.Show.S01E02.mkv"
	ctx := ResolvePathContext(filePath, "mixed", "/media/Archive {tmdb-12345}")
	if ctx.Type != "series" || ctx.Title != "The Show" || ctx.HasMovieFolderEvidence {
		t.Fatalf("configured root overrode explicit series evidence: %+v", ctx)
	}
}

func TestConfiguredRootProviderIDsDoNotIdentifyUnnamedEpisode(t *testing.T) {
	libraryRoot := "/media/Library [tvdbid=12345]"
	filePath := filepath.Join(libraryRoot, "E02.mkv")
	snapshots, assignments := InferRootAssignments([]string{filePath}, "series", 1, nil, libraryRoot)
	if len(snapshots) != 1 || snapshots[0].TvdbID != "" || snapshots[0].State != "ambiguous" {
		t.Fatalf("configured library IDs identified anonymous root: %+v", snapshots)
	}
	group := InferGroupIdentity(filePath, "series", assignments[filePath])
	if group.TvdbID != "" || group.State != "ambiguous" {
		t.Fatalf("configured library IDs identified anonymous group: %+v", group)
	}
	filePath = filepath.Join(libraryRoot, "E02 [tvdbid=67890].mkv")
	snapshots, assignments = InferRootAssignments([]string{filePath}, "series", 1, nil, libraryRoot)
	group = InferGroupIdentity(filePath, "series", assignments[filePath])
	if snapshots[0].RootPath != filePath || snapshots[0].TvdbID != "67890" || group.TvdbID != "67890" || group.State != "resolved" {
		t.Fatalf("file's own provider ID did not survive: roots=%+v, group=%+v", snapshots, group)
	}
}

func TestConfiguredRootSeasonDoesNotChangeRangeInterpretation(t *testing.T) {
	libraryRoot := "/media/Season 21"
	filePath := filepath.Join(libraryRoot, "Show.301-305.mkv")
	parsed := ParseFilename(filePath, "series", libraryRoot)
	variant := ParseVariantHints(filePath, "series", libraryRoot)
	if parsed.SeasonNum != 3 || parsed.EpisodeNum != 1 || variant.MultiEpisodeStart != parsed.EpisodeNum || variant.MultiEpisodeEnd != 5 {
		t.Fatalf("filename and range borrowed different context: filename=%+v, variant=%+v", parsed, variant)
	}
}

func TestConfiguredRootDistinguishesNumericShowsFromSeasons(t *testing.T) {
	for _, tc := range []struct {
		path, title string
		season      int
		known       bool
	}{
		{"/tv/86/E03.mkv", "86", 0, false},
		{"/tv/24/03.mkv", "24", 0, false},
		{"/tv/1923/1923 S01E01.mkv", "1923", 1, true},
		{"/tv/Show/02/E03.mkv", "Show", 2, true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			ctx := ResolvePathContext(tc.path, "series", "/tv")
			if ctx.Title != tc.title || ctx.SeasonNum != tc.season || ctx.SeasonKnown != tc.known {
				t.Fatalf("numeric show/season confusion: %+v", ctx)
			}
			_, assignments := InferRootAssignments([]string{tc.path}, "series", 1, nil, "/tv")
			group := InferGroupIdentity(tc.path, "series", assignments[tc.path])
			if group.BaseTitle != tc.title || group.State != "resolved" {
				t.Errorf("numeric title changed scan identity: %+v", group)
			}
		})
	}
}

func TestBareSeasonFolderJoinsParentShow(t *testing.T) {
	show := "/media/tv/Example Show (2013) {tvdb-123456}"
	paths := []string{
		show + "/Season/Example Show - S01E01.mkv",
		show + "/Season 2/Example Show - S02E01.mkv",
		show + "/Staffel/Example Show - S03E01.mkv",
	}
	_, assignments := InferRootAssignments(paths, "series", 1, nil, "/media/tv")
	for _, p := range paths {
		assignment := assignments[p]
		if assignment.RootPath != show {
			t.Errorf("%s: root %q, want %q", p, assignment.RootPath, show)
		}
		group := InferGroupIdentity(p, "series", assignment)
		if group.BaseTitle != "Example Show" || group.TvdbID != "123456" {
			t.Errorf("%s: identity %+v", p, group)
		}
	}
}

func TestBareSeasonLabelsExcludeCategoryFolders(t *testing.T) {
	for _, name := range []string{"Season", "season", "Staffel", "Temporada"} {
		if !isBareSeasonDirectory(name) {
			t.Errorf("%q should be a bare season folder", name)
		}
	}
	for _, name := range []string{"Series", "S", "Seasons", "Season 2", "Show"} {
		if isBareSeasonDirectory(name) {
			t.Errorf("%q should not be a bare season folder", name)
		}
	}
}

func TestBareSeasonFolderAtLibraryRootIsNotLifted(t *testing.T) {
	filePath := "/media/tv/Season/Example Show - S01E01.mkv"
	_, assignments := InferRootAssignments([]string{filePath}, "series", 1, nil, "/media/tv")
	if got := assignments[filePath].RootPath; got == "/media/tv" {
		t.Errorf("bare season folder at library root lifted to the library root: %q", got)
	}
}

func TestBareSeasonFolderOnlyCountsAsImmediateParent(t *testing.T) {
	cases := map[string]string{
		"/tv/Genres/Season/Shows/Example Show/Example.Show.S01E01.mkv": "/tv/Genres/Season/Shows/Example Show",
		"/tv/Season/Example.Show.S01E01.mkv":                           "/tv/Season",
	}
	for filePath, want := range cases {
		root, ok := deriveSeriesRoot(filePath, true, false, "/tv")
		if !ok || root.RootPath != want {
			t.Errorf("%s: root %+v ok=%v, want %q", filePath, root, ok, want)
		}
	}
	// A show named "Season" with a numbered season folder keeps its own name.
	root, ok := deriveSeriesRoot("/tv/Season/Season 1/Season.S01E01.mkv", true, false, "/tv")
	if !ok || root.RootPath != "/tv/Season" {
		t.Errorf("show named Season: root %+v ok=%v", root, ok)
	}
}

func TestBareSeasonFolderIsSeasonStructureEvidence(t *testing.T) {
	filePath := "/tv/Example Show/Season/Example.Show.S01E01.mkv"
	ctx := ResolvePathContext(filePath, "series", "/tv")
	if !ctx.HasSeasonStructure {
		t.Errorf("ResolvePathContext: HasSeasonStructure=false for bare season folder")
	}
	if ctx.RootPath != "/tv/Example Show" {
		t.Errorf("ResolvePathContext root %q", ctx.RootPath)
	}
	evidence := extractPathEvidence(filePath, "series", "/tv")
	if !evidence.HasSeasonStructure {
		t.Errorf("extractPathEvidence: HasSeasonStructure=false for bare season folder")
	}
	if _, ok := firstSeasonNumber([]string{"Example Show", "Season"}, true, true); ok {
		t.Errorf("bare season folder must not supply a season number")
	}
	// A bare folder higher up is not season structure.
	if found, _ := detectSeasonStructure([]string{"Genres", "Season", "Shows", "Example Show"}, true, true); found {
		t.Errorf("ancestor bare season folder counted as season structure")
	}
	_, assignments := InferRootAssignments([]string{filePath}, "series", 1, nil, "/tv")
	group := InferGroupIdentity(filePath, "series", assignments[filePath])
	if group.Confidence != "high" {
		t.Errorf("group confidence %q, want high", group.Confidence)
	}
}

func TestBareSeasonFolderInsideNumberedSeasonKeepsShow(t *testing.T) {
	shows := []string{"/media/tv/Example Show (2013) {tvdb-123456}", "/media/tv/Other Show (2015) {tvdb-654321}"}
	paths := []string{
		shows[0] + "/Season 2/Season/Example Show - S02E01.mkv",
		shows[1] + "/Season 2/Season/Other Show - S02E01.mkv",
	}
	_, assignments := InferRootAssignments(paths, "series", 1, nil, "/media/tv")
	keys := map[string]bool{}
	for i, p := range paths {
		assignment := assignments[p]
		if assignment.RootPath != shows[i] {
			t.Errorf("%s: root %q, want %q", p, assignment.RootPath, shows[i])
		}
		group := InferGroupIdentity(p, "series", assignment)
		if group.TvdbID == "" || keys[group.ContentGroupKey] {
			t.Errorf("%s: identity %+v", p, group)
		}
		keys[group.ContentGroupKey] = true
	}
	if number, ok := firstSeasonNumber([]string{"Example Show", "Season 2", "Season"}, true, true); !ok || number != 2 {
		t.Errorf("season number %d ok=%v, want 2 from the numbered ancestor", number, ok)
	}
}
