package naming

import "testing"

func TestSeriesFilenameIdentity(t *testing.T) {
	for _, tt := range []struct {
		path, title, root string
		year              int
	}{
		{"/tv/downloads/Show.One.S01E01.mkv", "Show One", "/tv/downloads/Show.One.S01E01.mkv", 0},
		{"/tv/downloads/Show.Two.S01E01.mkv", "Show Two", "/tv/downloads/Show.Two.S01E01.mkv", 0},
		{"/tv/downloads/Show.One.(2020).S01E01.mkv", "Show One", "/tv/downloads/Show.One.(2020).S01E01.mkv", 2020},
		{"/tv/Show One/Show.One.S01E01.mkv", "Show One", "/tv/Show One", 0},
		{"/tv/Show.One.S01.1080p.WEB-DL/Show.One.S01E01.mkv", "Show One", "/tv/Show.One.S01.1080p.WEB-DL", 0},
		{"/tv/Show.One.S01E01.1080p.WEB-DL/Show.One.S01E01.mkv", "Show One", "/tv/Show.One.S01E01.1080p.WEB-DL", 0},
		{"/tv/Show One (2020)/Alternate Name S01E01.mkv", "Show One", "/tv/Show One (2020)", 2020},
		{"/tv/Show One [tvdbid=12345]/Alternate Name S01E01.mkv", "Show One", "/tv/Show One [tvdbid=12345]", 0},
		{"/tv/Show One/Season 1/Episode Title S01E01.mkv", "Show One", "/tv/Show One", 0},
		{"/tv/downloads/[Group] Anime Title - 12 [1080p].mkv", "Anime Title", "/tv/downloads/[Group] Anime Title - 12 [1080p].mkv", 0},
		{"/tv/The.Show.S01/E01.mkv", "The Show", "/tv/The.Show.S01", 0},
		{"/tv/The.Show.S01.COMPLETE/E01.mkv", "The Show", "/tv/The.Show.S01.COMPLETE", 0},
		{"/tv/The_Show_Season_1/E01.mkv", "The Show", "/tv/The_Show_Season_1", 0},
		{"/tv/The Show/Staffel 1/E01.mkv", "The Show", "/tv/The Show", 0},
		{"/tv/The Show/3.Staffel/E01.mkv", "The Show", "/tv/The Show", 0},
		{"/tv/The Show/シーズン 1/E01.mkv", "The Show", "/tv/The Show", 0},
		{"/tv/The Show/The.Show.S01.PDTV/E01.mkv", "The Show", "/tv/The Show", 0},
	} {
		t.Run(tt.path, func(t *testing.T) {
			ctx := ResolvePathContext(tt.path, "series", "/tv/downloads", "/tv")
			if ctx.Title != tt.title || ctx.Year != tt.year || ctx.RootPath != tt.root {
				t.Fatalf("path context = %+v; want %q (%d), root %q", ctx, tt.title, tt.year, tt.root)
			}
			_, assignments := InferRootAssignments([]string{tt.path}, "series", 1, nil, "/tv/downloads", "/tv")
			group := InferGroupIdentity(tt.path, "series", assignments[tt.path])
			if group.BaseTitle != tt.title || group.BaseYear != tt.year || group.ObservedRootPath != tt.root || group.State != "resolved" {
				t.Fatalf("scan group = %+v", group)
			}
		})
	}
}

func TestEpisodeSeasonPresence(t *testing.T) {
	for _, tt := range []struct {
		path  string
		known bool
	}{
		{"/tv/Show/E01.mkv", false},
		{"/tv/Show/01.mkv", false},
		{"/tv/Show/Show - 12.mkv", false},
		{"/tv/Show/Specials/E01.mkv", true},
		{"/tv/Show/Show.S00E01.mkv", true},
		{"/tv/Show/Season 1/E01.mkv", true},
		{"/archive/Season 1/Show/E01.mkv", false},
	} {
		hints := ParseFilename(tt.path, "series")
		if hints.EpisodeNum == 0 || hints.SeasonKnown != tt.known {
			t.Errorf("season presence for %q = %+v; want known=%v", tt.path, hints, tt.known)
		}
	}
}
