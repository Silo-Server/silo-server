package catalog

import (
	"testing"
)

func TestDownloadEpisodePageRejectsInvalidBounds(t *testing.T) {
	repo := &EpisodeRepository{}
	for _, limit := range []int{0, 201} {
		if _, _, err := repo.ListDownloadEpisodesPage(t.Context(), "s", nil, nil, limit); err == nil {
			t.Fatal("accepted limit")
		}
	}
	if _, _, err := repo.ListDownloadEpisodesPage(t.Context(), "s", new(-1), nil, 1); err == nil {
		t.Fatal("accepted negative season")
	}
	if _, _, err := repo.ListDownloadEpisodesPage(t.Context(), "s", new(0), &EpisodePagePosition{SeasonNumber: 1, ContentID: "id"}, 1); err == nil {
		t.Fatal("accepted wrong season cursor")
	}
}
