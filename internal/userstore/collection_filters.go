package userstore

import "context"

// ProgressCompletionStore exposes the per-profile progress/history reads used
// to derive played/read state. Personal-collection display filtering no longer
// has its own watched evaluator — it routes through the catalog query executor
// (see internal/catalog/display_query_filter.go) — but this interface is still
// used by the shared progress helpers.
type ProgressCompletionStore interface {
	ListProgressByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]WatchProgress, error)
	ListCompletedHistoryItems(ctx context.Context, query CompletedHistoryItemQuery) ([]CompletedHistoryItem, error)
}
