package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeItemLookup struct {
	items map[string]*models.MediaItem
}

func (f *fakeItemLookup) GetByID(_ context.Context, contentID string) (*models.MediaItem, error) {
	if item, ok := f.items[contentID]; ok {
		return item, nil
	}
	return nil, fmt.Errorf("item %s not found", contentID)
}

func splitTestHandler(items map[string]*models.MediaItem) *AdminSplitHandler {
	return &AdminSplitHandler{items: &fakeItemLookup{items: items}}
}

func TestResolveSplitTarget_ProviderIDsDeriveDeterministicID(t *testing.T) {
	t.Parallel()
	source := &models.MediaItem{ContentID: "movie-tmdb-100", Type: "movie", Title: "The Grudge", Year: 2004}
	h := splitTestHandler(map[string]*models.MediaItem{source.ContentID: source})
	moved := []splitFile{{ID: 1, MediaFolderID: 3, FilePath: "/m/a.mkv", ObservedRootPath: "/m"}}

	target, err := h.resolveSplitTarget(context.Background(), source, moved, splitTargetRequest{
		ProviderIDs: map[string]string{"TMDB": " 11838 "},
	})
	if err != nil {
		t.Fatalf("resolveSplitTarget: %v", err)
	}
	if target.contentID != "movie-tmdb-11838" {
		t.Fatalf("contentID = %q, want movie-tmdb-11838", target.contentID)
	}
	if !target.created {
		t.Fatal("expected target.created for unknown derived id")
	}
	if target.providerIDs["tmdb"] != "11838" {
		t.Fatalf("providerIDs = %v, want normalized tmdb 11838", target.providerIDs)
	}
	// Falls back to the source title for the skeleton row.
	if target.title != source.Title {
		t.Fatalf("title = %q, want %q", target.title, source.Title)
	}
}

func TestResolveSplitTarget_ExistingContentIDAdoptsProviderIDs(t *testing.T) {
	t.Parallel()
	source := &models.MediaItem{ContentID: "movie-tmdb-100", Type: "movie", Title: "Crash"}
	existing := &models.MediaItem{ContentID: "movie-tmdb-10723", Type: "movie", Title: "Crash (1996)", TmdbID: "10723"}
	h := splitTestHandler(map[string]*models.MediaItem{
		source.ContentID:   source,
		existing.ContentID: existing,
	})
	moved := []splitFile{{ID: 1, MediaFolderID: 3, FilePath: "/m/a.mkv", ObservedRootPath: "/m"}}

	target, err := h.resolveSplitTarget(context.Background(), source, moved, splitTargetRequest{
		ContentID: existing.ContentID,
	})
	if err != nil {
		t.Fatalf("resolveSplitTarget: %v", err)
	}
	if target.created {
		t.Fatal("existing target must not be flagged created")
	}
	if target.providerIDs["tmdb"] != "10723" {
		t.Fatalf("providerIDs = %v, want target's tmdb id for override persistence", target.providerIDs)
	}
}

func TestResolveSplitTarget_Rejections(t *testing.T) {
	t.Parallel()
	source := &models.MediaItem{ContentID: "movie-tmdb-100", Type: "movie", Title: "A"}
	series := &models.MediaItem{ContentID: "series-tvdb-5", Type: "series", Title: "B"}
	h := splitTestHandler(map[string]*models.MediaItem{
		source.ContentID: source,
		series.ContentID: series,
	})
	moved := []splitFile{{ID: 1, MediaFolderID: 3, FilePath: "/m/a.mkv", ObservedRootPath: "/m"}}

	cases := []struct {
		name string
		req  splitTargetRequest
	}{
		{"no target", splitTargetRequest{}},
		{"type mismatch", splitTargetRequest{ContentID: series.ContentID}},
		{"unknown content id", splitTargetRequest{ContentID: "movie-tmdb-404"}},
		{"target equals source", splitTargetRequest{ContentID: source.ContentID}},
		{"unusable provider ids", splitTargetRequest{ProviderIDs: map[string]string{"anidb": "1"}}},
	}
	for _, tc := range cases {
		if _, err := h.resolveSplitTarget(context.Background(), source, moved, tc.req); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

func TestDeriveEpisodePairs(t *testing.T) {
	t.Parallel()
	series := &models.MediaItem{ContentID: "series-tvdb-100", Type: "series"}
	moved := []splitFile{
		{ID: 1, EpisodeID: "episode-tvdb-100-1-1", SeasonNumber: 1, EpisodeNumber: 1},
		{ID: 2, EpisodeID: "episode-tvdb-100-1-2", SeasonNumber: 1, EpisodeNumber: 2},
		{ID: 3, EpisodeID: "episode-tvdb-100-1-2", SeasonNumber: 1, EpisodeNumber: 2}, // dup version
		{ID: 4, EpisodeID: "", SeasonNumber: 1, EpisodeNumber: 3},                     // never linked
		{ID: 5, EpisodeID: "episode-tvdb-100-0-0", SeasonNumber: 0, EpisodeNumber: 0}, // unparsed
	}

	pairs := deriveEpisodePairs(series, moved, "series-tvdb-200")
	if len(pairs) != 2 {
		t.Fatalf("pairs = %v, want 2", pairs)
	}
	if pairs[0].To != "episode-tvdb-200-1-1" || pairs[1].To != "episode-tvdb-200-1-2" {
		t.Fatalf("pair targets = %v, want re-anchored deterministic ids", pairs)
	}

	// Local target: no deterministic anchor, no pairs.
	if got := deriveEpisodePairs(series, moved, "local-abcdef"); got != nil {
		t.Fatalf("local target pairs = %v, want nil", got)
	}
	// Movies never pair.
	movie := &models.MediaItem{ContentID: "movie-tmdb-1", Type: "movie"}
	if got := deriveEpisodePairs(movie, moved, "movie-tmdb-2"); got != nil {
		t.Fatalf("movie pairs = %v, want nil", got)
	}
}
