package naming

import "testing"

// TestInferGroupIdentity_ProviderIDsResolveTitleConflicts checks title
// conflicts between folder and file. Provider IDs anchor the identity; without
// them the conflict stays ambiguous. Empty expectations are not checked.
func TestInferGroupIdentity_ProviderIDsResolveTitleConflicts(t *testing.T) {
	border := RootAssignment{
		InferredType:       "series",
		Title:              "Gränsbevakarna Sverige",
		HasSeasonStructure: true,
		HasEpisodePattern:  true,
	}
	for _, tt := range []struct {
		name        string
		path        string
		libraryType string
		root        RootAssignment
		wantState   string
		wantTitle   string
		wantTmdb    string
		wantImdb    string
		wantTvdb    string
	}{
		// A renamed release inside a provider-tagged folder must stay resolved:
		// the structured IDs anchor the identity even though folder and file
		// titles are unrelated (Radarr keeps the folder name from grab time
		// while the release inside carries the retitled name).
		{name: "structured IDs resolve a movie title conflict",
			path:        "/movies/20s/On Fire {imdb-tt28078628} {tmdb-1196791}/Soul on Fire (2025) [WEBDL-1080p 8-bit h264 EAC3 5.1]-FLUX.mkv",
			libraryType: "movies",
			root:        RootAssignment{RootPath: "/movies/20s/On Fire {imdb-tt28078628} {tmdb-1196791}", InferredType: "movie"},
			wantState:   "resolved", wantTitle: "On Fire", wantTmdb: "1196791", wantImdb: "tt28078628"},
		// An unbracketed trailing IMDb id anchors identity the same way a
		// structured tag does — the tt prefix is unambiguous. Mirrors the
		// dev-library case "Eggs Run (2021) tt8049994" whose file carries a
		// different release title.
		{name: "trailing IMDb ID resolves a movie title conflict",
			path:        "/movies-int/es/Eggs Run (2021) tt8049994/Huevitos Congelados (2022) [WEBDL-1080p]-GRP.mkv",
			libraryType: "movies",
			root:        RootAssignment{RootPath: "/movies-int/es/Eggs Run (2021) tt8049994", InferredType: "movie"},
			wantState:   "resolved", wantImdb: "tt8049994"},
		// A trailing IMDb id on the file stem (folder carries none) must both
		// anchor the identity and be persisted into the group, so downstream
		// matching sees the provider ID that justified the resolution.
		{name: "file-level trailing IMDb ID anchors and persists",
			path:        "/movies/On Fire (2024)/Soul on Fire tt28078628.mkv",
			libraryType: "movies",
			root:        RootAssignment{RootPath: "/movies/On Fire (2024)", InferredType: "movie"},
			wantState:   "resolved", wantImdb: "tt28078628"},
		{name: "structured IDs resolve a series title conflict",
			path:        "/tv/Border Control - Sweden (2023) {tvdb-442777}/Season 01/Gränsbevakarna Sverige S01E01.mkv",
			libraryType: "series",
			root:        withRootPath(border, "/tv/Border Control - Sweden (2023) {tvdb-442777}"),
			wantState:   "resolved", wantTvdb: "442777"},
		{name: "series title conflict without IDs stays ambiguous",
			path:        "/tv/Border Control - Sweden (2023)/Season 01/Gränsbevakarna Sverige S01E01.mkv",
			libraryType: "series",
			root:        withRootPath(border, "/tv/Border Control - Sweden (2023)"),
			wantState:   "ambiguous"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			group := InferGroupIdentity(tt.path, tt.libraryType, tt.root)
			if got := group.State; got != tt.wantState {
				t.Fatalf("State = %q, want %q", got, tt.wantState)
			}
			for _, check := range []struct{ field, got, want string }{
				{"BaseTitle", group.BaseTitle, tt.wantTitle},
				{"TmdbID", group.TmdbID, tt.wantTmdb},
				{"ImdbID", group.ImdbID, tt.wantImdb},
				{"TvdbID", group.TvdbID, tt.wantTvdb},
			} {
				if check.want != "" && check.got != check.want {
					t.Fatalf("%s = %q, want %q", check.field, check.got, check.want)
				}
			}
		})
	}
}

func withRootPath(root RootAssignment, path string) RootAssignment {
	root.RootPath = path
	return root
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
