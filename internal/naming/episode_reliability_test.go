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
