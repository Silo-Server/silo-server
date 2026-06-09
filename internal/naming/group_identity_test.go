package naming

import "testing"

// A renamed release inside a provider-tagged folder must stay resolved: the
// structured IDs anchor the identity even though folder and file titles are
// unrelated (Radarr keeps the folder name from grab time while the release
// inside carries the retitled name).
func TestInferGroupIdentity_StructuredIDsResolveMovieTitleConflict(t *testing.T) {
	group := InferGroupIdentity(
		"/movies/20s/On Fire {imdb-tt28078628} {tmdb-1196791}/Soul on Fire (2025) [WEBDL-1080p 8-bit h264 EAC3 5.1]-FLUX.mkv",
		"movies",
		RootAssignment{
			RootPath:     "/movies/20s/On Fire {imdb-tt28078628} {tmdb-1196791}",
			InferredType: "movie",
		},
	)

	if got, want := group.State, "resolved"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := group.BaseTitle, "On Fire"; got != want {
		t.Fatalf("BaseTitle = %q, want %q", got, want)
	}
	if got, want := group.TmdbID, "1196791"; got != want {
		t.Fatalf("TmdbID = %q, want %q", got, want)
	}
	if got, want := group.ImdbID, "tt28078628"; got != want {
		t.Fatalf("ImdbID = %q, want %q", got, want)
	}
}

// Without provider IDs the same folder/file title conflict must still be
// flagged ambiguous — there is nothing to anchor the identity.
func TestInferGroupIdentity_TitleConflictWithoutIDsStaysAmbiguous(t *testing.T) {
	group := InferGroupIdentity(
		"/movies/20s/On Fire (2024)/Soul on Fire (2025) [WEBDL-1080p 8-bit h264 EAC3 5.1]-FLUX.mkv",
		"movies",
		RootAssignment{
			RootPath:     "/movies/20s/On Fire (2024)",
			InferredType: "movie",
		},
	)

	if got, want := group.State, "ambiguous"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
}

func TestInferGroupIdentity_StructuredIDsResolveSeriesTitleConflict(t *testing.T) {
	group := InferGroupIdentity(
		"/tv/Border Control - Sweden (2023) {tvdb-442777}/Season 01/Gränsbevakarna Sverige S01E01.mkv",
		"series",
		RootAssignment{
			RootPath:           "/tv/Border Control - Sweden (2023) {tvdb-442777}",
			InferredType:       "series",
			Title:              "Gränsbevakarna Sverige",
			HasSeasonStructure: true,
			HasEpisodePattern:  true,
		},
	)

	if got, want := group.State, "resolved"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
	if got, want := group.TvdbID, "442777"; got != want {
		t.Fatalf("TvdbID = %q, want %q", got, want)
	}
}

func TestInferGroupIdentity_SeriesTitleConflictWithoutIDsStaysAmbiguous(t *testing.T) {
	group := InferGroupIdentity(
		"/tv/Border Control - Sweden (2023)/Season 01/Gränsbevakarna Sverige S01E01.mkv",
		"series",
		RootAssignment{
			RootPath:           "/tv/Border Control - Sweden (2023)",
			InferredType:       "series",
			Title:              "Gränsbevakarna Sverige",
			HasSeasonStructure: true,
			HasEpisodePattern:  true,
		},
	)

	if got, want := group.State, "ambiguous"; got != want {
		t.Fatalf("State = %q, want %q", got, want)
	}
}
