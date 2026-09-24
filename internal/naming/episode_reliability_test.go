package naming

import "testing"

func TestExplicitEpisodeMarkersSupportLargeSeasonNumbers(t *testing.T) {
	for _, tt := range []struct {
		path    string
		season  int
		episode int
	}{
		{"/tv/Example Show/Season 200/Example.Show.S200E02.1080p.mkv", 200, 2},
		{"/tv/Example Show/Season 345/Example Show Season 345 Episode 8.mkv", 345, 8},
		{"/tv/Incoming/Example.Show.S0300E02.mkv", 300, 2},
		{"/tv/Incoming/Example.Show.S999E01.mkv", 999, 1},
		{"/tv/Incoming/Example Show S999 E01.mkv", 999, 1},
		{"/tv/Incoming/Example Show Season 999 Episode 1.mkv", 999, 1},
		{"/tv/Incoming/Example.Show.S1920E01.mkv", 1920, 1},
	} {
		t.Run(tt.path, func(t *testing.T) {
			hints := ParseFilename(tt.path, "series", "/tv")
			if hints.SeasonNum != tt.season || hints.EpisodeNum != tt.episode || !hints.SeasonKnown {
				t.Fatalf("hints = %+v, want season %d episode %d", hints, tt.season, tt.episode)
			}
		})
	}
}

func TestEpisodeMarkersAcceptSingleLetterParts(t *testing.T) {
	for _, name := range []string{"Example Show S03E01a", "Example Show S03E01B 1080p", "Example Show 3x01a", "E01a"} {
		t.Run(name, func(t *testing.T) {
			token, ok := parseEpisodeToken(name, []string{"Example Show", "Season 3"}, true, true)
			if !ok || token.season != 3 || token.episode != 1 || token.episodeEnd != 0 {
				t.Fatalf("token = %+v, ok=%v; want season 3 episode 1 without a range", token, ok)
			}
		})
	}
	for _, name := range []string{"Example Show S03E01another", "Example Show 3x01bonus", "E01abc", "E01v2video"} {
		t.Run(name, func(t *testing.T) {
			token, _ := parseEpisodeToken(name, []string{"Example Show", "Season 3"}, true, true)
			if token.episode != 0 {
				t.Fatalf("word suffix acquired episode coordinates: %+v", token)
			}
		})
	}
}

func TestDatedSeriesFoldersPreserveCoordinateLikeTitles(t *testing.T) {
	for _, title := range []string{"Example 4x4 Adventure Club", "Example S-245"} {
		file := "/tv/" + title + " (2022) {tvdb-12345}/Season 01/" + title + " (2022) - S01E02.mkv"
		hints := ParseFilename(file, "series", "/tv")
		if hints.Title != title || hints.Year != 2022 || hints.SeasonNum != 1 || hints.EpisodeNum != 2 {
			t.Fatalf("%s: %+v", title, hints)
		}
	}
}

func TestDatedEpisodeTitleCoordinateDoesNotReplaceAirDate(t *testing.T) {
	for _, tt := range []struct {
		file            string
		season, episode int
	}{
		// An NxM inside the episode title is title text; the air date and
		// the season folder identify the episode.
		{"Season 21/Example Court (1996) - 2016-10-25 - Salon Fail!; 2x4 Vandalism Victim - [HDTV-1080p].mkv", 21, 0},
		{"Season 21/Example Court (1996) - 2016-10-25 - The 3x5 Card.mkv", 21, 0},
		// A coordinate that directly follows the date is the episode.
		{"Season 2024/Example Court (1996) - 2024-10-01 - 2024x246 - [WEBDL-1080p].mp4", 2024, 246},
		{"Season 04/Example Court (1996) - 2024-09-18 - 4x221 - [WEBDL-480p].mkv", 4, 221},
	} {
		hints := ParseFilename("/tv/Example Court (1996) {tvdb-12345}/"+tt.file, "series", "/tv")
		if hints.SeasonNum != tt.season || hints.EpisodeNum != tt.episode || hints.AirDate == "" {
			t.Fatalf("%s: %+v", tt.file, hints)
		}
	}
}

func TestYearSeasonCoordinateIsNotPictureSize(t *testing.T) {
	for _, tt := range []struct {
		name            string
		season, episode int
		ok              bool
	}{
		{"Example News - 2024x246", 2024, 246, true},
		{"Example News - 1999x366", 1999, 366, true},
		{"Example Show 1920x1080", 0, 0, false},
		{"Example Show 2048x1080", 0, 0, false},
		{"Example Show 720x480", 0, 0, false},
		{"Example Show 128x96", 0, 0, false},
		{"Example Show 12x96", 12, 96, true},
	} {
		token, ok := parseEpisodeToken(tt.name, nil, false)
		if ok != tt.ok || token.season != tt.season || token.episode != tt.episode {
			t.Fatalf("%s: ok=%v %+v", tt.name, ok, token)
		}
	}
}

func TestBareAspectRatioNeedsSeriesContext(t *testing.T) {
	for _, tt := range []struct {
		path, libraryType string
		kind              string
		season, episode   int
	}{
		{"/mixed/Movie.Name.16x9.mkv", "mixed", "movie", 0, 0},
		{"/mixed/Movie Name (2001)/Movie.Name.4x3.mkv", "mixed", "movie", 0, 0},
		{"/mixed/Movie.Name.21x9.mkv", "mixed", "movie", 0, 0},
		{"/mixed/Example Show/Season 16/Example.Show.16x9.mkv", "mixed", "series", 16, 9},
		{"/series/Example Show/Example.Show.16x9.mkv", "series", "series", 16, 9},
	} {
		root := "/" + tt.libraryType
		hints := ParseFilename(tt.path, tt.libraryType, root)
		if hints.Type != tt.kind || hints.SeasonNum != tt.season || hints.EpisodeNum != tt.episode {
			t.Fatalf("%s: %+v", tt.path, hints)
		}
	}
}

func TestShowTitleNumberIsNotTrailingEpisode(t *testing.T) {
	for _, tt := range []struct {
		path    string
		episode int
	}{
		{"/tv/The 100/Season 01/The 100.mkv", 0},
		{"/tv/Room 104/Season 01/Room 104.mkv", 0},
		{"/tv/The 100/Season 01/The 100 05.mkv", 5},
		{"/tv/The 100/Season 01/The 100 - 05.mkv", 5},
		{"/tv/The 100/Season 01/The 100 - 05 - Title.mkv", 5},
		{"/tv/Example Show/Season 01/Example Show 07.mkv", 7},
		{"/tv/Area 51/Season 01/Area 51 - 51.mkv", 51},
	} {
		hints := ParseFilename(tt.path, "series", "/tv")
		if hints.SeasonNum != 1 || hints.EpisodeNum != tt.episode {
			t.Fatalf("%s: %+v, want episode %d", tt.path, hints, tt.episode)
		}
	}
}

func TestUndatedFolderKeepsFormatWordTitles(t *testing.T) {
	for _, tt := range []struct{ path, title string }{
		{"/movies/Mr Holland's Opus/Mr Holland's Opus.mkv", "Mr Holland's Opus"},
		{"/movies/The UHD Journey/The UHD Journey.mkv", "The UHD Journey"},
		{"/movies/Example Movie/Example Movie 1080p BluRay.mkv", "Example Movie"},
	} {
		hints := ParseFilename(tt.path, "movies", "/movies")
		if hints.Type != "movie" || hints.Title != tt.title {
			t.Fatalf("%s: %+v, want title %q", tt.path, hints, tt.title)
		}
	}
	hints := ParseFilename("/tv/Show/Season 1/Show.128x96.E02.mkv", "series", "/tv")
	if hints.SeasonNum != 1 || hints.EpisodeNum != 2 {
		t.Fatalf("picture size replaced the episode marker: %+v", hints)
	}
	if hints := ParseFilename("/mixed/Movie.Name.128x96.3gp", "mixed", "/mixed"); hints.Type != "movie" {
		t.Fatalf("picture size classified a movie as a series: %+v", hints)
	}
}

func TestSeriesFilenameCorroboratesMissingFolderYear(t *testing.T) {
	for _, filename := range []string{"Example Show (1994) - S01E02", "Another Show (1994) - S01E02"} {
		hints := ParseFilename("/tv/Example Show {tvdb-12345}/Season 01/"+filename+".mkv", "series", "/tv")
		wantYear := 0
		if filename == "Example Show (1994) - S01E02" {
			wantYear = 1994
		}
		if hints.Title != "Example Show" || hints.Year != wantYear {
			t.Fatalf("%s: %+v", filename, hints)
		}
	}
}

func TestEpisodeRangeEndMarker(t *testing.T) {
	for _, tt := range []struct {
		name string
		end  int
	}{
		{"Example.Show.S01E37-E40END.1080p", 40},
		{"Example.Show.S01E37-E40end", 40},
		{"Example.Show.S01E37-E40Ending", 0},
	} {
		token, _ := parseEpisodeToken(tt.name, nil, false)
		if token.episodeEnd != tt.end {
			t.Fatalf("%s: %+v", tt.name, token)
		}
	}
}

func TestSeriesFilenameYearWithLigatureSpelling(t *testing.T) {
	for _, tt := range []struct {
		folder, file string
		year         int
	}{
		{"Aeon Example", "Æon Example (2019)", 2019},
		{"Cœur Example", "Coeur Example (2019)", 2019},
		{"Aeon Example", "Æon Another (2019)", 0},
	} {
		hints := ParseFilename("/tv/"+tt.folder+"/Season 01/"+tt.file+" - S01E03.mkv", "series", "/tv")
		if hints.Title != tt.folder || hints.Year != tt.year {
			t.Fatalf("%+v: %+v", tt, hints)
		}
	}
}

func TestDatedSeriesReleasePackSuppliesSeason(t *testing.T) {
	for _, folder := range []string{"Example.Show.(2020).S02.COMPLETE", "Example Show (2020) S02E01-E03"} {
		hints := ParseFilename("/tv/"+folder+"/E03.mkv", "series", "/tv")
		if hints.Title != "Example Show" || hints.Year != 2020 || hints.SeasonNum != 2 || hints.EpisodeNum != 3 || !hints.SeasonKnown {
			t.Fatalf("%s: %+v", folder, hints)
		}
	}
}

func TestSeriesSeasonFilenameCorroboratesBareReleaseYear(t *testing.T) {
	for _, tt := range []struct {
		folder, file string
		year         int
	}{
		{"Example Show", "Example.Show.2009.S01E01", 2009},
		{"Example Show {tvdb-12345}", "Example.Show.2009.S01E01", 2009},
		{"Space 1999", "Space.1999.S01E01", 0},
		{"Example Show", "Another.Show.2009.S01E01", 0},
	} {
		hints := ParseFilename("/tv/"+tt.folder+"/Season 01/"+tt.file+".mkv", "series", "/tv")
		if hints.Year != tt.year {
			t.Fatalf("%+v: %+v", tt, hints)
		}
	}
}

func TestXEpisodeRangesDoNotConsumeCodecSuffixes(t *testing.T) {
	for _, tt := range []struct {
		name string
		end  int
	}{
		{"Example.Show.1x02-x264", 0},
		{"Example.Show.1x02-x265", 0},
		{"Example Show 1x02 x264", 0},
		{"Example Show 1x02 - x265", 0},
		{"Example.Show.1x02_x264", 0},
		{"Example.Show.1x02-XviD", 0},
		{"Example.Show.1x02x03-x264", 3},
		{"Example.Show.1x02-x03", 3},
		{"Example.Show.1x02-1x264", 264},
		{"Example.Show.1x263x264", 264},
	} {
		t.Run(tt.name, func(t *testing.T) {
			filePath := "/tv/Example Show/Season 1/" + tt.name + ".mkv"
			hints := ParseVariantHints(filePath, "series", "/tv")
			if hints.MultiEpisodeEnd != tt.end {
				t.Fatalf("codec changed episode range: %+v, want end %d", hints, tt.end)
			}
		})
	}
}

func TestDelimitedEpisodeNumberIgnoresTitleNumbers(t *testing.T) {
	for _, tt := range []struct {
		path    string
		season  int
		episode int
		known   bool
	}{
		{"/tv/Example Show/Season 1/Example Show - 05 - Part 2.mkv", 1, 5, true},
		{"/tv/Example Show/Season 4/Example Show - 12 - The Example Part 1.mkv", 4, 12, true},
		{"/tv/Example Show/Season 1/Example Show - 02 - 24 Hours.mkv", 1, 2, true},
		{"/tv/Example Show/Season 1/Example Show - 06 - Area 51.mkv", 1, 6, true},
		{"/tv/90 Day Example/Season 1/90 Day Example - 01 - Title.mkv", 1, 1, true},
		{"/tv/9-1-1/Season 1/9-1-1 - 01 - Pilot.mkv", 1, 1, true},
		{"/tv/13 Example Reasons/Season 1/13 Example Reasons - 01.mkv", 1, 1, true},
		{"/tv/90 Day Example/Season 1/90 Day Example 01.mkv", 1, 1, true},
		{"/tv/Example Show/Season 1/01 - Title.mkv", 1, 1, true},
		{"/tv/24/Season 1/24 - 05 - Part 2.mkv", 1, 5, true},
		{"/tv/24/Season 1/24 - 24 - 11-00 PM.mkv", 1, 24, true},
		{"/tv/24/Season 1/24 - Title.mkv", 1, 24, true},
		{"/tv/12 Example Monkeys/Season 1/12 - Title.mkv", 1, 12, true},
		{"/tv/13 Example Reasons/Season 1/13 - Tape 7 Side A.mkv", 1, 13, true},
		{"/tv/Mission - 3/Mission - 3 - 05.mkv", 0, 5, false},
		{"/tv/Example Show/Season 2/Example Show - 2 - 05.mkv", 2, 5, true},
		{"/tv/24/Season 1/24 - 12-00 AM.mkv", 1, 24, true},
		{"/tv/24/Season 1/24 - 12 00 AM.mkv", 1, 24, true},
		{"/tv/24/Season 1/24 - 11-00 PM - 12-00 AM.mkv", 1, 24, true},
		{"/tv/24/Season 1/24 - 05-06 - Title.mkv", 1, 5, true},
		{"/tv/24 - 01.mkv", 0, 1, false},
		{"/tv/Example Show - 05 - Part 2.mkv", 0, 5, false},
		{"/tv/Example Show/[Group] Example Show - 07v2 [1080p].mkv", 0, 7, false},
	} {
		t.Run(tt.path, func(t *testing.T) {
			hints := ParseFilename(tt.path, "series", "/tv")
			if hints.SeasonNum != tt.season || hints.EpisodeNum != tt.episode || hints.SeasonKnown != tt.known {
				t.Fatalf("hints = %+v, want season %d (known %v) episode %d", hints, tt.season, tt.known, tt.episode)
			}
		})
	}
	token, ok := parseEpisodeToken("Example Show - 05-06 - Title", []string{"Example Show", "Season 1"}, true, true)
	if !ok || token.episode != 5 || token.episodeEnd != 6 {
		t.Fatalf("delimited range = %+v, ok=%v; want episodes 5-6", token, ok)
	}
}

func TestXCoordinatesRejectAudioLayoutsAndDimensions(t *testing.T) {
	for _, tt := range []struct {
		path    string
		season  int
		episode int
		known   bool
	}{
		{"/tv/Example Show/Season 1/[Group] Example Show - 05 [1080p AAC 2.0x2].mkv", 1, 5, true},
		{"/tv/Example Show/Season 1/[Group] Example Show - 05 [DTS 5.1x2].mkv", 1, 5, true},
		{"/tv/Example Show/[Group] Example Show - 01 [2048x1080].mkv", 0, 1, false},
		{"/tv/Example Show/Example Show - 2009x03 - Title.mkv", 2009, 3, true},
		{"/tv/Incoming/Example.Show.2019.1x02.mkv", 1, 2, true},
		{"/tv/Example 5/Example.5.1x01.Title.mkv", 1, 1, true},
		{"/tv/Example SG-1/Example.SG-1.2x05.mkv", 2, 5, true},
		{"/tv/Example Show/Season 1/[Group] Example Show - 02 [16x9 1080p].mkv", 1, 2, true},
		{"/tv/Example Show/Season 1/Example.Show.1920x1080.E02.mkv", 1, 2, true},
		{"/tv/Example Show/Example Show - 4x3 - Title.mkv", 4, 3, true},
		{"/tv/Example Show/Season 1/Example.Show.16x9.E02.mkv", 1, 2, true},
		{"/tv/Example Show/Season 1/Example.Show.AAC.2.0x2.E03.mkv", 1, 3, true},
		{"/tv/Example Show/Season 1/Example.Show.DD5.1x264.E03.mkv", 1, 3, true},
		{"/tv/Example Show/Season 1/Example.Show.2.35x1.E02.mkv", 1, 2, true},
		{"/tv/Example Show/Season 1/Example.Show.3x2.E02.mkv", 1, 2, true},
		{"/tv/Example Show/Season 1/Example.Show.176x144.E02.mkv", 1, 2, true},
		{"/tv/Example Opus/Season 1/Example Opus - 02.mkv", 1, 2, true},
		{"/tv/Opus.Example/Season 1/Opus.Example - 02 [1080p FLAC].mkv", 1, 2, true},
	} {
		t.Run(tt.path, func(t *testing.T) {
			hints := ParseFilename(tt.path, "series", "/tv")
			if hints.SeasonNum != tt.season || hints.EpisodeNum != tt.episode || hints.SeasonKnown != tt.known {
				t.Fatalf("hints = %+v, want season %d (known %v) episode %d", hints, tt.season, tt.known, tt.episode)
			}
		})
	}
	for _, path := range []string{
		"/mixed/Example.Movie.2019.1080p.BluRay.DTS.5.1x2.mkv",
		"/mixed/Example Movie (2019)/Example.Movie.2019.1080p.BluRay.DD5.1x264-GRP.mkv",
		"/mixed/Example Movie/Example.Movie.2048x1080.mkv",
		"/mixed/Example Movie/Example.Movie.1998x1080.mkv",
		"/mixed/10x10 (2018).mkv",
		"/mixed/8x10 Example (2009).mkv",
		"/mixed/4x4.2019.1080p.mkv",
		"/mixed/10x10 - 2018.mkv",
		"/mixed/Example.Movie.176x144.3gp",
		"/mixed/Example.Movie.16x9.1080p.mkv",
		"/mixed/Example.Movie.4x3.DVDRip.mkv",
	} {
		t.Run(path, func(t *testing.T) {
			if ctx := ResolvePathContext(path, "mixed", "/mixed"); ctx.Type != "movie" || ctx.HasEpisodePattern {
				t.Fatalf("technical token classified a movie as episodic: %+v", ctx)
			}
		})
	}
}

func TestMixedLibraryDatedShowFolderWithEpisodesIsSeries(t *testing.T) {
	paths := []string{
		"/mixed/Example Show (2005)/Example Show (2005) - S01E01 - Pilot.mkv",
		"/mixed/Example Show (2005)/Example Show (2005) - S01E02 - Second.mkv",
	}
	_, assignments := InferRootAssignments(paths, "mixed", 1, nil, "/mixed")
	for _, path := range paths {
		if got := assignments[path]; got.InferredType != "series" || got.Title != "Example Show" || got.Year != 2005 {
			t.Fatalf("%s: assignment = %+v; want series Example Show (2005)", path, got)
		}
		if hints := ParseFilename(path, "mixed", "/mixed"); hints.Type != "series" || hints.SeasonNum != 1 || hints.EpisodeNum == 0 {
			t.Fatalf("%s: filename hints = %+v; want a season 1 series episode", path, hints)
		}
	}
}

func TestLibraryRootBoundsSeasonDirectoryLabels(t *testing.T) {
	const root = "/mnt/s3/movies"
	if IsMisplacedSeriesFile(root+"/Example Movie (2019)/Example.Movie.2019.S01E43.1080p.mkv", root) {
		t.Fatal("a mount directory above the library was read as a season folder")
	}
	if !IsMisplacedSeriesFile(root+"/Example Pack/Season 01/Example.Show.S01E01.mkv", root) {
		t.Fatal("a season folder inside the library was not detected")
	}
	series, ok := DetectSeriesRoot("/mnt/s3/tv/Example Show/Example Show - S01E01.mkv", "series", "/mnt/s3/tv")
	if !ok || series.RootPath != "/mnt/s3/tv/Example Show" {
		t.Fatalf("series root = %+v, %v; want the show folder", series, ok)
	}
}

func TestSeasonEpisodeDashFormStaysAtTheStart(t *testing.T) {
	for _, tt := range []struct {
		path            string
		season, episode int
	}{
		{"/tv/Example Show/1-05 - Title.mkv", 1, 5},
		{"/tv/Example Show/Season 1/Example Show - 01 - 7-11 Heist.mkv", 1, 1},
		{"/tv/Example Show/Season 2/Example Show - 02 - 1-00 A.M.-2-00 A.M..mkv", 2, 2},
		{"/tv/Example Show/Season 1/01 - 2-00 PM.mkv", 1, 1},
	} {
		hints := ParseFilename(tt.path, "series", "/tv")
		if hints.SeasonNum != tt.season || hints.EpisodeNum != tt.episode || !hints.SeasonKnown {
			t.Fatalf("%s: hints = %+v, want season %d episode %d", tt.path, hints, tt.season, tt.episode)
		}
		if variants := ParseVariantHints(tt.path, "series", "/tv"); variants.PresentationKind == "multi_episode" {
			t.Fatalf("%s: episode title became a range: %+v", tt.path, variants)
		}
	}
}

func TestExplicitMovieYearKeepsFormatWordsInTitle(t *testing.T) {
	for _, tt := range []struct{ path, title string }{
		{"/movies/Mr. Example's Opus (1995).mkv", "Mr. Example's Opus"},
		{"/movies/The UHD Journey (2014).mkv", "The UHD Journey"},
		{"/movies/Example 4K Story (2020).mkv", "Example 4K Story"},
		{"/movies/Mr.Examples.Opus.1995.mkv", "Mr Examples Opus"},
		{"/movies/The.UHD.Journey.2014.1080p.BluRay.mkv", "The UHD Journey"},
	} {
		hints := ParseFilename(tt.path, "movies", "/movies")
		if hints.Title != tt.title || hints.Year == 0 {
			t.Fatalf("%s: hints = %+v, want %q with its year", tt.path, hints, tt.title)
		}
	}
	for _, path := range []string{"/movies/Example.Movie.2019.1080p.BluRay.x264-GRP.mkv", "/movies/Example.Movie.4K.HDR.2160p.2019.mkv"} {
		if hints := ParseFilename(path, "movies", "/movies"); hints.Title != "Example Movie" {
			t.Fatalf("%s: release name hints = %+v", path, hints)
		}
	}
}
