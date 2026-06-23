package userstore

import (
	"context"
	"strings"
)

const (
	CollectionWatchFilterAll       = "all"
	CollectionWatchFilterUnwatched = "unwatched"
	CollectionWatchFilterWatched   = "watched"

	CollectionMediaFilterAll    = "all"
	CollectionMediaFilterMovie  = "movie"
	CollectionMediaFilterSeries = "series"
)

var CollectionWatchFilterValues = []string{
	CollectionWatchFilterAll,
	CollectionWatchFilterUnwatched,
	CollectionWatchFilterWatched,
}

var CollectionMediaFilterValues = []string{
	CollectionMediaFilterAll,
	CollectionMediaFilterMovie,
	CollectionMediaFilterSeries,
}

type ProgressCompletionStore interface {
	ListProgressByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]WatchProgress, error)
	ListCompletedHistoryItems(ctx context.Context, query CompletedHistoryItemQuery) ([]CompletedHistoryItem, error)
}

func NormalizeCollectionWatchFilter(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return CollectionWatchFilterAll, true
	}
	switch value {
	case CollectionWatchFilterAll, CollectionWatchFilterUnwatched, CollectionWatchFilterWatched:
		return value, true
	default:
		return "", false
	}
}

func NormalizeCollectionMediaFilter(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return CollectionMediaFilterAll, true
	}
	switch value {
	case CollectionMediaFilterAll, CollectionMediaFilterMovie, CollectionMediaFilterSeries:
		return value, true
	default:
		return "", false
	}
}
