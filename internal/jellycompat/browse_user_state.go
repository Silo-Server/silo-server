package jellycompat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const compatBrowseRandomSort = "random"

// browseConfiguredUserState scans catalog candidates in bounded batches. State
// predicates run against the configured store before counting and paging; only
// the requested matching page survives the scan. PostgreSQL uses its SQL path.
func (s *directContentService) browseConfiguredUserState(ctx context.Context, session *Session, filters catalog.BrowseFilters, includeTotal bool, fetch func(catalog.BrowseFilters) ([]upstreamListItem, bool, error)) (*upstreamBrowseResponse, error) {
	store, err := s.userStore(ctx, session)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, fmt.Errorf("user store unavailable")
	}
	page := filters
	if page.Sort == compatBrowseRandomSort && page.SnapshotAt == nil {
		page.SnapshotAt = new(time.Now().UTC())
	}
	page.IsFavorite, page.IsPlayed, page.IsResumable = false, nil, false
	page.Offset, page.Limit = 0, compatBrowseChunkLimit
	result := &upstreamBrowseResponse{Items: []upstreamListItem{}}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items, more, err := fetch(page)
		if err != nil {
			return nil, err
		}
		matches, err := s.matchConfiguredUserState(ctx, store, session.ProfileID, items, filters)
		if err != nil {
			return nil, err
		}
		for i, item := range items {
			if !matches[i] {
				continue
			}
			if result.Total >= filters.Offset && len(result.Items) < filters.Limit {
				result.Items = append(result.Items, item)
			}
			result.Total++
			if !includeTotal && result.Total > filters.Offset+filters.Limit {
				result.Total = 0
				result.HasMore = true
				return result, nil
			}
		}
		if !more || len(items) == 0 {
			break
		}
		page.Offset += len(items)
	}
	result.HasMore = result.Total > filters.Offset+len(result.Items)
	if !includeTotal {
		result.Total = 0
	}
	return result, nil
}

// configuredPlayed distinguishes completed history from raw progress: resume
// predicates use the latter, even if an older completed watch exists.
func configuredPlayed(ctx context.Context, store userstore.UserStore, profileID string, ids []string) (map[string]userstore.WatchProgress, map[string]bool, error) {
	progress, err := store.ListProgressByMediaItems(ctx, profileID, ids)
	if err != nil {
		return nil, nil, err
	}
	history, err := store.ListCompletedHistoryItems(ctx, userstore.CompletedHistoryItemQuery{ProfileID: profileID, MediaItemIDs: ids})
	if err != nil {
		return nil, nil, err
	}
	played := make(map[string]bool, len(ids))
	for id, row := range progress {
		played[id] = row.Completed
	}
	for _, row := range history {
		played[row.MediaItemID] = true
	}
	return progress, played, nil
}

func (s *directContentService) matchConfiguredUserState(ctx context.Context, store userstore.UserStore, profileID string, items []upstreamListItem, filters catalog.BrowseFilters) ([]bool, error) {
	matches := make([]bool, len(items))
	if len(items) == 0 {
		return matches, nil
	}
	ids := contentIDsFromListItems(items)
	var favorites map[string]bool
	var err error
	if filters.IsFavorite {
		favorites, err = store.ListFavoritesByMediaItems(ctx, profileID, ids)
		if err != nil {
			return nil, err
		}
	}
	var progress map[string]userstore.WatchProgress
	var played map[string]bool
	if filters.IsPlayed != nil {
		progress, played, err = configuredPlayed(ctx, store, profileID, ids)
	} else if filters.IsResumable {
		progress, err = store.ListProgressByMediaItems(ctx, profileID, ids)
	}
	if err != nil {
		return nil, err
	}
	for i, item := range items {
		if filters.IsFavorite && !favorites[item.ContentID] {
			continue
		}
		if filters.IsResumable {
			row := progress[item.ContentID]
			if row.PositionSeconds <= 0 || row.Completed {
				continue
			}
		}
		if filters.IsPlayed != nil {
			completed := played[item.ContentID]
			if strings.EqualFold(item.Type, "series") {
				completed, err = s.configuredSeriesPlayed(ctx, store, profileID, item.ContentID)
				if err != nil {
					return nil, err
				}
			}
			if completed != *filters.IsPlayed {
				continue
			}
		}
		matches[i] = true
	}
	return matches, nil
}

func (s *directContentService) configuredSeriesPlayed(ctx context.Context, store userstore.UserStore, profileID, seriesID string) (bool, error) {
	if s.episodeRepo == nil {
		return false, fmt.Errorf("episode catalog unavailable")
	}
	// Production reads only episode IDs in pages, so even a very long series
	// does not expand a catalog batch into an unbounded episode allocation.
	if repo, ok := s.episodeRepo.(interface {
		ListAvailableIDsBySeriesPage(context.Context, string, int, int) ([]string, error)
	}); ok {
		found := false
		for offset := 0; ; offset += compatBrowseChunkLimit {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			ids, err := repo.ListAvailableIDsBySeriesPage(ctx, seriesID, compatBrowseChunkLimit, offset)
			if err != nil {
				return false, err
			}
			if len(ids) == 0 {
				return found, nil
			}
			found = true
			_, played, err := configuredPlayed(ctx, store, profileID, ids)
			if err != nil {
				return false, err
			}
			for _, id := range ids {
				if !played[id] {
					return false, nil
				}
			}
			if len(ids) < compatBrowseChunkLimit {
				return true, nil
			}
		}
	}
	return false, fmt.Errorf("episode catalog does not support bounded completion queries")
}
