package naming

import (
	"path/filepath"
	"testing"
)

// Cases follow Jellyfin v12.1 SeasonPathParserTests at commit
// ee91c75e777da41a9c4f4855e70adc604fbf2ef8. Expectations cover TV and mixed libraries.
func TestSeasonDirectoryJellyfinConventions(t *testing.T) {
	for _, tt := range []struct {
		path         string
		parent       string
		season       int
		ok           bool
		allowNumeric bool
	}{
		{"/Drive/Season 1", "/Drive", 1, true, true},
		{"/Drive/SEASON 1", "/Drive", 1, true, true},
		{"/Drive/Staffel 1", "/Drive", 1, true, true},
		{"/Drive/STAFFEL 1", "/Drive", 1, true, true},
		{"/Drive/Stagione 1", "/Drive", 1, true, true},
		{"/Drive/STAGIONE 1", "/Drive", 1, true, true},
		{"/Drive/sæson 1", "/Drive", 1, true, true},
		{"/Drive/SÆSON 1", "/Drive", 1, true, true},
		{"/Drive/Temporada 1", "/Drive", 1, true, true},
		{"/Drive/TEMPORADA 1", "/Drive", 1, true, true},
		{"/Drive/series 1", "/Drive", 1, true, true},
		{"/Drive/SERIES 1", "/Drive", 1, true, true},
		{"/Drive/Kausi 1", "/Drive", 1, true, true},
		{"/Drive/KAUSI 1", "/Drive", 1, true, true},
		{"/Drive/Säsong 1", "/Drive", 1, true, true},
		{"/Drive/SÄSONG 1", "/Drive", 1, true, true},
		{"/Drive/Seizoen 1", "/Drive", 1, true, true},
		{"/Drive/SEIZOEN 1", "/Drive", 1, true, true},
		{"/Drive/Seasong 1", "/Drive", 1, true, true},
		{"/Drive/SEASONG 1", "/Drive", 1, true, true},
		{"/Drive/Sezon 1", "/Drive", 1, true, true},
		{"/Drive/SEZON 1", "/Drive", 1, true, true},
		{"/Drive/sezona 1", "/Drive", 1, true, true},
		{"/Drive/SEZONA 1", "/Drive", 1, true, true},
		{"/Drive/sezóna 1", "/Drive", 1, true, true},
		{"/Drive/SEZÓNA 1", "/Drive", 1, true, true},
		{"/Drive/Sezonul 1", "/Drive", 1, true, true},
		{"/Drive/SEZONUL 1", "/Drive", 1, true, true},
		{"/Drive/시즌 1", "/Drive", 1, true, true},
		{"/Drive/シーズン 1", "/Drive", 1, true, true},
		{"/Drive/сезон 1", "/Drive", 1, true, true},
		{"/Drive/Сезон 1", "/Drive", 1, true, true},
		{"/Drive/СЕЗОН 1", "/Drive", 1, true, true},
		{"/Drive/Season 10", "/Drive", 10, true, true},
		{"/Drive/Season 100", "/Drive", 100, true, true},
		{"/Drive/s1", "/Drive", 1, true, true},
		{"/Drive/S1", "/Drive", 1, true, true},
		{"/Drive/Season 2", "/Drive", 2, true, true},
		{"/Drive/Season 02", "/Drive", 2, true, true},
		{"/Drive/Seinfeld/S02", "/Seinfeld", 2, true, true},
		{"/Drive/Seinfeld/2", "/Seinfeld", 2, true, true},
		{"/Drive/Seinfeld Season 2", "/Drive", 0, false, true},
		{"/Drive/Season 2009", "/Drive", 2009, true, true},
		{"/Drive/Season1", "/Drive", 1, true, true},
		{"The Wonder Years/The.Wonder.Years.S04.PDTV.x264-JCH", "/The Wonder Years", 4, true, true},
		{"/Drive/Season 7 (2016)", "/Drive", 7, true, true},
		{"/Drive/Staffel 7 (2016)", "/Drive", 7, true, true},
		{"/Drive/Stagione 7 (2016)", "/Drive", 7, true, true},
		{"/Drive/Stargate SG-1/Season 1", "/Drive/Stargate SG-1", 1, true, true},
		{"/Drive/Stargate SG-1/Stargate SG-1 Season 1", "/Drive/Stargate SG-1", 1, true, true},
		{"/Drive/Season (8)", "/Drive", 0, false, true},
		{"/Drive/3.Staffel", "/Drive", 3, true, true},
		{"/Drive/s06e05", "/Drive", 0, false, true},
		{"/Drive/The.Legend.of.Condor.Heroes.2017.V2.web-dl.1080p.h264.aac-hdctv", "/Drive", 0, false, true},
		{"/Drive/extras", "/Drive", 0, true, true},
		{"/Drive/EXTRAS", "/Drive", 0, true, true},
		{"/Drive/specials", "/Drive", 0, true, true},
		{"/Drive/SPECIALS", "/Drive", 0, true, true},
		{"/Drive/Episode 1 Season 2", "/Drive", 0, false, true},
		{"/Drive/Episode 1 SEASON 2", "/Drive", 0, false, true},
		{"/media/YouTube/Devyn Johnston/2024-01-24 4070 Ti SUPER in under 7 minutes", "/media/YouTube/Devyn Johnston", 0, false, true},
		{"/media/YouTube/Devyn Johnston/2025-01-28 5090 vs 2 SFF Cases", "/media/YouTube/Devyn Johnston", 0, false, true},
		{"/Drive/202401244070", "/Drive", 0, false, true},
		{"/Drive/Drive.S01.2160p.WEB-DL.DDP5.1.H.265-XXXX", "/Drive", 1, true, true},
		{"The Wonder Years/The.Wonder.Years.S04.1080p.PDTV.x264-JCH", "/The Wonder Years", 4, true, true},
		{"The Wonder Years/[The.Wonder.Years.S04.1080p.PDTV.x264-JCH]", "/The Wonder Years", 4, true, true},
		{"The Wonder Years/The.Wonder.Years [S04][1080p.PDTV.x264-JCH]", "/The Wonder Years", 4, true, true},
		{"The Wonder Years/The Wonder Years Season 01 1080p", "/The Wonder Years", 1, true, true},
		{"/Drive/300 Collection/300 (2006)", "/Drive/300 Collection", 0, false, false},
		{"/Drive/300 Collection/300 Rise of an Empire", "/Drive/300 Collection", 0, false, false},
		{"/Drive/300 Collection/1", "/Drive/300 Collection", 0, false, false},
		{"/Drive/300 Collection/300 Disc 1", "/Drive/300 Collection", 0, false, false},
		{"/Drive/28 Years Later Collection/28 Days Later", "/Drive/28 Years Later Collection", 0, false, false},
		{"/Drive/28 Years Later Collection/28 Weeks Later (2007)", "/Drive/28 Years Later Collection", 0, false, false},
		{"/Drive/28 Years Later Collection/28 Years Later 2025", "/Drive/28 Years Later Collection", 0, false, false},
		{"/Drive/300 Collection/Season 1", "/Drive/300 Collection", 1, true, false},
		{"/Drive/28 Years Later Collection/Season 01", "/Drive/28 Years Later Collection", 1, true, false},
		{"/Drive/300 Collection/S01", "/Drive/300 Collection", 1, true, false},
		{"/Drive/300 Collection/S1", "/Drive/300 Collection", 1, true, false},
	} {
		t.Run(tt.path, func(t *testing.T) {
			got, ok := seasonDirectoryNumber(filepath.Base(tt.path), tt.parent, tt.allowNumeric, false)
			if got != tt.season || ok != tt.ok {
				t.Fatalf("seasonDirectoryNumber = %d, %v; want %d, %v", got, ok, tt.season, tt.ok)
			}
		})
	}
}

func TestSeasonDirectoryRejectsUncorroboratedNames(t *testing.T) {
	for _, tt := range []struct{ segment, parent string }{
		{"Other Show Season 2", "Example Show"},
		{"Example Show Season 2", ""},
		{"ExampleShowSeason2", "Example Show"},
		{"E01Season2", "Example Show"},
		{"Episode 1 Season 2", "Example Show"},
		{"1920x1080", "Example Show"},
		{"Season 1920x1080", "Example Show"},
		{"2024-01-24", "Example Show"},
		{"Season 12345", "Example Show"},
		{"S06E05", "Example Show"},
	} {
		t.Run(tt.segment, func(t *testing.T) {
			if got, ok := seasonDirectoryNumber(tt.segment, tt.parent, true, false); ok {
				t.Fatalf("unexpected season %d", got)
			}
		})
	}
}

func TestSeasonDirectoryAdditionalConventions(t *testing.T) {
	for _, tt := range []struct {
		segment, parent string
		season          int
	}{
		{"Season_02", "Example Show", 2},
		{"SEASON-02 - Arc 1", "Example Show", 2},
		{"Special", "Example Show", 0},
		{"Extra", "Example Show", 0},
		{"Example.Show.S02.1080p", "Example Show [tvdbid-123]", 2},
		{"Example.Show.S02.1080p", `C:\TV\Example Show`, 2},
		{"12.Staffel", "Example Show", 12},
	} {
		t.Run(tt.segment, func(t *testing.T) {
			got, ok := seasonDirectoryNumber(tt.segment, tt.parent, true, false)
			if got != tt.season || !ok {
				t.Fatalf("seasonDirectoryNumber = %d, %v; want %d, true", got, ok, tt.season)
			}
		})
	}
}

func TestSeasonDirectoryLocalizedAndOrdinalLabels(t *testing.T) {
	for _, tt := range []struct {
		segment string
		season  int
	}{
		{"Saison 1", 1},
		{"SAISON 02", 2},
		{"Saison 3 (2019)", 3},
		{"Serie 2", 2},
		{"Série 2", 2},
		{"SÉRIE 2", 2},
		{"Se\u0301rie 4", 4}, // decomposed é, as macOS stores it
		{"Seria 3", 3},
		{"Séria 3", 3},
		{"Se\u0301ria 5", 5},
		{"Sa\u0308song 6", 6},
		{"Sezo\u0301na 7", 7},
		{"Series 2 (2019)", 2},
		{"Series.2.1080p.BluRay.x264-GRP", 2},
		{"Series 2 [1080p]", 2},
		{"Series 2 {tvdb-12345}", 2},
		{"Series 2 HDR", 2},
		{"Series 2 (2019) [REMUX]", 2},
		{"Series 2 2019 DV", 2},
		{"Series 2 UHD", 2},
		{"Series 2 4K", 2},
		{"Series 2 HDR10", 2},
		{"Series 2 SDR", 2},
		{"1st Season", 1},
		{"2nd Season", 2},
		{"3rd Season", 3},
		{"4th Season", 4},
		{"11th Season", 11},
		{"21st Season", 21},
		{"2nd.Season.1080p", 2},
		{"3rd Saison", 3},
	} {
		t.Run(tt.segment, func(t *testing.T) {
			for _, topLevel := range []bool{false, true} {
				got, ok := seasonDirectoryNumber(tt.segment, "Example Show", false, topLevel)
				if !ok || got != tt.season {
					t.Fatalf("seasonDirectoryNumber(topLevel=%v) = %d, %v; want %d, true", topLevel, got, ok, tt.season)
				}
			}
		})
	}
}

// Series, serie, and seria also start show titles. A folder directly in the
// library folder is the show unless only release tags follow the number;
// deeper, the UK form Series 5 Matt Smith stays a season.
func TestSeasonDirectoryWordLabelsAtTheLibraryTop(t *testing.T) {
	for _, segment := range []string{
		"Serie A",
		"Seria",
		"Série",
		"Series",
		"Series 7 Documentary",
		"Serie 2 Temporada Completa",
		"Seria 2 Odcinki",
		"1st Series Of Tests",
		"1st",
		"1st Saisonnier",
		"Series 7 - The Contenders",
		"Series 7 (Documentary)",
		"Series 7 (2019) - The Contenders",
		"Series 7 2019 Documentary",
		"21st S.W.A.T. Archive",
	} {
		t.Run(segment, func(t *testing.T) {
			if got, ok := seasonDirectoryNumber(segment, "", true, true); ok {
				t.Fatalf("unexpected season %d", got)
			}
		})
	}
	for _, segment := range []string{"Serie A", "Seria", "Series"} {
		if got, ok := seasonDirectoryNumber(segment, "Example Show", true, false); ok {
			t.Fatalf("%s: unexpected season %d", segment, got)
		}
	}
	if got, ok := seasonDirectoryNumber("21st S.W.A.T. Archive", "Example Show", false, false); ok {
		t.Fatalf("nested 21st S.W.A.T. Archive = season %d", got)
	}
	for _, tt := range []struct {
		segment  string
		topLevel bool
	}{
		{"Series 5 Matt Smith", false},
		{"Series 5 - Extras", false},
		{"Example Show Series 5 Matt Smith", true},
	} {
		if got, ok := seasonDirectoryNumber(tt.segment, "Example Show", false, tt.topLevel); !ok || got != 5 {
			t.Fatalf("%s = %d, %v; want 5, true", tt.segment, got, ok)
		}
	}
}

func TestLocalizedSeasonFoldersSitUnderTheShow(t *testing.T) {
	for _, tt := range []struct {
		path, title     string
		season, episode int
	}{
		{"/tv/Le Grand Bureau/Saison 1/Le Grand Bureau - S01E02.mkv", "Le Grand Bureau", 1, 2},
		{"/tv/Ordinal Example Show/1st Season/Episode 03.mkv", "Ordinal Example Show", 1, 3},
		{"/tv/Ordinal Example Show/2nd Season/Episode 01.mkv", "Ordinal Example Show", 2, 1},
		{"/tv/Serie Mundo/Série 3/Serie Mundo - 01.mkv", "Serie Mundo", 3, 1},
		{"/tv/Serie Mundo/Se\u0301rie 3/Serie Mundo - 01.mkv", "Serie Mundo", 3, 1},
		{"/tv/Polska Seria Przyklad/Seria 2/Polska Seria Przyklad - 01.mkv", "Polska Seria Przyklad", 2, 1},
		{"/tv/Example Show/Series 5 Matt Smith/Episode 01.mkv", "Example Show", 5, 1},
		{"/tv/Serie A/Season 1/Serie A - S01E02.mkv", "Serie A", 1, 2},
		{"/tv/Seria/Seria - S01E02.mkv", "Seria", 1, 2},
	} {
		t.Run(tt.path, func(t *testing.T) {
			hints := ParseFilename(tt.path, "series", "/tv")
			if hints.Title != tt.title || hints.SeasonNum != tt.season || hints.EpisodeNum != tt.episode {
				t.Fatalf("ParseFilename = %q S%dE%d; want %q S%dE%d", hints.Title, hints.SeasonNum, hints.EpisodeNum, tt.title, tt.season, tt.episode)
			}
			showRoot := filepath.Dir(filepath.Dir(tt.path))
			if filepath.Base(showRoot) == "tv" {
				showRoot = filepath.Dir(tt.path)
			}
			ctx := ResolvePathContext(tt.path, "mixed", "/tv")
			if ctx.Type != "series" || ctx.RootPath != showRoot {
				t.Fatalf("mixed ResolvePathContext = %s %q; want series %q", ctx.Type, ctx.RootPath, showRoot)
			}
			_, assignments := InferRootAssignments([]string{tt.path}, "series", 1, nil, "/tv")
			if got := assignments[tt.path]; got.RootPath != showRoot || got.Title != tt.title {
				t.Fatalf("InferRootAssignments = %q %q; want %q %q", got.RootPath, got.Title, showRoot, tt.title)
			}
		})
	}
}

func TestSeriesTitledLikeASeasonIsTheShow(t *testing.T) {
	const path = "/tv/Series 7 Documentary/Episode 03.mkv"
	ctx := ResolvePathContext(path, "series", "/tv")
	if ctx.RootPath != "/tv/Series 7 Documentary" || ctx.Title != "Series 7 Documentary" || ctx.SeasonKnown {
		t.Fatalf("ResolvePathContext = root %q title %q season %d known %v; want the show folder, season unknown", ctx.RootPath, ctx.Title, ctx.SeasonNum, ctx.SeasonKnown)
	}
	_, assignments := InferRootAssignments([]string{path}, "series", 1, nil, "/tv")
	if got := assignments[path]; got.RootPath != "/tv/Series 7 Documentary" || got.Title != "Series 7 Documentary" {
		t.Fatalf("InferRootAssignments = %q %q; want the show folder", got.RootPath, got.Title)
	}
	if IsMisplacedSeriesFile("/movies/Series 7 Documentary/Series 7 Documentary S01E03.mkv", "/movies") {
		t.Fatal("IsMisplacedSeriesFile took the title folder for a season folder")
	}
	// A show title before the label is evidence enough, as on main.
	title, _, season, _ := parseSeriesFolderIdentity("Example Show Series 5 Matt Smith")
	if title != "Example Show" || season != 5 {
		t.Fatalf("parseSeriesFolderIdentity = %q season %d; want Example Show season 5", title, season)
	}
}

func TestWordSeasonLabelsKeepShowContext(t *testing.T) {
	// A library folder that is one show keeps its UK season folders when the
	// file name names that show.
	ctx := ResolvePathContext("/tv/Doctor Who/Series 5 Matt Smith/Doctor Who S05E03.mkv", "series", "/tv/Doctor Who")
	if ctx.Title != "Doctor Who" || ctx.SeasonNum != 5 || !ctx.SeasonKnown {
		t.Fatalf("single-show library = %q S%d known %v; want Doctor Who S5", ctx.Title, ctx.SeasonNum, ctx.SeasonKnown)
	}
	// Without a configured library folder, no folder is taken for the show.
	ctx = ResolvePathContext("Series 5 Matt Smith/03-04.mkv", "mixed")
	if ctx.Type != "series" || ctx.SeasonNum != 5 || ctx.EpisodeNum != 3 {
		t.Fatalf("rootless = %s S%dE%d; want series S5E3", ctx.Type, ctx.SeasonNum, ctx.EpisodeNum)
	}
	if !IsMisplacedSeriesFile("Series 5 Matt Smith/03-04.mkv") {
		t.Fatal("rootless IsMisplacedSeriesFile lost the season folder")
	}
	if hints := ParseVariantHints("Series 5 Matt Smith/03-04.mkv", "mixed"); hints.PresentationKind != "multi_episode" {
		t.Fatalf("rootless ParseVariantHints = %q; want multi_episode", hints.PresentationKind)
	}
	// A show title that contains a season label stays whole when the file
	// name repeats it.
	for _, title := range []string{"La Série 2", "La Series 2"} {
		path := "/tv/" + title + "/" + title + " S01E03.mkv"
		ctx = ResolvePathContext(path, "series", "/tv")
		if ctx.Title != title || ctx.SeasonNum != 1 {
			t.Fatalf("%s = %q S%d; want the whole title, S1", title, ctx.Title, ctx.SeasonNum)
		}
		_, assignments := InferRootAssignments([]string{path}, "series", 1, nil, "/tv")
		if got := assignments[path].Title; got != title {
			t.Fatalf("%s InferRootAssignments title = %q", title, got)
		}
	}
}
