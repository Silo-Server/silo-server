package watchlist

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/contentid"
)

// Waiter is a profile whose watchlist held a title the library did not have
// when the profile added it, and which a catalog item now matches.
type Waiter struct {
	UserID    int
	ProfileID string
	// MediaType and TMDBIDs name the title: its current TMDB ID first, then
	// the ones TMDB replaced. TMDBIDs is empty when neither the title nor the
	// item carries a TMDB ID.
	MediaType string
	TMDBIDs   []int
}

// Querier is the subset of pgx.Tx / pgxpool.Pool the arrival reads need, so
// the notification fanout can run them inside its transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Arrivals finds the profiles waiting for a catalog item. The notification
// system tells them the title arrived. It holds no state: its reads run on
// the caller's querier.
type Arrivals struct{}

// WaitingFor returns, for each of the catalog items, the profiles waiting for
// it. Items no profile waits for are absent. Only an item created at or after
// arrivedSince has arrived: a later episode of a series the library has had
// for longer is no arrival, so it cannot repeat the notice once delivery
// retention removed the first one. A profile waits for an arrived item when
// either:
//
//   - a watchlist title entry resolves to the item the way promotion resolves
//     it (catalog.ResolveProviderAliases: any provider ID the title holds or
//     once held, an ID set that does not contradict the item's, an item in an
//     enabled library); or
//   - promotion already moved such an entry onto the library watchlist,
//     recorded in watchlist_title_promotions with the entry's added_at, which
//     predates the item.
//
// The second rule keeps a profile that reads its watchlist between the item's
// arrival and the notification fanout from losing the notice. It reads the
// promotion record rather than the library watchlist, which may live in a
// per-user SQLite store this query cannot reach.
//
// contentIDs is expected to be one fanout claim's items (at most 100): they
// go to the database as one array, and each arrived item costs its own reads.
func (a *Arrivals) WaitingFor(ctx context.Context, q Querier, contentIDs []string, arrivedSince time.Time) (map[string][]Waiter, error) {
	out := make(map[string][]Waiter)
	if a == nil || len(contentIDs) == 0 {
		return out, nil
	}
	arrived, err := arrivedItems(ctx, q, contentIDs, arrivedSince)
	if err != nil {
		return nil, err
	}
	for _, contentID := range arrived {
		waiters, err := waitingFor(ctx, q, contentID)
		if err != nil {
			return nil, err
		}
		if len(waiters) > 0 {
			out[contentID] = waiters
		}
	}
	return out, nil
}

// PrunePromotions deletes promotion records older than before. The arrival
// read only counts an item created at or after its horizon, and a promotion
// happens after its item exists, so a record promoted before the horizon can
// no longer match. The notification retention pass calls it with the fanout
// staleness horizon.
func (a *Arrivals) PrunePromotions(ctx context.Context, q Querier, before time.Time) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM watchlist_title_promotions WHERE promoted_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("pruning watchlist title promotions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// arrivedItems keeps the catalog items created at or after arrivedSince, in
// one query for the whole batch. It keeps none while no profile has a title
// entry or a promotion record, so a bulk import on a server where nobody
// watchlists unavailable titles costs this query alone. The per-item reads in
// waitingFor then run only for fresh items, at most one per claimed event.
func arrivedItems(ctx context.Context, q Querier, contentIDs []string, arrivedSince time.Time) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT content_id FROM media_items
		WHERE content_id = ANY($1) AND created_at >= $2
		  AND (EXISTS (SELECT 1 FROM user_watchlist_titles)
		       OR EXISTS (SELECT 1 FROM watchlist_title_promotions))`, contentIDs, arrivedSince)
	if err != nil {
		return nil, fmt.Errorf("reading arrived catalog items: %w", err)
	}
	fresh, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("reading arrived catalog items: %w", err)
	}
	// Keep the caller's order.
	arrived := make([]string, 0, len(fresh))
	for _, contentID := range contentIDs {
		if slices.Contains(fresh, contentID) && !slices.Contains(arrived, contentID) {
			arrived = append(arrived, contentID)
		}
	}
	return arrived, nil
}

// recordPromotion notes, in the promotion's transaction, that a title entry
// added at titleAddedAt moved onto the item. It keeps every TMDB ID the title
// holds or once held: the item may not carry a former one, and a request made
// under it must still be found once the title row is gone. A second title
// promoted onto the same item adds its IDs and keeps the earlier entry time.
func recordPromotion(ctx context.Context, q Querier, userID int, profileID string, titleID int64, contentID string, titleAddedAt time.Time) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO watchlist_title_promotions (user_id, profile_id, media_item_id, title_added_at, title_tmdb_ids)
		VALUES ($1, $2, $3, $4, ARRAY(
			SELECT t.tmdb_id FROM watchlist_titles t WHERE t.id = $5
			UNION
			SELECT ids.id::integer FROM (
				SELECT CASE WHEN a.provider_id ~ '^[0-9]{1,10}$' THEN a.provider_id::bigint END AS id
				FROM watchlist_title_aliases a
				WHERE a.title_id = $5 AND a.provider = 'tmdb') ids
			WHERE ids.id BETWEEN 1 AND 2147483647))
		ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE
		SET title_tmdb_ids = ARRAY(
		        SELECT DISTINCT unnest(watchlist_title_promotions.title_tmdb_ids || EXCLUDED.title_tmdb_ids)),
		    title_added_at = LEAST(watchlist_title_promotions.title_added_at, EXCLUDED.title_added_at)`,
		userID, profileID, contentID, titleAddedAt, titleID); err != nil {
		return fmt.Errorf("recording watchlist title promotion: %w", err)
	}
	return nil
}

// ForgetPromotion deletes the record of a title entry promoted onto the item,
// once the item leaves the profile's library watchlist: the profile no longer
// waits for the title. The notification system calls it from its user store
// wrapper, so a removal through the API, a watch provider sync or the
// watched-movie cleanup all reach it.
func ForgetPromotion(ctx context.Context, q Querier, userID int, profileID, contentID string) error {
	if _, err := q.Exec(ctx, `
		DELETE FROM watchlist_title_promotions
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`, userID, profileID, contentID); err != nil {
		return fmt.Errorf("forgetting watchlist title promotion: %w", err)
	}
	return nil
}

func waitingFor(ctx context.Context, q Querier, contentID string) ([]Waiter, error) {
	mediaType, itemAliases, err := catalog.ItemProviderAliasesOn(ctx, q, contentID)
	if err != nil || len(itemAliases) == 0 {
		return nil, err
	}
	waiters, err := unpromotedWaiters(ctx, q, contentID, mediaType, itemAliases)
	if err != nil {
		return nil, err
	}
	promoted, err := promotedWaiters(ctx, q, contentID, mediaType, itemAliases)
	if err != nil {
		return nil, err
	}
	return append(waiters, promoted...), nil
}

// unpromotedWaiters returns the profiles with a title entry, added before the
// item existed, that resolves to the item. It finds the entries whose title shares any provider ID with the
// item, then keeps the titles that promotion's resolution would move onto
// this item.
func unpromotedWaiters(ctx context.Context, q Querier, contentID, mediaType string, itemAliases []catalog.ProviderAlias) ([]Waiter, error) {
	providers := make([]string, len(itemAliases))
	providerIDs := make([]string, len(itemAliases))
	for i, alias := range itemAliases {
		providers[i], providerIDs[i] = alias.Provider, alias.ProviderID
	}
	rows, err := q.Query(ctx, `
		WITH matched AS (
			SELECT DISTINCT a.title_id
			FROM unnest($2::text[], $3::text[]) AS v(provider, provider_id)
			JOIN watchlist_title_aliases a
			  ON a.media_type = $1 AND a.provider = v.provider AND a.provider_id = v.provider_id
		)
		SELECT m.title_id, t.tmdb_id,
		       array_agg(ta.provider ORDER BY ta.provider, ta.provider_id),
		       array_agg(ta.provider_id ORDER BY ta.provider, ta.provider_id)
		FROM matched m
		JOIN watchlist_titles t ON t.id = m.title_id
		JOIN watchlist_title_aliases ta ON ta.title_id = m.title_id
		GROUP BY m.title_id, t.tmdb_id
		ORDER BY m.title_id`,
		mediaType, providers, providerIDs)
	if err != nil {
		return nil, fmt.Errorf("matching watchlist titles to %s: %w", contentID, err)
	}
	// titleTMDBIDs holds each matched title's TMDB IDs, its current one first.
	titleTMDBIDs := make(map[int64][]int)
	var requested []catalog.ProviderAlias
	for rows.Next() {
		var titleID int64
		var currentTMDB int
		var aliasProviders, aliasIDs []string
		if err := rows.Scan(&titleID, &currentTMDB, &aliasProviders, &aliasIDs); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning matched watchlist title: %w", err)
		}
		tmdbIDs := []int{currentTMDB}
		for i, provider := range aliasProviders {
			requested = append(requested, catalog.ProviderAlias{Key: titleID, MediaType: mediaType, Provider: provider, ProviderID: aliasIDs[i]})
			if provider != contentid.ProviderTMDB {
				continue
			}
			if id, err := strconv.Atoi(aliasIDs[i]); err == nil && !slices.Contains(tmdbIDs, id) {
				tmdbIDs = append(tmdbIDs, id)
			}
		}
		titleTMDBIDs[titleID] = tmdbIDs
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("matching watchlist titles to %s: %w", contentID, err)
	}
	if len(titleTMDBIDs) == 0 {
		return nil, nil
	}
	resolved, err := catalog.ResolveProviderAliasesOn(ctx, q, requested)
	if err != nil {
		return nil, err
	}
	var titleIDs []int64
	for titleID, items := range resolved {
		if slices.Contains(items, contentID) {
			titleIDs = append(titleIDs, titleID)
		}
	}
	if len(titleIDs) == 0 {
		return nil, nil
	}
	slices.Sort(titleIDs)
	// An entry added after the item existed was not waiting for it.
	rows, err = q.Query(ctx, `
		SELECT e.title_id, e.user_id, e.profile_id
		FROM user_watchlist_titles e
		JOIN media_items i ON i.content_id = $2
		WHERE e.title_id = ANY($1) AND e.added_at < i.created_at
		ORDER BY e.user_id, e.profile_id, e.title_id`, titleIDs, contentID)
	if err != nil {
		return nil, fmt.Errorf("listing watchlist entries waiting for %s: %w", contentID, err)
	}
	defer rows.Close()
	var waiters []Waiter
	for rows.Next() {
		var titleID int64
		w := Waiter{MediaType: mediaType}
		if err := rows.Scan(&titleID, &w.UserID, &w.ProfileID); err != nil {
			return nil, fmt.Errorf("scanning watchlist waiter: %w", err)
		}
		w.TMDBIDs = slices.Clone(titleTMDBIDs[titleID])
		waiters = append(waiters, w)
	}
	return waiters, rows.Err()
}

// promotedWaiters returns the profiles whose entry promotion already moved
// onto the item, when the entry predates the item. The title row may be gone
// by then, so the item's TMDB IDs name the title, its current one first,
// joined by the IDs the promotion kept from the title.
func promotedWaiters(ctx context.Context, q Querier, contentID, mediaType string, itemAliases []catalog.ProviderAlias) ([]Waiter, error) {
	rows, err := q.Query(ctx, `
		SELECT p.user_id, p.profile_id, i.tmdb_id, p.title_tmdb_ids
		FROM watchlist_title_promotions p
		JOIN media_items i ON i.content_id = p.media_item_id
		WHERE p.media_item_id = $1 AND p.title_added_at < i.created_at
		ORDER BY p.user_id, p.profile_id`,
		contentID)
	if err != nil {
		return nil, fmt.Errorf("listing promoted watchlist entries for %s: %w", contentID, err)
	}
	defer rows.Close()
	var itemTMDBIDs []int
	for _, alias := range itemAliases {
		if alias.Provider != contentid.ProviderTMDB {
			continue
		}
		if id, err := strconv.Atoi(alias.ProviderID); err == nil && !slices.Contains(itemTMDBIDs, id) {
			itemTMDBIDs = append(itemTMDBIDs, id)
		}
	}
	var waiters []Waiter
	for rows.Next() {
		w := Waiter{MediaType: mediaType}
		var currentTMDB *string
		var titleTMDBIDs []int
		if err := rows.Scan(&w.UserID, &w.ProfileID, &currentTMDB, &titleTMDBIDs); err != nil {
			return nil, fmt.Errorf("scanning promoted watchlist waiter: %w", err)
		}
		w.TMDBIDs = currentFirst(itemTMDBIDs, currentTMDB)
		for _, id := range titleTMDBIDs {
			if !slices.Contains(w.TMDBIDs, id) {
				w.TMDBIDs = append(w.TMDBIDs, id)
			}
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
