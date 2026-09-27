package naming

import "testing"

func TestFlexibleNamingAcrossScanIdentity(t *testing.T) {
	tests := []struct {
		name, path, library, title, kind string
		year, season, episode            int
	}{
		{"x episode", "/tv/Example Show/Example.Show.1x02.The.Arrival.mkv", "series", "Example Show", "series", 0, 1, 2},
		{"x mixed library", "/media/Example Show/Example.Show.1x02.mkv", "mixed", "Example Show", "series", 0, 1, 2},
		{"written episode", "/tv/Example Show/Example Show Season 1 Episode 2.mkv", "series", "Example Show", "series", 0, 1, 2},
		{"spaced episode", "/tv/Example Show/Example Show S 01 E 02.mkv", "series", "Example Show", "series", 0, 1, 2},
		{"episode in season", "/tv/Example Show/Season 2/E03 - The Arrival.mkv", "series", "Example Show", "series", 0, 2, 3},
		{"episode with dimensions", "/tv/Example Show/Season 2/E03.720x480.mkv", "series", "Example Show", "series", 0, 2, 3},
		{"episode word in season", "/tv/Example Show/Season 2/Episode 03 - The Arrival.mkv", "series", "Example Show", "series", 0, 2, 3},
		{"number in season", "/tv/Example Show/Season 2/03 - The Arrival.mkv", "series", "Example Show", "series", 0, 2, 3},
		{"only number in season", "/tv/Example Show/Season 2/03.mkv", "series", "Example Show", "series", 0, 2, 3},
		{"padded number in season", "/tv/Example Show/Season 2/003.The.Arrival.mkv", "series", "Example Show", "series", 0, 2, 3},
		{"nearest season folder", "/archive/Season 1/Example Show/Season 2/E03.mkv", "series", "Example Show", "series", 0, 2, 3},
		{"short season folder", "/tv/Example Show/S02/E03.mkv", "series", "Example Show", "series", 0, 2, 3},
		{"underscore season folder", "/tv/Example Show/Season_02/E03.mkv", "series", "Example Show", "series", 0, 2, 3},
		{"special", "/tv/Example Show/Specials/E03.mkv", "series", "Example Show", "series", 0, 0, 3},
		{"unseasoned labeled episode", "/tv/Example Show/E03.mkv", "series", "Example Show", "series", 0, 0, 3},
		{"unseasoned episode number", "/tv/Example Show/03.mkv", "series", "Example Show", "series", 0, 0, 3},
		{"compact episode", "/tv/Example Show/Season 3/301.mkv", "series", "Example Show", "series", 0, 3, 1},
		{"compact named episode", "/tv/Example Show/Season 3/Example.Show.301.mkv", "series", "Example Show", "series", 0, 3, 1},
		{"year season range", "/tv/Example Show/Season 2009/2009x03-E15.mkv", "series", "Example Show", "series", 0, 2009, 3},
		{"loose release movie", "/movies/Example.Movie.2020.1080p.BluRay.x264-GROUP.mkv", "movies", "Example Movie", "movie", 2020, 0, 0},
		{"movie without year", "/movies/Example.Movie.1080p.BluRay.x264-GROUP.mkv", "movies", "Example Movie", "movie", 0, 0, 0},
		{"bracketed movie quality", "/movies/Example Movie [1080p] [BluRay].mkv", "movies", "Example Movie", "movie", 0, 0, 0},
		{"movie source without year", "/movies/Example_Movie_DVDRip_XviD-GROUP.avi", "movies", "Example Movie", "movie", 0, 0, 0},
		{"dotted movie folder", "/movies/Example.Movie.2020.1080p.BluRay/random.mkv", "movies", "Example Movie", "movie", 2020, 0, 0},
		{"dotted show folder", "/tv/Example.Show/Season 1/Example.Show.S01E02.mkv", "series", "Example Show", "series", 0, 1, 2},
		{"numeric movie title", "/movies/1917.1080p.BluRay.mkv", "movies", "1917", "movie", 0, 0, 0},
		// Public examples from filebot.net/naming.html, and Sonarr/Radarr's
		// Organizer/NamingConfig.cs defaults with the placeholders filled in.
		{"filebot x", "/tv/Dark Angel/Season 3/3x01 - Labyrinth.mkv", "series", "Dark Angel", "series", 0, 3, 1},
		{"filebot bracketed movie", "/movies/The Man from Earth [2007] 720p 6ch.mkv", "movies", "The Man from Earth", "movie", 2007, 0, 0},
		{"filebot scene movie", "/movies/The.Man.From.Earth.2007.DVDRip.XviD-ALLiANCE.avi", "movies", "The Man From Earth", "movie", 2007, 0, 0},
		{"filebot scene episode", "/tv/Firefly/Season 1/Firefly.s01e01.Serenity.720p.x264.ac3.mkv", "series", "Firefly", "series", 0, 1, 1},
		{"sonarr default", "/tv/Example Show/Season 1/Example Show - S01E02 - The Arrival HDTV-720p.mkv", "series", "Example Show", "series", 0, 1, 2},
		{"radarr default", "/movies/Example Movie (2020)/Example Movie (2020) Bluray-1080p.mkv", "movies", "Example Movie", "movie", 2020, 0, 0},
		{"disc rip named parent", "/movies/Example Movie (2020)/title00.mkv", "movies", "Example Movie", "movie", 2020, 0, 0},
		{"disc rip release parent", "/movies/Example.Movie.2020.1080p.BluRay/title_t00.mkv", "movies", "Example Movie", "movie", 2020, 0, 0},
		{"title word web", "/movies/The Web.mkv", "movies", "The Web", "movie", 0, 0, 0},
		{"title word extended", "/movies/Extended Family.mkv", "movies", "Extended Family", "movie", 0, 0, 0},
		{"bracketed title", "/movies/[REC].mkv", "movies", "[REC]", "movie", 0, 0, 0},
		{"numeric series title", "/tv/Space 1999/Season 1/Space 1999 S01E01.mkv", "series", "Space 1999", "series", 0, 1, 1},
		{"numeric series release", "/tv/Space 1999/Season 1/Space.1999.S01E01.1080p.mkv", "series", "Space 1999", "series", 0, 1, 1},
		{"numeric movie title agrees with folder", "/movies/Blade Runner 2049/Blade Runner 2049.mkv", "movies", "Blade Runner 2049", "movie", 0, 0, 0},
		{"numeric movie release agrees with folder", "/movies/Blade Runner 2049/Blade.Runner.2049.1080p.mkv", "movies", "Blade Runner 2049", "movie", 0, 0, 0},
		{"disc rip with quality", "/movies/Example Movie (2020)/title00.1080p.mkv", "movies", "Example Movie", "movie", 2020, 0, 0},
		{"disc track with quality", "/movies/Example Movie (2020)/title_t00.1080p.mkv", "movies", "Example Movie", "movie", 2020, 0, 0},
		{"explicit year before release group year", "/movies/loose/My Movie (1997) - GreatestReleaseGroup 2019.mp4", "movies", "My Movie", "movie", 1997, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hints := ParseFilename(tt.path, tt.library)
			if hints.Title != tt.title || hints.Type != tt.kind || hints.Year != tt.year || hints.SeasonNum != tt.season || hints.EpisodeNum != tt.episode {
				t.Errorf("filename = %+v; want %q (%d), %s, season %d episode %d", hints, tt.title, tt.year, tt.kind, tt.season, tt.episode)
			}
			_, assignments := InferRootAssignments([]string{tt.path}, tt.library, 1, nil)
			if assignment := assignments[tt.path]; assignment.Title != tt.title || assignment.Year != tt.year {
				t.Errorf("root identity = %q (%d); want %q (%d)", assignment.Title, assignment.Year, tt.title, tt.year)
			}
			group := InferGroupIdentity(tt.path, tt.library, assignments[tt.path])
			if group.BaseTitle != tt.title || group.BaseYear != tt.year || group.BaseType != tt.kind || group.State != "resolved" {
				t.Errorf("scan identity = %q (%d), %s, %s; want %q (%d), %s, resolved", group.BaseTitle, group.BaseYear, group.BaseType, group.State, tt.title, tt.year, tt.kind)
			}
		})
	}
}

func TestFlexibleEpisodeNamingRequiresEvidence(t *testing.T) {
	for _, filePath := range []string{
		"/tv/Example Show/Season 1/1080p.mkv",
		"/tv/Example Show/Season 1/1920x1080.mkv",
		"/tv/Example Show/Season 1/720x480.mkv",
		"/tv/Example Show/Season 1/360x240.mkv",
		"/tv/Example Show/Season 1/2020-03-04.mkv",
		"/tv/Example Show/Season 1/03.04.2020.mkv",
		"/tv/Example Show/Season 1/03-04-2020.mkv",
		"/tv/Example Show/Season 1/2001 - A Space Odyssey.mkv",
		"/tv/Example Show/Season 1/E123456.mkv",
		"/tv/Example Show/Extras/01 - Making Of.mkv",
		"/tv/Example Show/Season 1/Trailers/E02 - Preview.mkv",
	} {
		t.Run(filePath, func(t *testing.T) {
			if hints := ParseFilename(filePath, "series"); hints.EpisodeNum != 0 {
				t.Fatalf("parsed unsupported or ambiguous episode: %+v", hints)
			}
		})
	}
}
