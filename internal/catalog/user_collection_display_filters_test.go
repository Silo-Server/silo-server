package catalog

import (
	"context"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestFilterUserCollectionDisplayItemsAppliesMediaFilterInSourceOrder(t *testing.T) {
	items := []*models.MediaItem{
		{ContentID: "movie-a", Type: "movie"},
		{ContentID: "series-a", Type: "series"},
		{ContentID: "movie-b", Type: "movie"},
	}

	got, err := FilterUserCollectionDisplayItems(context.Background(), nil, items, UserCollectionDisplayFilterOptions{
		MediaFilter: userstore.CollectionMediaFilterMovie,
	})
	if err != nil {
		t.Fatalf("FilterUserCollectionDisplayItems: %v", err)
	}

	if ids := mediaItemIDs(got); !reflect.DeepEqual(ids, []string{"movie-a", "movie-b"}) {
		t.Fatalf("filtered IDs = %#v, want movie source order", ids)
	}
}

func TestFilterUserCollectionDisplayItemsWatchFilterUsesActiveProfile(t *testing.T) {
	items := []*models.MediaItem{
		{ContentID: "movie-a", Type: "movie"},
		{ContentID: "movie-b", Type: "movie"},
		{ContentID: "movie-c", Type: "movie"},
	}
	store := &displayFilterProgressStore{
		progressByProfile: map[string]map[string]userstore.WatchProgress{
			"profile-a": {
				"movie-a": {ProfileID: "profile-a", MediaItemID: "movie-a", Completed: true},
				"movie-b": {ProfileID: "profile-a", MediaItemID: "movie-b", Completed: false},
			},
		},
		completedByProfile: map[string]map[string]string{
			"profile-a": {"movie-c": "2026-01-01T00:00:00Z"},
		},
	}

	watched, err := FilterUserCollectionDisplayItems(context.Background(), store, items, UserCollectionDisplayFilterOptions{
		ProfileID:   "profile-a",
		WatchFilter: userstore.CollectionWatchFilterWatched,
	})
	if err != nil {
		t.Fatalf("FilterUserCollectionDisplayItems(watched): %v", err)
	}
	if ids := mediaItemIDs(watched); !reflect.DeepEqual(ids, []string{"movie-a", "movie-c"}) {
		t.Fatalf("watched IDs = %#v, want progress + completed history", ids)
	}

	unwatched, err := FilterUserCollectionDisplayItems(context.Background(), store, items, UserCollectionDisplayFilterOptions{
		ProfileID:   "profile-a",
		WatchFilter: userstore.CollectionWatchFilterUnwatched,
	})
	if err != nil {
		t.Fatalf("FilterUserCollectionDisplayItems(unwatched): %v", err)
	}
	if ids := mediaItemIDs(unwatched); !reflect.DeepEqual(ids, []string{"movie-b"}) {
		t.Fatalf("unwatched IDs = %#v, want only incomplete item", ids)
	}

	otherProfileWatched, err := FilterUserCollectionDisplayItems(context.Background(), store, items, UserCollectionDisplayFilterOptions{
		ProfileID:   "profile-b",
		WatchFilter: userstore.CollectionWatchFilterWatched,
	})
	if err != nil {
		t.Fatalf("FilterUserCollectionDisplayItems(other profile): %v", err)
	}
	if ids := mediaItemIDs(otherProfileWatched); len(ids) != 0 {
		t.Fatalf("other profile watched IDs = %#v, want none", ids)
	}
}

func TestFilterUserCollectionDisplayItemsSeriesRequiresAllEpisodesComplete(t *testing.T) {
	items := []*models.MediaItem{
		{ContentID: "series-a", Type: "series"},
		{ContentID: "series-b", Type: "series"},
	}
	store := &displayFilterProgressStore{
		progressByProfile: map[string]map[string]userstore.WatchProgress{
			"profile-a": {
				"episode-a1": {ProfileID: "profile-a", MediaItemID: "episode-a1", Completed: true},
				"episode-a2": {ProfileID: "profile-a", MediaItemID: "episode-a2", Completed: true},
				"episode-b1": {ProfileID: "profile-a", MediaItemID: "episode-b1", Completed: true},
			},
		},
	}
	episodes := &displayFilterEpisodeLister{
		series: map[string][]*models.Episode{
			"series-a": {
				{ContentID: "episode-a1"},
				{ContentID: "episode-a2"},
			},
			"series-b": {
				{ContentID: "episode-b1"},
				{ContentID: "episode-b2"},
			},
		},
	}

	watched, err := FilterUserCollectionDisplayItems(context.Background(), store, items, UserCollectionDisplayFilterOptions{
		ProfileID:     "profile-a",
		WatchFilter:   userstore.CollectionWatchFilterWatched,
		EpisodeLister: episodes,
	})
	if err != nil {
		t.Fatalf("FilterUserCollectionDisplayItems(watched): %v", err)
	}
	if ids := mediaItemIDs(watched); !reflect.DeepEqual(ids, []string{"series-a"}) {
		t.Fatalf("watched series IDs = %#v, want only fully complete series", ids)
	}

	unwatched, err := FilterUserCollectionDisplayItems(context.Background(), store, items, UserCollectionDisplayFilterOptions{
		ProfileID:     "profile-a",
		WatchFilter:   userstore.CollectionWatchFilterUnwatched,
		EpisodeLister: episodes,
	})
	if err != nil {
		t.Fatalf("FilterUserCollectionDisplayItems(unwatched): %v", err)
	}
	if ids := mediaItemIDs(unwatched); !reflect.DeepEqual(ids, []string{"series-b"}) {
		t.Fatalf("unwatched series IDs = %#v, want series with an incomplete episode", ids)
	}
}

func mediaItemIDs(items []*models.MediaItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil {
			ids = append(ids, item.ContentID)
		}
	}
	return ids
}

type displayFilterProgressStore struct {
	progressByProfile  map[string]map[string]userstore.WatchProgress
	completedByProfile map[string]map[string]string
}

func (s *displayFilterProgressStore) ListProgressByMediaItems(
	_ context.Context,
	profileID string,
	mediaItemIDs []string,
) (map[string]userstore.WatchProgress, error) {
	progress := s.progressByProfile[profileID]
	out := make(map[string]userstore.WatchProgress, len(progress))
	for _, mediaItemID := range mediaItemIDs {
		if entry, ok := progress[mediaItemID]; ok {
			out[mediaItemID] = entry
		}
	}
	return out, nil
}

func (s *displayFilterProgressStore) ListCompletedHistoryItems(
	_ context.Context,
	query userstore.CompletedHistoryItemQuery,
) ([]userstore.CompletedHistoryItem, error) {
	completed := s.completedByProfile[query.ProfileID]
	out := make([]userstore.CompletedHistoryItem, 0, len(query.MediaItemIDs))
	for _, mediaItemID := range query.MediaItemIDs {
		if watchedAt, ok := completed[mediaItemID]; ok {
			out = append(out, userstore.CompletedHistoryItem{
				MediaItemID: mediaItemID,
				WatchedAt:   watchedAt,
			})
		}
	}
	return out, nil
}

type displayFilterEpisodeLister struct {
	series  map[string][]*models.Episode
	seasons map[string][]*models.Episode
}

func (l *displayFilterEpisodeLister) ListBySeriesIDs(
	_ context.Context,
	seriesIDs []string,
) (map[string][]*models.Episode, error) {
	out := make(map[string][]*models.Episode, len(seriesIDs))
	for _, seriesID := range seriesIDs {
		out[seriesID] = append([]*models.Episode(nil), l.series[seriesID]...)
	}
	return out, nil
}

func (l *displayFilterEpisodeLister) ListBySeasonIDs(
	_ context.Context,
	seasonIDs []string,
) (map[string][]*models.Episode, error) {
	out := make(map[string][]*models.Episode, len(seasonIDs))
	for _, seasonID := range seasonIDs {
		out[seasonID] = append([]*models.Episode(nil), l.seasons[seasonID]...)
	}
	return out, nil
}
