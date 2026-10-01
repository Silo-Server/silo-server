package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5"
)

// resolvePersonalCursor keeps source membership in PostgreSQL so page size,
// rather than the size of a profile's lists, bounds application memory.
func (r *CatalogResolver) resolvePersonalCursor(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	store, err := r.catalogStoreForAccess(ctx, access)
	if err != nil {
		return nil, err
	}
	if !userstore.HasCatalogSQLState(store) {
		return nil, ErrCatalogStorageUnsupported
	}
	if req.Source == CatalogSourceFavorites || req.Source == CatalogSourceWatchlist {
		req = r.resolvePersonalSourceEffectiveSort(ctx, req, access)
	}
	snapshot := time.Now().UTC()
	if req.SnapshotAt != nil {
		snapshot = *req.SnapshotAt
	}
	executor := r.queryExecutorForScope(req.Query.MediaScope, &snapshot)
	executor.SourceArgs = []any{access.UserID, access.ProfileID}
	switch req.Source {
	case CatalogSourceFavorites, CatalogSourceWatchlist:
		table := "user_favorites"
		if req.Source == CatalogSourceWatchlist {
			table = "user_watchlist"
		}
		membership := fmt.Sprintf("FROM %s personal_source WHERE personal_source.user_id=$1 AND personal_source.profile_id=$2 AND personal_source.media_item_id=mi.content_id", table)
		executor.SourceWhere = "EXISTS (SELECT 1 " + membership + ")"
		if req.UseSourceOrder || req.Query.Sort.Field == defaultSortField {
			executor.SourceOrder = personalSourceOrder("(SELECT added_at "+membership+")", req.Query.Sort.Order != querySortAsc)
		}
		if req.Source == CatalogSourceWatchlist {
			hideWatched, err := store.RemoveWatchedFromWatchlist(ctx, access.ProfileID)
			if err != nil {
				return nil, err
			}
			if hideWatched {
				executor.SourceWhere += " AND " + watchlistVisibleSeriesPredicate
			}
		}
	case CatalogSourceHistory:
		applyHistoryCursorSource(executor, req, access, snapshot)
	default:
		return nil, fmt.Errorf("%w: unsupported personal source", ErrInvalidCatalogRequest)
	}
	result, err := resolvePersonalExecutorCursor(ctx, executor, req, access, snapshot)
	if err == nil && (req.Source == CatalogSourceFavorites || req.Source == CatalogSourceWatchlist) {
		result.EffectiveSort = req.Query.Sort
		result.EffectiveSortResolved = true
	}
	return result, err
}

// applyHistoryCursorSource projects the profile's watch history once and joins
// it, rather than re-running it per candidate row.
func applyHistoryCursorSource(executor *QueryExecutor, req CatalogRequest, access AccessFilter, snapshot time.Time) {
	// The history base query aggregates the profile's whole watch history, so it
	// must be evaluated once, not once per candidate row. Project it as a CTE
	// joined on display_id — the same shape the offset path uses
	// (buildHistoryPreviewPagePlan) — and take both membership and the
	// viewed-at ordering key from that join. DISTINCT ON (display_id) makes the
	// join a semi-join, so it filters exactly like the previous EXISTS did.
	base, args := buildHistoryDisplayBaseQuery(access, &snapshot, isEpisodeCatalogScope(req.Query.MediaScope))
	executor.SourceArgs = args
	executor.SourceWhere = ""
	executor.SourceCTE = historyCursorCTE + " AS (" + base + ")"
	executor.SourceJoin = "JOIN " + historyCursorCTE + " " + historyCursorAlias + " ON " + historyCursorAlias + ".display_id = mi.content_id"
	if req.UseSourceOrder || req.Query.Sort.Field == historyDateViewedSort {
		executor.SourceOrder = personalSourceOrder(historyCursorAlias+".watched_at", !historyDateViewedAscending(req))
	}
}

// Available episodes and completion match WatchlistVisibility: a series with
// no available episodes remains visible, and a newly available unwatched
// episode makes a previously completed series visible again.
const watchlistVisibleSeriesPredicate = `(mi.type <> 'series' OR NOT EXISTS (
 SELECT 1 FROM episodes ep WHERE ep.series_id=mi.content_id
 AND EXISTS (SELECT 1 FROM episode_libraries el WHERE el.episode_id=ep.content_id)
) OR EXISTS (
 SELECT 1 FROM episodes ep WHERE ep.series_id=mi.content_id
 AND EXISTS (SELECT 1 FROM episode_libraries el WHERE el.episode_id=ep.content_id)
 AND NOT EXISTS (SELECT 1 FROM user_watch_progress progress WHERE progress.user_id=$1 AND progress.profile_id=$2 AND progress.media_item_id=ep.content_id AND progress.completed
 AND NOT EXISTS (SELECT 1 FROM user_history_hidden_items hidden WHERE hidden.user_id=progress.user_id AND hidden.profile_id=progress.profile_id AND hidden.media_item_id=progress.media_item_id AND progress.updated_at <= hidden.hidden_before))
))`

// historyCursorCTE and historyCursorAlias name the projected history relation
// on the keyset path. They are internal SQL identifiers, never user input.
const (
	historyCursorCTE   = "history_display"
	historyCursorAlias = "history_source"
)

func personalSourceOrder(timestamp string, descending bool) []queryCursorTerm {
	return []queryCursorTerm{
		{expression: timestamp, kind: cursorKindTimestamp, descending: descending, nullsLast: true},
		{expression: cursorContentIDExpression, kind: cursorKindText, nullsLast: true},
	}
}

func (r *CatalogResolver) resolvePersonCursor(ctx context.Context, req CatalogRequest, access AccessFilter) (*CatalogResult, error) {
	if err := r.requireQueryStore(ctx, req.Query, access); err != nil {
		return nil, err
	}
	snapshot := time.Now().UTC()
	if req.SnapshotAt != nil {
		snapshot = *req.SnapshotAt
	}
	executor := r.queryExecutorForScope(req.Query.MediaScope, &snapshot)
	executor.SourceWhere = "EXISTS (SELECT 1 FROM item_people person_source WHERE person_source.person_id=$1 AND person_source.content_id=mi.content_id)"
	executor.SourceArgs = []any{req.PersonID}
	return resolvePersonalExecutorCursor(ctx, executor, req, access, snapshot)
}

func resolvePersonalExecutorCursor(ctx context.Context, executor *QueryExecutor, req CatalogRequest, access AccessFilter, snapshot time.Time) (*CatalogResult, error) {
	executor.GroupByWork = req.GroupByWork
	applyCollectionSearchPredicate(executor, req.SearchQuery)
	access.NamePrefix = strings.TrimSpace(req.NamePrefix)
	after := req.After
	if after != nil && len(after.Keys) == 0 {
		after = nil
	}
	if req.Seek != nil {
		var err error
		after, err = executor.SeekCursor(ctx, req.Query, access, *req.Seek)
		if errors.Is(err, pgx.ErrNoRows) {
			return &CatalogResult{Items: []*models.MediaItem{}, SnapshotAt: snapshot}, nil
		}
		if err != nil {
			return nil, err
		}
	}
	page, err := executor.PreviewCursorPage(ctx, req.Query, access, req.Limit, after, !req.SkipTotal)
	if err != nil {
		return nil, err
	}
	return &CatalogResult{Items: page.Items, Total: page.Total, TotalExact: page.TotalExact, HasMore: page.HasMore, Next: page.Next, SnapshotAt: snapshot}, nil
}
