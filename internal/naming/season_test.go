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
			got, ok := seasonDirectoryNumber(filepath.Base(tt.path), tt.parent, tt.allowNumeric)
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
			if got, ok := seasonDirectoryNumber(tt.segment, tt.parent, true); ok {
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
			got, ok := seasonDirectoryNumber(tt.segment, tt.parent, true)
			if got != tt.season || !ok {
				t.Fatalf("seasonDirectoryNumber = %d, %v; want %d, true", got, ok, tt.season)
			}
		})
	}
}
