package watchlist

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Waiter is a profile whose watchlist holds a title the library did not have
// when the profile added it, and which a catalog item now matches.
type Waiter struct {
	UserID    int
	ProfileID string
	// MediaType and TMDBIDs name the title: its current TMDB ID first, then
	// the ones TMDB replaced.
	MediaType string
	TMDBIDs   []int
}

// itemAliases is the slice of the catalog arrival matching needs.
type itemAliases interface {
	ItemProviderAliases(ctx context.Context, contentID string) (string, []catalog.ProviderAlias, error)
}

// Arrivals finds the profiles waiting for a catalog item: those with a
// watchlist entry whose title carries any provider ID the item carries or once
// carried, the matching promotion uses. The notification system tells them
// the title arrived.
type Arrivals struct {
	pool  *pgxpool.Pool
	items itemAliases
}

// NewArrivals creates an Arrivals.
func NewArrivals(pool *pgxpool.Pool, items *catalog.ItemRepository) *Arrivals {
	return &Arrivals{pool: pool, items: items}
}

// WaitingFor returns, for each of the catalog items, the profiles waiting for
// it. Items no entry matches are absent. While no profile has an entry it
// costs one index probe.
func (a *Arrivals) WaitingFor(ctx context.Context, contentIDs []string) (map[string][]Waiter, error) {
	out := make(map[string][]Waiter)
	if a == nil || len(contentIDs) == 0 {
		return out, nil
	}
	var hasEntries bool
	if err := a.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_watchlist_titles)`).Scan(&hasEntries); err != nil {
		return nil, fmt.Errorf("probing watchlist title entries: %w", err)
	}
	if !hasEntries {
		return out, nil
	}
	for _, contentID := range contentIDs {
		waiters, err := a.waitingFor(ctx, contentID)
		if err != nil {
			return nil, err
		}
		if len(waiters) > 0 {
			out[contentID] = waiters
		}
	}
	return out, nil
}

func (a *Arrivals) waitingFor(ctx context.Context, contentID string) ([]Waiter, error) {
	mediaType, aliases, err := a.items.ItemProviderAliases(ctx, contentID)
	if err != nil || len(aliases) == 0 {
		return nil, err
	}
	providers := make([]string, len(aliases))
	providerIDs := make([]string, len(aliases))
	for i, alias := range aliases {
		providers[i], providerIDs[i] = alias.Provider, alias.ProviderID
	}
	rows, err := a.pool.Query(ctx, `
		WITH matched AS (
			SELECT DISTINCT a.title_id
			FROM unnest($2::text[], $3::text[]) AS v(provider, provider_id)
			JOIN watchlist_title_aliases a
			  ON a.media_type = $1 AND a.provider = v.provider AND a.provider_id = v.provider_id
		)
		SELECT e.user_id, e.profile_id, t.media_type,
		       t.tmdb_id || coalesce((SELECT array_agg(fa.provider_id::int ORDER BY fa.provider_id::int)
		                              FROM watchlist_title_aliases fa
		                              WHERE fa.title_id = t.id AND fa.provider = 'tmdb'
		                                AND fa.provider_id <> t.tmdb_id::text), '{}')
		FROM matched m
		JOIN watchlist_titles t ON t.id = m.title_id
		JOIN user_watchlist_titles e ON e.title_id = m.title_id
		ORDER BY e.user_id, e.profile_id`,
		mediaType, providers, providerIDs)
	if err != nil {
		return nil, fmt.Errorf("matching watchlist titles to %s: %w", contentID, err)
	}
	defer rows.Close()
	var waiters []Waiter
	for rows.Next() {
		var w Waiter
		if err := rows.Scan(&w.UserID, &w.ProfileID, &w.MediaType, &w.TMDBIDs); err != nil {
			return nil, fmt.Errorf("scanning watchlist title waiter: %w", err)
		}
		waiters = append(waiters, w)
	}
	return waiters, rows.Err()
}
