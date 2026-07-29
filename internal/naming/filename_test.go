package naming

import "testing"

func TestDetectSeriesRoot(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		libraryType string
		wantRoot    string
		wantOK      bool
	}{
		{
			name:     "standard season directory",
			path:     "/tv/Breaking Bad/Season 01/Breaking.Bad.S01E01.mkv",
			wantRoot: "/tv/Breaking Bad",
			wantOK:   true,
		},
		{
			name:     "season directory case insensitive",
			path:     "/tv/Show Name/season 03/Show.Name.S03E01.mkv",
			wantRoot: "/tv/Show Name",
			wantOK:   true,
		},
		{
			name:     "one pace arc folder collapses to series root",
			path:     "/television/anime/One Pace/Season 01 - Arc 01 - Romance Dawn/One.Piece.E01.1080p.mkv",
			wantRoot: "/television/anime/One Pace",
			wantOK:   true,
		},
		{
			name:     "specials directory",
			path:     "/tv/Show Name/Specials/Show.Name.S00E01.mkv",
			wantRoot: "/tv/Show Name",
			wantOK:   true,
		},
		{
			name:     "episodic file without season dir",
			path:     "/tv/Show Name/Show.Name.S01E01.mkv",
			wantRoot: "/tv/Show Name",
			wantOK:   true,
		},
		{
			name:     "movie file no series root",
			path:     "/movies/Arrival (2016)/Arrival.2016.1080p.mkv",
			wantRoot: "",
			wantOK:   false,
		},
		{
			name:     "year folder is not a season dir",
			path:     "/movies/2024/Some.Movie.mkv",
			wantRoot: "",
			wantOK:   false,
		},
		{
			name:     "1080p in path is not a season dir",
			path:     "/movies/Movie Name/1080p/Movie.Name.mkv",
			wantRoot: "",
			wantOK:   false,
		},
		{
			name:        "movie library ignores episode shaped movie folder",
			path:        "/movies/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}/s01e03 (2020).mkv",
			libraryType: "movies",
			wantRoot:    "",
			wantOK:      false,
		},
		{
			name:        "mixed library movie folder beats bare episode token",
			path:        "/mixed/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}/s01e03 (2020).mkv",
			libraryType: "mixed",
			wantRoot:    "",
			wantOK:      false,
		},
		{
			name:        "series library remains authoritative",
			path:        "/tv/Standalone Folder/random-file.mkv",
			libraryType: "series",
			wantRoot:    "/tv/Standalone Folder",
			wantOK:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, ok := DetectSeriesRoot(tt.path, tt.libraryType)
			if ok != tt.wantOK {
				t.Fatalf("DetectSeriesRoot(%q, %q) ok = %v, want %v", tt.path, tt.libraryType, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if root.RootPath != tt.wantRoot {
				t.Errorf("DetectSeriesRoot(%q, %q) root = %q, want %q", tt.path, tt.libraryType, root.RootPath, tt.wantRoot)
			}
		})
	}
}

func TestParseFilename(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		libraryType string
		wantTitle   string
		wantYear    int
		wantType    string
		wantSeason  int
		wantEp      int
		wantAirDate string
	}{
		{
			name:       "standard series episode",
			path:       "/tv/Breaking Bad (2008)/Season 01/Breaking.Bad.S01E01.mkv",
			wantTitle:  "Breaking Bad",
			wantYear:   2008,
			wantType:   "series",
			wantSeason: 1,
			wantEp:     1,
		},
		{
			name:       "extras maps to season zero",
			path:       "/tv/Show Name/Extras/Show.Name.S00E01.mkv",
			wantTitle:  "Show Name",
			wantYear:   0,
			wantType:   "series",
			wantSeason: 0,
			wantEp:     1,
		},
		{
			name:       "anime release with decimal episode suffix and release junk",
			path:       "/television/anime/A Sister's All You Need (2017) {tvdb-332994}/Season 01/A Sister's All You Need (2017) - S01E01.001 - I Only Need a Little Brother Who Can Cook a Beautiful Naked Girl and Friends I Can Relate to [Bluray-1080p][10bit][x265][AC3 5.1][EN+JA]-GHOST][.mkv",
			wantTitle:  "A Sister's All You Need",
			wantYear:   2017,
			wantType:   "series",
			wantSeason: 1,
			wantEp:     1,
		},
		{
			name:        "movie library episode token folder is movie",
			path:        "/movies/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}/s01e03 (2020).mkv",
			libraryType: "movies",
			wantTitle:   "s01e03",
			wantYear:    2020,
			wantType:    "movie",
			wantSeason:  0,
			wantEp:      0,
		},
		{
			name:        "mixed library obvious movie folder beats episode token",
			path:        "/mixed/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}/s01e03 (2020).mkv",
			libraryType: "mixed",
			wantTitle:   "s01e03",
			wantYear:    2020,
			wantType:    "movie",
			wantSeason:  0,
			wantEp:      0,
		},
		{
			name:        "mixed library flat tv folder stays series",
			path:        "/mixed/Show Name/Show Name S01E03.mkv",
			libraryType: "mixed",
			wantTitle:   "Show Name",
			wantYear:    0,
			wantType:    "series",
			wantSeason:  1,
			wantEp:      3,
		},
		{
			name:        "mixed library season dir stays series",
			path:        "/mixed/Show Name/Season 01/Show Name S01E03.mkv",
			libraryType: "mixed",
			wantTitle:   "Show Name",
			wantYear:    0,
			wantType:    "series",
			wantSeason:  1,
			wantEp:      3,
		},
		{
			name:        "movies library tv looking path stays movie",
			path:        "/movies/Show Name/Season 01/Show Name S01E03.mkv",
			libraryType: "movies",
			wantTitle:   "Show Name S01E03",
			wantYear:    0,
			wantType:    "movie",
			wantSeason:  0,
			wantEp:      0,
		},
		{
			name:        "series daily date episode",
			path:        "/tv/Jeopardy! (1984)/Season 2026/Jeopardy! (1984) - 2026-04-24 - Jamie Ding Zach Pollock Nicco Martinez.mkv",
			libraryType: "series",
			wantTitle:   "Jeopardy!",
			wantYear:    1984,
			wantType:    "series",
			wantSeason:  2026,
			wantEp:      0,
			wantAirDate: "2026-04-24",
		},
		{
			name:        "series daily date episode with dotted separator",
			path:        "/tv/The Daily Show/The.Daily.Show.2024.02.15.Guest.Name.mkv",
			libraryType: "series",
			wantTitle:   "The Daily Show",
			wantYear:    0,
			wantType:    "series",
			wantSeason:  0,
			wantEp:      0,
			wantAirDate: "2024-02-15",
		},
		{
			name:        "movie library date does not become tv air date",
			path:        "/movies/News Archive (2026)/News Archive 2026-04-24.mkv",
			libraryType: "movies",
			wantTitle:   "News Archive",
			wantYear:    2026,
			wantType:    "movie",
			wantSeason:  0,
			wantEp:      0,
			wantAirDate: "",
		},
		{
			name:       "movie in folder with year",
			path:       "/movies/Arrival (2016)/Arrival.2016.1080p.mkv",
			wantTitle:  "Arrival",
			wantYear:   2016,
			wantType:   "movie",
			wantSeason: 0,
			wantEp:     0,
		},
		{
			name:       "movie with imdbid tag no year",
			path:       "/movies/Big Buck Bunny [imdbid-tt1254207]/Big Buck Bunny.mp4",
			wantTitle:  "Big Buck Bunny",
			wantYear:   0,
			wantType:   "movie",
			wantSeason: 0,
			wantEp:     0,
		},
		{
			name:       "1080p in filename is not a year",
			path:       "/movies/Movie Name (2020)/Movie.Name.1080p.BluRay.mkv",
			wantTitle:  "Movie Name",
			wantYear:   2020,
			wantType:   "movie",
			wantSeason: 0,
			wantEp:     0,
		},
		{
			name:       "numeric folder not treated as season without evidence",
			path:       "/movies/2024/Some.Movie.mkv",
			wantTitle:  "Some.Movie",
			wantYear:   0,
			wantType:   "movie",
			wantSeason: 0,
			wantEp:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := ParseFilename(tt.path, tt.libraryType)
			if h.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", h.Type, tt.wantType)
			}
			if h.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", h.Title, tt.wantTitle)
			}
			if h.Year != tt.wantYear {
				t.Errorf("Year = %d, want %d", h.Year, tt.wantYear)
			}
			if h.SeasonNum != tt.wantSeason {
				t.Errorf("SeasonNum = %d, want %d", h.SeasonNum, tt.wantSeason)
			}
			if h.EpisodeNum != tt.wantEp {
				t.Errorf("EpisodeNum = %d, want %d", h.EpisodeNum, tt.wantEp)
			}
			if h.AirDate != tt.wantAirDate {
				t.Errorf("AirDate = %q, want %q", h.AirDate, tt.wantAirDate)
			}
		})
	}
}

func TestParseFilename_StripsStructuredProviderIDsFromTitles(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path      string
		wantTitle string
		wantYear  int
	}{
		{
			path:      "/movies/10 Tricks (2022)/10 Tricks (2022) (tt0473100) [WEBDL-1080p.AAC.h264.GNOME].mp4",
			wantTitle: "10 Tricks",
			wantYear:  2022,
		},
		{path: "/movies/Some Movie [tt1234567]/Some Movie.mkv", wantTitle: "Some Movie"},
		{path: "/movies/Some Movie (tmdb-12345)/Some Movie.mkv", wantTitle: "Some Movie"},
	} {
		hints := ParseFilename(test.path, "movie")
		if hints.Title != test.wantTitle || hints.Year != test.wantYear {
			t.Errorf("ParseFilename(%q) = title %q year %d, want %q/%d", test.path, hints.Title, hints.Year, test.wantTitle, test.wantYear)
		}
	}
}

func TestParseFilename_SeriesReleasePackDirectoryUsesParentShowRoot(t *testing.T) {
	path := "/library/Becoming You/Becoming.You.S01.HDR.2160p.WEB-DL.DDP5.1.H.265-ROCCaT/Becoming.You.S01E01.HDR.2160p.WEB-DL.mkv"

	hints := ParseFilename(path, "series")
	if hints == nil {
		t.Fatal("ParseFilename returned nil")
		return
	}
	if hints.Title != "Becoming You" || hints.Year != 0 || hints.Type != "series" {
		t.Fatalf("ParseFilename() = title %q year %d type %q, want Becoming You/0/series", hints.Title, hints.Year, hints.Type)
	}
	root, ok := DetectSeriesRoot(path, "series")
	if !ok || root.RootPath != "/library/Becoming You" {
		t.Fatalf("DetectSeriesRoot() = %+v, %v", root, ok)
	}
}

func TestParseFilename_SeriesReleasePackDirectoryRequiresCoherentParent(t *testing.T) {
	t.Parallel()
	flatPath := "/library/Becoming.You.S01.HDR.2160p.WEB-DL-GROUP/Becoming.You.S01E01.mkv"
	root, ok := DetectSeriesRoot(flatPath, "series")
	if !ok || root.RootPath != "/library/Becoming.You.S01.HDR.2160p.WEB-DL-GROUP" {
		t.Fatalf("flat release root = %+v, %v; must not collapse to the library root", root, ok)
	}

	nestedPath := "/library/Becoming You (2020)/Becoming.You.S01.1080p.WEB-DL/Becoming.You.S01E01.mkv"
	root, ok = DetectSeriesRoot(nestedPath, "series")
	if !ok || root.RootPath != "/library/Becoming You (2020)" {
		t.Fatalf("nested release root = %+v, %v; want coherent parent show folder", root, ok)
	}
}

func TestResolvePathContext(t *testing.T) {
	tests := []struct {
		name                    string
		path                    string
		libraryType             string
		wantType                string
		wantRoot                string
		wantTitle               string
		wantYear                int
		wantSeason              int
		wantEpisode             int
		wantAirDate             string
		wantAirDatePattern      bool
		wantEpisodePattern      bool
		wantSeasonStructure     bool
		wantMovieFolderEvidence bool
	}{
		{
			name:                    "movies library episode token folder is movie",
			path:                    "/movies/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}/s01e03 (2020).mkv",
			libraryType:             "movies",
			wantType:                "movie",
			wantRoot:                "/movies/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}",
			wantTitle:               "s01e03",
			wantYear:                2020,
			wantEpisodePattern:      true,
			wantSeasonStructure:     false,
			wantMovieFolderEvidence: true,
		},
		{
			name:                    "mixed library obvious movie folder beats episode token",
			path:                    "/mixed/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}/s01e03 (2020).mkv",
			libraryType:             "mixed",
			wantType:                "movie",
			wantRoot:                "/mixed/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}",
			wantTitle:               "s01e03",
			wantYear:                2020,
			wantEpisodePattern:      true,
			wantSeasonStructure:     false,
			wantMovieFolderEvidence: true,
		},
		{
			name:                    "mixed flat tv folder stays series",
			path:                    "/mixed/Show Name/Show Name S01E03.mkv",
			libraryType:             "mixed",
			wantType:                "series",
			wantRoot:                "/mixed/Show Name",
			wantTitle:               "Show Name",
			wantSeason:              1,
			wantEpisode:             3,
			wantEpisodePattern:      true,
			wantSeasonStructure:     false,
			wantMovieFolderEvidence: false,
		},
		{
			name:                    "mixed season dir stays series",
			path:                    "/mixed/Show Name/Season 01/Show Name S01E03.mkv",
			libraryType:             "mixed",
			wantType:                "series",
			wantRoot:                "/mixed/Show Name",
			wantTitle:               "Show Name",
			wantSeason:              1,
			wantEpisode:             3,
			wantEpisodePattern:      true,
			wantSeasonStructure:     true,
			wantMovieFolderEvidence: false,
		},
		{
			name:                    "anime release with decimal episode suffix keeps show root",
			path:                    "/television/anime/A Sister's All You Need (2017) {tvdb-332994}/Season 01/A Sister's All You Need (2017) - S01E01.001 - I Only Need a Little Brother Who Can Cook a Beautiful Naked Girl and Friends I Can Relate to [Bluray-1080p][10bit][x265][AC3 5.1][EN+JA]-GHOST][.mkv",
			libraryType:             "series",
			wantType:                "series",
			wantRoot:                "/television/anime/A Sister's All You Need (2017) {tvdb-332994}",
			wantTitle:               "A Sister's All You Need",
			wantYear:                2017,
			wantSeason:              1,
			wantEpisode:             1,
			wantEpisodePattern:      true,
			wantSeasonStructure:     true,
			wantMovieFolderEvidence: false,
		},
		{
			name:                    "series daily date has air date but no episode number",
			path:                    "/tv/Jeopardy! (1984)/Season 2026/Jeopardy! (1984) - 2026_04_24 - Jamie Ding.mkv",
			libraryType:             "series",
			wantType:                "series",
			wantRoot:                "/tv/Jeopardy! (1984)",
			wantTitle:               "Jeopardy!",
			wantYear:                1984,
			wantSeason:              2026,
			wantEpisode:             0,
			wantAirDate:             "2026-04-24",
			wantAirDatePattern:      true,
			wantEpisodePattern:      false,
			wantSeasonStructure:     true,
			wantMovieFolderEvidence: false,
		},
		{
			name:                    "numeric year folder is not season structure",
			path:                    "/movies/2024/Some.Movie.mkv",
			wantType:                "movie",
			wantRoot:                "/movies/2024/Some.Movie",
			wantTitle:               "Some.Movie",
			wantEpisodePattern:      false,
			wantSeasonStructure:     false,
			wantMovieFolderEvidence: false,
		},
		{
			name:                    "id tagged movie folder beats divergent release filename",
			path:                    "/movies/The Expendables 4 {imdb-tt3291150} {tmdb-299054}/Expend4bles (2023) [Remux-1080p 8-bit AVC TrueHD Atmos 7.1]-CiNEPHiLES.mkv",
			libraryType:             "movies",
			wantType:                "movie",
			wantRoot:                "/movies/The Expendables 4 {imdb-tt3291150} {tmdb-299054}",
			wantTitle:               "The Expendables 4",
			wantYear:                0,
			wantEpisodePattern:      false,
			wantSeasonStructure:     false,
			wantMovieFolderEvidence: true,
		},
		{
			name:                    "mixed library id tagged movie folder beats divergent release filename",
			path:                    "/mixed/The Expendables 4 {imdb-tt3291150} {tmdb-299054}/Expend4bles (2023) [Remux-1080p 8-bit AVC TrueHD Atmos 7.1]-CiNEPHiLES.mkv",
			libraryType:             "mixed",
			wantType:                "movie",
			wantRoot:                "/mixed/The Expendables 4 {imdb-tt3291150} {tmdb-299054}",
			wantTitle:               "The Expendables 4",
			wantYear:                0,
			wantEpisodePattern:      false,
			wantSeasonStructure:     false,
			wantMovieFolderEvidence: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ResolvePathContext(tt.path, tt.libraryType)
			if ctx == nil {
				t.Fatal("expected non-nil PathContext")
			}
			if ctx.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", ctx.Type, tt.wantType)
			}
			if ctx.RootPath != tt.wantRoot {
				t.Errorf("RootPath = %q, want %q", ctx.RootPath, tt.wantRoot)
			}
			if ctx.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", ctx.Title, tt.wantTitle)
			}
			if ctx.Year != tt.wantYear {
				t.Errorf("Year = %d, want %d", ctx.Year, tt.wantYear)
			}
			if ctx.SeasonNum != tt.wantSeason {
				t.Errorf("SeasonNum = %d, want %d", ctx.SeasonNum, tt.wantSeason)
			}
			if ctx.EpisodeNum != tt.wantEpisode {
				t.Errorf("EpisodeNum = %d, want %d", ctx.EpisodeNum, tt.wantEpisode)
			}
			if ctx.AirDate != tt.wantAirDate {
				t.Errorf("AirDate = %q, want %q", ctx.AirDate, tt.wantAirDate)
			}
			if ctx.HasAirDatePattern != tt.wantAirDatePattern {
				t.Errorf("HasAirDatePattern = %v, want %v", ctx.HasAirDatePattern, tt.wantAirDatePattern)
			}
			if ctx.HasEpisodePattern != tt.wantEpisodePattern {
				t.Errorf("HasEpisodePattern = %v, want %v", ctx.HasEpisodePattern, tt.wantEpisodePattern)
			}
			if ctx.HasSeasonStructure != tt.wantSeasonStructure {
				t.Errorf("HasSeasonStructure = %v, want %v", ctx.HasSeasonStructure, tt.wantSeasonStructure)
			}
			if ctx.HasMovieFolderEvidence != tt.wantMovieFolderEvidence {
				t.Errorf("HasMovieFolderEvidence = %v, want %v", ctx.HasMovieFolderEvidence, tt.wantMovieFolderEvidence)
			}
		})
	}
}

func TestDetectCanonicalRoot(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		libraryType string
		wantRoot    string
		wantType    string
		wantOK      bool
	}{
		{
			name:     "standard series root",
			path:     "/tv/Breaking Bad/Season 01/Breaking.Bad.S01E01.mkv",
			wantRoot: "/tv/Breaking Bad",
			wantType: "series",
			wantOK:   true,
		},
		{
			name:     "movie in folder",
			path:     "/movies/Arrival (2016)/Arrival.2016.1080p.mkv",
			wantRoot: "/movies/Arrival (2016)",
			wantType: "movie",
			wantOK:   true,
		},
		{
			name:     "loose movie file uses file stem as root",
			path:     "/movies/Arrival (2016).mkv",
			wantRoot: "/movies/Arrival (2016)",
			wantType: "movie",
			wantOK:   true,
		},
		{
			name:        "mixed library obvious movie folder uses movie root",
			path:        "/mixed/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}/s01e03 (2020).mkv",
			libraryType: "mixed",
			wantRoot:    "/mixed/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}",
			wantType:    "movie",
			wantOK:      true,
		},
		{
			name:        "movies library tv looking path still uses movie root",
			path:        "/movies/Show Name/Season 01/Show Name S01E03.mkv",
			libraryType: "movies",
			wantRoot:    "/movies/Show Name/Season 01/Show Name S01E03",
			wantType:    "movie",
			wantOK:      true,
		},
		{
			name:        "mixed flat tv folder uses series root",
			path:        "/mixed/Show Name/Show Name S01E03.mkv",
			libraryType: "mixed",
			wantRoot:    "/mixed/Show Name",
			wantType:    "series",
			wantOK:      true,
		},
		{
			name:     "movie root uses parent when comparable normalization differs",
			path:     "/movies/WALL-E {imdb-tt0910970}/Wall-E.mkv",
			wantRoot: "/movies/WALL-E {imdb-tt0910970}",
			wantType: "movie",
			wantOK:   true,
		},
		{
			name:     "movie root uses id tagged parent for divergent release filename",
			path:     "/movies/The Expendables 4 {imdb-tt3291150} {tmdb-299054}/Expend4bles (2023) [Remux-1080p 8-bit AVC TrueHD Atmos 7.1]-CiNEPHiLES.mkv",
			wantRoot: "/movies/The Expendables 4 {imdb-tt3291150} {tmdb-299054}",
			wantType: "movie",
			wantOK:   true,
		},
		{
			name:   "absolute root-level file returns false",
			path:   "/single-file.mkv",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr, ok := DetectCanonicalRoot(tt.path, tt.libraryType)
			if ok != tt.wantOK {
				t.Fatalf("DetectCanonicalRoot(%q, %q) ok = %v, want %v", tt.path, tt.libraryType, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if cr.RootPath != tt.wantRoot {
				t.Errorf("RootPath = %q, want %q", cr.RootPath, tt.wantRoot)
			}
			if cr.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", cr.Type, tt.wantType)
			}
		})
	}
}
