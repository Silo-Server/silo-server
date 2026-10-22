package watchlist

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/contentid"
)

// Waiter is a profile whose watchlist held a title the library did not have
// when the profile added it, and which a catalog item now matches.
type Waiter struct {
	UserID    int
	ProfileID string
	// MediaType and TMDBIDs name the title: its current TMDB ID first, then
	// the ones TMDB replaced. TMDBIDs is empty when the item carries no TMDB
	// ID.
	MediaType string
	TMDBIDs   []int
}

// Querier is the subset of pgx.Tx / pgxpool.Pool the arrival reads need, so
// the notification fanout can run them inside its transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Arrivals finds the profiles waiting for a catalog item. The notification
// system tells them the title arrived. It holds no state: its reads run on
// the caller's querier.
type Arrivals struct{}

// WaitingFor returns, for each of the catalog items, the profiles waiting for
// it. Items no profile waits for are absent. A profile waits for an item when
// either:
//
//   - a watchlist entry's title carries any provider ID the item carries or
//     once carried, the matching promotion uses; or
//   - promotion already moved such an entry onto the library watchlist. The
//     library entry keeps the title entry's added_at, so it predates the item.
//     Only items created at or after arrivedSince count, so a later episode of
//     a series promoted long ago is no arrival.
//
// The second rule keeps a profile that reads its watchlist between the
// item's arrival and the notification fanout from losing the notice.
func (a *Arrivals) WaitingFor(ctx context.Context, q Querier, contentIDs []string, arrivedSince time.Time) (map[string][]Waiter, error) {
	out := make(map[string][]Waiter)
	if a == nil || len(contentIDs) == 0 {
		return out, nil
	}
	for _, contentID := range contentIDs {
		waiters, err := waitingFor(ctx, q, contentID, arrivedSince)
		if err != nil {
			return nil, err
		}
		if len(waiters) > 0 {
			out[contentID] = waiters
		}
	}
	return out, nil
}

func waitingFor(ctx context.Context, q Querier, contentID string, arrivedSince time.Time) ([]Waiter, error) {
	mediaType, aliases, err := catalog.ReadItemProviderAliases(ctx, q, contentID)
	if err != nil || len(aliases) == 0 {
		return nil, err
	}
	providers := make([]string, len(aliases))
	providerIDs := make([]string, len(aliases))
	var itemTMDBIDs []int
	for i, alias := range aliases {
		providers[i], providerIDs[i] = alias.Provider, alias.ProviderID
		if alias.Provider == contentid.ProviderTMDB {
			if id, err := strconv.Atoi(alias.ProviderID); err == nil {
				itemTMDBIDs = append(itemTMDBIDs, id)
			}
		}
	}
	rows, err := q.Query(ctx, `
		WITH matched AS (
			SELECT DISTINCT a.title_id
			FROM unnest($2::text[], $3::text[]) AS v(provider, provider_id)
			JOIN watchlist_title_aliases a
			  ON a.media_type = $1 AND a.provider = v.provider AND a.provider_id = v.provider_id
		)
		SELECT e.user_id, e.profile_id,
		       t.tmdb_id || coalesce((SELECT array_agg(fa.provider_id::int ORDER BY fa.provider_id::int)
		                              FROM watchlist_title_aliases fa
		                              WHERE fa.title_id = t.id AND fa.provider = 'tmdb'
		                                AND fa.provider_id <> t.tmdb_id::text), '{}'),
		       NULL::text
		FROM matched m
		JOIN watchlist_titles t ON t.id = m.title_id
		JOIN user_watchlist_titles e ON e.title_id = m.title_id
		UNION ALL
		SELECT w.user_id, w.profile_id, NULL::int[], i.tmdb_id
		FROM user_watchlist w
		JOIN media_items i ON i.content_id = w.media_item_id
		WHERE w.media_item_id = $4 AND w.added_at < i.created_at AND i.created_at >= $5
		ORDER BY 1, 2`,
		mediaType, providers, providerIDs, contentID, arrivedSince)
	if err != nil {
		return nil, fmt.Errorf("matching watchlist entries to %s: %w", contentID, err)
	}
	defer rows.Close()
	var waiters []Waiter
	for rows.Next() {
		w := Waiter{MediaType: mediaType}
		var currentTMDB *string
		if err := rows.Scan(&w.UserID, &w.ProfileID, &w.TMDBIDs, &currentTMDB); err != nil {
			return nil, fmt.Errorf("scanning watchlist waiter: %w", err)
		}
		if w.TMDBIDs == nil {
			w.TMDBIDs = currentFirst(itemTMDBIDs, currentTMDB)
		}
		waiters = append(waiters, w)
	}
	return waiters, rows.Err()
}

// currentFirst orders an item's TMDB IDs with its current one first.
func currentFirst(ids []int, current *string) []int {
	out := slices.Clone(ids)
	if current == nil {
		return out
	}
	id, err := strconv.Atoi(*current)
	if err != nil {
		return out
	}
	if i := slices.Index(out, id); i > 0 {
		out = append([]int{id}, slices.Delete(out, i, i+1)...)
	}
	return out
}
