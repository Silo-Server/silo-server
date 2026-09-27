package naming

import "testing"

func TestParityAdversarialMovieBracketDates(t *testing.T) {
	for _, filename := range []string{
		"Example Movie [1080p] (2001).mkv",
		"Example Movie [480p] 2001.mkv",
		"Example Movie [Remastered 1080p] 2001.mkv",
	} {
		t.Run(filename, func(t *testing.T) {
			hints := ParseFilename("/movies/loose/"+filename, "movies")
			if hints.Year != 0 || hints.Title != "Example Movie" {
				t.Fatalf("technical suffix supplied identity: %+v", hints)
			}
		})
	}
}

func TestParityAdversarialDailyShowsInFlatFolder(t *testing.T) {
	paths := []string{
		"/tv/Incoming/The.Daily.Show.2026.04.24.mkv",
		"/tv/Incoming/The.Late.Show.2026.04.24.mkv",
	}
	_, assignments := InferRootAssignments(paths, "series", 1, nil, "/tv/Incoming")
	if assignments[paths[0]].RootPath == assignments[paths[1]].RootPath {
		t.Fatalf("unrelated daily shows share a metadata queue root: %+v", assignments)
	}
}

func TestParityAdversarialDayFirstDailyEpisode(t *testing.T) {
	for _, filename := range []string{
		"Daily.Show.09.03.2020.720p.mkv",
		"Daily Show 09-03-2020 720p.mkv",
		"Daily.Show.31.03.2020.720p.mkv",
	} {
		t.Run(filename, func(t *testing.T) {
			hints := ParseFilename("/tv/Daily Show/"+filename, "series")
			if hints.EpisodeNum != 0 {
				t.Fatalf("calendar date became episode: %+v", hints)
			}
		})
	}
}

func TestParityAdversarialSeriesYearDoesNotDisplaceKnownFolder(t *testing.T) {
	filePath := "/tv/The Expanse/The.Expanse.2015.S01E01.mkv"
	ctx := ResolvePathContext(filePath, "series")
	if ctx.Title != "The Expanse" || ctx.RootPath != "/tv/The Expanse" {
		t.Fatalf("release year displaced matching show folder: %+v", ctx)
	}
}
