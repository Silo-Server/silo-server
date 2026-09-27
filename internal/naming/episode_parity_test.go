package naming

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixture records public filename expectations, not Jellyfin's parsing
// implementation. Keep its immutable upstream reference when extending it.
func TestEpisodeNamingJellyfinExamples(t *testing.T) {
	data, err := os.ReadFile("testdata/jellyfin_tv.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Path        string
			Category    string
			SeriesTitle *string `json:"series_title"`
			Want        map[string]int
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Category+"/"+tc.Path, func(t *testing.T) {
			stem := strings.TrimSuffix(filepath.Base(tc.Path), filepath.Ext(tc.Path))
			dirs := strings.Split(filepath.ToSlash(filepath.Dir(tc.Path)), "/")
			token, _ := parseEpisodeToken(stem, dirs, true, true)
			if tc.SeriesTitle != nil && !strings.EqualFold(token.seriesTitle, *tc.SeriesTitle) {
				t.Errorf("series title = %q, want %q", token.seriesTitle, *tc.SeriesTitle)
			}
			for key, want := range tc.Want {
				var got int
				switch key {
				case "season":
					got = token.season
				case "episode":
					got = token.episode
				case "end":
					got = token.episodeEnd
				default:
					t.Fatalf("unknown fixture assertion %q", key)
				}
				if got != want {
					t.Errorf("%s = %d, want %d; parsed %+v", key, got, want, token)
				}
			}
			if want, hasRangeAssertion := tc.Want["end"]; hasRangeAssertion {
				if hints := ParseVariantHints(tc.Path, "series"); hints.MultiEpisodeEnd != want {
					t.Errorf("range hint = %d, want %d", hints.MultiEpisodeEnd, want)
				}
			}
		})
	}
}

func TestEpisodeNamingSeriesTitle(t *testing.T) {
	for _, tc := range []struct {
		name, title string
		episode     int
	}{
		{"anything_s01e02", "anything", 2},
		{"anything_102", "anything", 2},
		{"The Walking Dead 4x01", "The Walking Dead", 1},
		{"the_simpsons-s02e01_18536", "the simpsons", 1},
		{"S01E02 foo", "", 2},
		{"4x12 - The Woman", "", 12},
		{"LA X, Pt. 1_s06e32", "LA X, Pt. 1", 32},
		{"[Baz-Bar] Foo - 05 [1080p][Multiple Subtitle]", "Foo", 5},
		{"[YuiSubs] Tensura Nikki - Tensei Shitara Slime Datta Ken - 12 (NVENC H.265 1080p)", "Tensura Nikki - Tensei Shitara Slime Datta Ken", 12},
		{"[CASO&Sumisora][Oda_Nobuna_no_Yabou][04][BDRIP][1920x1080][x264_AAC][7620E503]", "Oda Nobuna no Yabou", 4},
		{"[HorribleSubs] Made in Abyss - 13 [720p]", "Made in Abyss", 13},
		{"[Erai-raws] Jujutsu Kaisen - 03 [720p][Multiple Subtitle]", "Jujutsu Kaisen", 3},
		{"[tvN] 혼술남녀.E01-E16.720p-NEXT", "혼술남녀", 1},
		{"[tvN] 연애말고 결혼 E01~E16 END HDTV.H264.720p-WITH", "연애말고 결혼", 1},
		{"[Group] 86 - 03 [1080p]", "86", 3},
		{"Space 1999 S01E01", "Space 1999", 1},
		{"[Group] Show - 12 [1080p AAC5.1]", "Show", 12},
		{"Show - 12 AAC5.1", "Show", 12},
		{"Show - 12 H.265", "Show", 12},
		{"[Group] Show - 12v2 [1080p]", "Show", 12},
		{"Show S01E12v2", "Show", 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, ok := parseEpisodeToken(tc.name, nil, true, true)
			if !ok || token.seriesTitle != tc.title || token.episode != tc.episode {
				t.Errorf("parsed %+v, %t; want title %q episode %d", token, ok, tc.title, tc.episode)
			}
		})
	}
}

func TestEpisodeNamingDoesNotInventCoordinates(t *testing.T) {
	for _, name := range []string{
		"1920x1080", "720x480", "360x240", "1080p", "1080p H.265 AAC5.1",
		"1920x1080-E15", "999x03-E15", "Show.03.04.2020", "Show 03-04-2020", "2001 - A Space Odyssey", "Space 1999",
		"Show E02video", "Show E02v2video", "Show [1920x1080] [AAC5.1]", "Show H.265 AAC5.1", "Show S01E02foo",
	} {
		t.Run(name, func(t *testing.T) {
			if token, _ := parseEpisodeToken(name, []string{"Show", "Season 1"}, true, true); token.episode != 0 {
				t.Fatalf("unexpected episode coordinates: %+v", token)
			}
		})
	}
	for _, folder := range []string{"Extras", "Trailers", "Shorts", "Clips", "Other", "Teasers", "Scenes", "BehindTheScenes", "DeletedScenes", "Behind_the_scenes", "Interviews"} {
		t.Run(folder, func(t *testing.T) {
			if token, _ := parseEpisodeToken("E02 - Bonus", []string{"Show", "Season 1", folder}, true, true); token.episode != 0 {
				t.Fatalf("extra acquired episode coordinates: %+v", token)
			}
		})
	}
	for _, name := range []string{"E02", "02", "Show - 12", "[Group] Show - 12 [720p]", "Show_102"} {
		if token, _ := parseEpisodeToken(name, []string{"movies"}, false); token.episode != 0 {
			t.Errorf("%q acquired episode coordinates without series context: %+v", name, token)
		}
	}
}

func TestEpisodeNamingKnownSeason(t *testing.T) {
	for _, tc := range []struct {
		name, parent string
		season       int
		known        bool
	}{
		{"Show S00E02", "Show", 0, true},
		{"E02", "Specials", 0, true},
		{"E02", "Season 2", 2, true},
		{"E02", "Staffel 2", 2, true},
		{"Show - 12", "Show", 0, false},
		{"E02", "Show", 0, false},
		{"Show_102", "Show", 1, true},
	} {
		t.Run(tc.name+"/"+tc.parent, func(t *testing.T) {
			token, _ := parseEpisodeToken(tc.name, []string{tc.parent}, true, true)
			if token.season != tc.season || token.seasonKnown != tc.known {
				t.Errorf("parsed %+v; want season %d, known %t", token, tc.season, tc.known)
			}
		})
	}
}

func TestEpisodeNamingRangeTitleSuffix(t *testing.T) {
	for _, name := range []string{
		"S01E02E03E04 - Arrival.mkv", "S01E02v2-E04v2 - Arrival.mkv", "1x02x03x04 - Arrival.mkv",
		"01x02 - 01x03 - 01x04 - Arrival.mkv", "02-04 - Arrival.mkv",
	} {
		if got := EpisodeTitleSuffix(filepath.Join("Show", "Season 1", name)); got != "Arrival" {
			t.Errorf("%q: suffix = %q, want Arrival", name, got)
		}
	}
}

func TestEpisodeNamingDoesNotTreatReleaseIDsAsRanges(t *testing.T) {
	for _, name := range []string{"the_simpsons-s02e01_18536", "Show S01E01_1234", "Show S01E01-1080p", "Show S01E01 The 6-10 to Lubbock"} {
		token, _ := parseEpisodeToken(name, nil, true, true)
		if token.episodeEnd != 0 {
			t.Errorf("%q: unexpected range end %d", name, token.episodeEnd)
		}
	}
}

func TestEpisodeNamingRangeNumberBounds(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end int
	}{
		{"Show S23E1162-E1163", 1162, 1163},
		{"Show S01E12345-E12346", 12345, 12346},
		{"Show S01E12345-E123456", 12345, 0},
		{"Show S01E123456-E123457", 0, 0},
		{"Show S01E02-E03p", 2, 0},
		{"Show S01E02-E03x264", 2, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, _ := parseEpisodeToken(tc.name, nil, true, true)
			if token.episode != tc.start || token.episodeEnd != tc.end {
				t.Errorf("parsed %+v; want episodes %d-%d", token, tc.start, tc.end)
			}
		})
	}
}

func TestEpisodeNamingCompactCodeRespectsContainingSeason(t *testing.T) {
	for _, tc := range []struct {
		path            string
		season, episode int
	}{
		{"/tv/One Piece/Season 21/301.mkv", 21, 301},
		{"/tv/One Piece/Season 21/One.Piece.301.mkv", 21, 301},
		{"/tv/Example Show/Season 2/105 - Pilot.mkv", 2, 105},
		{"/tv/Example Show/Staffel 2/105 - Pilot.mkv", 2, 105},
		{"/tv/Example Show/Specials/105.mkv", 0, 105},
		{"/tv/Example Show/Season 3/301.mkv", 3, 1},
		{"/tv/Example Show/301.mkv", 3, 1},
		{"/tv/Example Show/Season 2/S01E05.mkv", 1, 5},
	} {
		t.Run(tc.path, func(t *testing.T) {
			hints := ParseFilename(tc.path, "series")
			if hints.SeasonNum != tc.season || hints.EpisodeNum != tc.episode || !hints.SeasonKnown {
				t.Errorf("parsed %+v; want known season %d episode %d", hints, tc.season, tc.episode)
			}
		})
	}
}

func TestEpisodeNamingCompactRanges(t *testing.T) {
	for _, tc := range []struct {
		path               string
		season, start, end int
	}{
		{"/tv/Show/Show.301-305.mkv", 3, 1, 5},
		{"/tv/Show/Show.301-302-305.mkv", 3, 1, 5},
		{"/tv/Show/Show.301-E305.mkv", 3, 1, 305},
		{"/tv/Show/Show.301-405.mkv", 3, 1, 0},
		{"/tv/Show/Season 21/301-305.mkv", 21, 301, 305},
	} {
		t.Run(tc.path, func(t *testing.T) {
			parsed := ParseFilename(tc.path, "series")
			variant := ParseVariantHints(tc.path, "series")
			if parsed.SeasonNum != tc.season || parsed.EpisodeNum != tc.start || variant.MultiEpisodeEnd != tc.end {
				t.Errorf("parsed=%+v, variant=%+v; want season%d episodes%d-%d", parsed, variant, tc.season, tc.start, tc.end)
			}
		})
	}
}
