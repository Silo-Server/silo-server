package naming

import "testing"

func TestParseExtraSuffix(t *testing.T) {
	cases := []struct {
		name     string
		wantKind string
		wantOK   bool
	}{
		{"Movie (2020)-trailer.mkv", "trailer", true},
		{"Movie (2020)-Trailer.mkv", "trailer", true},
		{"Movie.2020.behindthescenes.mkv", "behind_the_scenes", true},
		{"Movie-deleted.mkv", "deleted_scene", true},
		{"Movie-featurette.mp4", "featurette", true},
		{"Movie-interview.mkv", "other", true},
		{"Movie-short.mkv", "other", true},
		// The token must be the final suffix component.
		{"Trailer Park Boys S01E01.mkv", "", false},
		{"The Deleted (2016).mkv", "", false},
		{"Movie (2020).mkv", "", false},
		// A bare token with no preceding title is not a suffix match.
		{"trailer.mkv", "", false},
		{"-trailer.mkv", "", false},
	}
	for _, tc := range cases {
		kind, ok := ParseExtraSuffix(tc.name)
		if ok != tc.wantOK || kind != tc.wantKind {
			t.Errorf("ParseExtraSuffix(%q) = (%q, %v), want (%q, %v)",
				tc.name, kind, ok, tc.wantKind, tc.wantOK)
		}
	}
}

func TestExtraTitleFromFile(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/lib/Movie (2020)/Movie (2020)-trailer.mkv", "Movie (2020)"},
		{"/lib/Movie (2020)/Extras/Making Of.mkv", "Making Of"},
		{"/lib/Movie/Extras/behind.the.scenes_reel.mkv", "behind the scenes reel"},
	}
	for _, tc := range cases {
		if got := ExtraTitleFromFile(tc.path); got != tc.want {
			t.Errorf("ExtraTitleFromFile(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestSeriesExtrasFolderBelongsToShow(t *testing.T) {
	cases := []struct {
		path        string
		wantRoot    string
		wantTitle   string
		wantYear    int
		wantEpisode int
	}{
		{"/tv/Show/other/Show - S00E01 - Making of.mkv", "/tv/Show", "Show", 0, 1},
		{"/tv/Show/other/Behind the camera.mkv", "/tv/Show", "Show", 0, 0},
		{"/tv/Show (2021)/trailers/Show trailer.mkv", "/tv/Show (2021)", "Show", 2021, 0},
		{"/tv/Show/Featurettes/Show S00E02 Featurette.mkv", "/tv/Show", "Show", 0, 2},
		{"/tv/Show/Extras/Behind The Scenes/clip.mkv", "/tv/Show", "Show", 0, 0},
		{"/tv/Show/Season 01/Trailers/Preview.mkv", "/tv/Show", "Show", 0, 0},
		{"/tv/Show/Season 01/Extras/Show S01E05 Deleted.mkv", "/tv/Show", "Show", 0, 5},
		{"/tv/Show/Season 01/Specials/Show S00E05.mkv", "/tv/Show", "Show", 0, 5},
		{"/tv/Show/Specials/Show S00E06.mkv", "/tv/Show", "Show", 0, 6},
		{"/tv/Show/other/Extras/Show.S00E01.mkv", "/tv/Show", "Show", 0, 1},
		{"/tv/Show/other/Specials/Show.S00E02.mkv", "/tv/Show", "Show", 0, 2},
		// Extras words directly in the library folder, or named by the file's
		// own episode title, name a show or an organizational folder.
		{"/tv/Other/Other - S01E01.mkv", "/tv/Other", "Other", 0, 1},
		{"/tv/Trailers/Season 01/Trailers - S01E01.mkv", "/tv/Trailers", "Trailers", 0, 1},
		{"/tv/other/Flat Show/pilot.mkv", "/tv/other/Flat Show", "Flat Show", 0, 0},
		{"/tv/Documentaries/Interviews/Interviews S01E01.mkv", "/tv/Documentaries/Interviews", "Interviews", 0, 1},
		{"/tv/Documentaries/Interviews/Interviews E02.mkv", "/tv/Documentaries/Interviews", "Interviews", 0, 0},
		{"/tv/Kids/Shorts/Shorts.2024-10-01.mkv", "/tv/Kids/Shorts", "Shorts", 0, 0},
		{"/tv/Kids/Clips/Clips.2024-10-01.mkv", "/tv/Kids/Clips", "Clips", 0, 0},
		// The folder holding Specials/ or Extras/ is the show when its name is
		// a bare number or a Specials/Extras label: those are show titles.
		{"/tv/Drama/1883/Specials/1883 S00E01.mkv", "/tv/Drama/1883", "1883", 0, 1},
		{"/tv/Drama/24/Extras/24 S00E02.mkv", "/tv/Drama/24", "24", 0, 2},
		{"/tv/Extras/Specials/Extras S00E01.mkv", "/tv/Extras", "Extras", 0, 1},
		{"/tv/Comedy/Extras/Specials/Extras S00E01.mkv", "/tv/Comedy/Extras", "Extras", 0, 1},
	}
	for _, tc := range cases {
		ctx := ResolvePathContext(tc.path, "series", "/tv")
		if ctx.RootPath != tc.wantRoot || ctx.Title != tc.wantTitle || ctx.Year != tc.wantYear || ctx.EpisodeNum != tc.wantEpisode {
			t.Errorf("ResolvePathContext(%q) = root %q title %q year %d episode %d, want root %q title %q year %d episode %d",
				tc.path, ctx.RootPath, ctx.Title, ctx.Year, ctx.EpisodeNum, tc.wantRoot, tc.wantTitle, tc.wantYear, tc.wantEpisode)
		}
	}
}

func TestExtrasDirKind(t *testing.T) {
	for name, want := range map[string]bool{
		"Trailers": true, "behind.the.scenes": true, "Deleted_Scenes": true, "other": true,
		"others": false, "Season 01": false, "Specials": false,
	} {
		if _, got := ExtrasDirKind(name); got != want {
			t.Errorf("ExtrasDirKind(%q) = %v, want %v", name, got, want)
		}
	}
}
