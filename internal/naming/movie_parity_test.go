package naming

import "testing"

// Expectations use public filename conventions, including Jellyfin v12.1's
// CleanStringTests and CleanDateTimeTests. Punctuation is normalized for Silo's
// metadata search rather than copied from Jellyfin's intermediate parser output.
func TestMovieReleaseNameParity(t *testing.T) {
	tests := []struct {
		filename string
		title    string
		year     int
	}{
		{"Super movie 480p.mp4", "Super movie", 0},
		{"Super movie Multi.mp4", "Super movie", 0},
		{"Super movie 480p 2001.mp4", "Super movie", 0},
		{"Super movie [480p].mp4", "Super movie", 0},
		{"Crouching.Tiger.Hidden.Dragon.4k.mkv", "Crouching Tiger Hidden Dragon", 0},
		{"Crouching.Tiger.Hidden.Dragon.UltraHD.mkv", "Crouching Tiger Hidden Dragon", 0},
		{"Crouching.Tiger.Hidden.Dragon.UHD.mkv", "Crouching Tiger Hidden Dragon", 0},
		{"Crouching.Tiger.Hidden.Dragon.HDR.mkv", "Crouching Tiger Hidden Dragon", 0},
		{"Crouching.Tiger.Hidden.Dragon.HDC.mkv", "Crouching Tiger Hidden Dragon", 0},
		{"Crouching.Tiger.Hidden.Dragon-HDC.mkv", "Crouching Tiger Hidden Dragon", 0},
		{"Crouching.Tiger.Hidden.Dragon.BDrip-HDC.mkv", "Crouching Tiger Hidden Dragon", 0},
		{"Crouching.Tiger.Hidden.Dragon.4K.UltraHD.HDR.BDrip-HDC.mkv", "Crouching Tiger Hidden Dragon", 0},
		{"Last.Call.for.Nowhere.WEB-DL.1080p.mkv", "Last Call for Nowhere", 0},
		{"Robin Hood [Multi-Subs] [2018].mkv", "Robin Hood", 2018},
		{"Maximum Ride - 2016 - WEBDL-1080p - x264 AC3.mkv", "Maximum Ride", 2016},
		{"Rain Man 1988 REMASTERED 1080p BluRay x264 AAC - Ozlem.mp4", "Rain Man", 1988},
		{"Run lola run (lola rennt) (2009).mp4", "Run lola run (lola rennt)", 2009},
		{"[rec].mkv", "[rec]", 0},
		{"[REC] 4 Apocalypse (2014).mkv", "[REC] 4 Apocalypse", 2014},
		{"[REC]² (2009).mkv", "[REC]²", 2009},
		{"1917.4k.HDR.mkv", "1917", 0},
		{"2001.A.Space.Odyssey.1968.4K.mkv", "2001 A Space Odyssey", 1968},
		{"The Web.mkv", "The Web", 0},
		{"Extended Family.mkv", "Extended Family", 0},
		{"Multi Story.mkv", "Multi Story", 0},
		{"[ExampleSubs] Your Name (2016) [1080p].mkv", "Your Name", 2016},
		{"[www.release.example.com] Your.Name.2016.1080p.mkv", "Your Name", 2016},
		{"Your Name [ABC123EF].mkv", "Your Name", 0},
		{"Example Movie [Dual Audio].mkv", "Example Movie", 0},
		{"Example Movie 1920x1080 x264.mkv", "Example Movie", 0},
		{"Example Movie DVD-SCR XviD.avi", "Example Movie", 0},
		{"Example Movie AV1 Opus.mkv", "Example Movie", 0},
		{"Example Movie DTS-HD MA.mkv", "Example Movie", 0},
		{"Example Movie DDP5.1.mkv", "Example Movie", 0},
		{"Example Movie EAC3.mkv", "Example Movie", 0},
		{"Example Movie 1080p (2001).mkv", "Example Movie", 0},
		{"My Movie 2013.12.09 1080p.mkv", "My Movie 2013 12 09", 0},
	}
	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			filePath := "/movies/loose/" + tt.filename
			hints := ParseFilename(filePath, "movies")
			if hints.Title != tt.title || hints.Year != tt.year {
				t.Errorf("filename title = %q (%d), want %q (%d)", hints.Title, hints.Year, tt.title, tt.year)
			}
			_, assignments := InferRootAssignments([]string{filePath}, "movies", 1, nil)
			group := InferGroupIdentity(filePath, "movies", assignments[filePath])
			if group.BaseTitle != tt.title || group.BaseYear != tt.year || group.State != "resolved" {
				t.Errorf("group = %q (%d), %s; want %q (%d), resolved", group.BaseTitle, group.BaseYear, group.State, tt.title, tt.year)
			}
		})
	}
}

func TestMovieReleaseFolderRetainsCleanIdentity(t *testing.T) {
	for _, filePath := range []string{
		"/movies/Example Movie [Multi-Subs] [2020]/title00.mkv",
		"/movies/Example.Movie.2020.[1080p]/title00.mkv",
		"/movies/Example Movie (2020)/VTS_01_1.1080p.mkv",
	} {
		t.Run(filePath, func(t *testing.T) {
			hints := ParseFilename(filePath, "movies")
			if hints.Title != "Example Movie" || hints.Year != 2020 {
				t.Errorf("filename title = %q (%d)", hints.Title, hints.Year)
			}
			_, assignments := InferRootAssignments([]string{filePath}, "movies", 1, nil)
			group := InferGroupIdentity(filePath, "movies", assignments[filePath])
			if group.BaseTitle != "Example Movie" || group.BaseYear != 2020 || group.State != "resolved" {
				t.Errorf("group = %q (%d), %s", group.BaseTitle, group.BaseYear, group.State)
			}
		})
	}
}

func TestMovieReleaseSuffixCannotSupplyYear(t *testing.T) {
	for _, name := range []string{
		"Example Movie 480p 2001",
		"Example Movie 1080p (2001)",
		"Example Movie 2020 1080p 2001",
	} {
		t.Run(name, func(t *testing.T) {
			stem := ParseInferMovieStem(name, "", 0)
			if stem.Title != "Example Movie" || stem.Year == 2001 {
				t.Fatalf("release suffix supplied identity: %+v", stem)
			}
		})
	}
}
