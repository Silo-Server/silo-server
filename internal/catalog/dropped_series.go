package catalog

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// A dropped series is hidden from a profile's Next Up and Continue Watching
// while its drop is active: until any episode of the series has watch
// progress, or a history-hide stamp (mark unwatched, remove from history),
// newer than dropped_at. Watching the series again therefore undrops it
// without any write, whichever path recorded the watch. Imported history keeps
// its original watch time, so re-importing old plays never undrops a series.
//
// The rule reads user_watch_progress in Postgres, so profiles whose watch
// state lives in the SQLite user store never auto-undrop.
//
// droppedSeriesStateQuery lists a profile's drops ($1 user, $2 profile) with
// whether each is active. It reads only the profile's progress newer than its
// oldest drop and probes episodes by content id, so a profile without drops
// costs one empty index read and a large library is never scanned. seriesFilter
// narrows the drops, for example to "AND series_id = ANY($3)".
func droppedSeriesStateQuery(seriesFilter string) string {
	return `
		WITH d AS MATERIALIZED (
			SELECT series_id, dropped_at FROM user_dropped_series
			WHERE user_id = $1 AND profile_id = $2 ` + seriesFilter + `
		), recent AS (
			SELECT media_item_id, updated_at FROM user_watch_progress
			WHERE user_id = $1 AND profile_id = $2 AND updated_at > (SELECT min(dropped_at) FROM d)
			UNION ALL
			SELECT media_item_id, updated_at FROM user_history_hidden_items
			WHERE user_id = $1 AND profile_id = $2 AND updated_at > (SELECT min(dropped_at) FROM d)
		), activity AS (
			SELECT e.series_id, max(r.updated_at) AS at
			FROM recent r JOIN episodes e ON e.content_id = r.media_item_id
			WHERE e.series_id = ANY(ARRAY(SELECT series_id FROM d))
			GROUP BY e.series_id
		)
		SELECT d.series_id, d.dropped_at, (a.at IS NULL OR a.at <= d.dropped_at) AS active
		FROM d LEFT JOIN activity a ON a.series_id = d.series_id`
}

// DroppedSeries is one dropped-series row. Active is false once the profile
// watched the series after dropping it; an inactive row means the same as no
// row and is left for watch sync to clean up.
type DroppedSeries struct {
	SeriesID  string
	DroppedAt time.Time
	Active    bool
}

// DroppedSeriesRepo stores the series each profile dropped.
type DroppedSeriesRepo struct {
	pool *pgxpool.Pool
}

// NewDroppedSeriesRepo creates a DroppedSeriesRepo.
func NewDroppedSeriesRepo(pool *pgxpool.Pool) *DroppedSeriesRepo {
	return &DroppedSeriesRepo{pool: pool}
}

// ResolveDropSeries returns the series a Home dismissal of itemID drops: the
// parent series of an episode, or the item itself when it is a series. ok is
// false for every other item, which keeps its per-item dismissal.
func (r *DroppedSeriesRepo) ResolveDropSeries(ctx context.Context, itemID string) (string, bool, error) {
	var seriesID *string
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(
			(SELECT series_id FROM episodes WHERE content_id = $1),
			(SELECT content_id FROM media_items WHERE content_id = $1 AND type = 'series'))`,
		itemID,
	).Scan(&seriesID)
	if err != nil {
		return "", false, fmt.Errorf("resolving dropped series: %w", err)
	}
	if seriesID == nil || *seriesID == "" {
		return "", false, nil
	}
	return *seriesID, true, nil
}

// EpisodeSeriesIDs returns the series of each of the given episodes, in one
// query per thousand IDs. IDs that are not episodes are absent.
func (r *DroppedSeriesRepo) EpisodeSeriesIDs(ctx context.Context, episodeIDs []string) (map[string]string, error) {
	seriesOf := make(map[string]string, len(episodeIDs))
	for start := 0; start < len(episodeIDs); start += 1000 {
		chunk := episodeIDs[start:min(len(episodeIDs), start+1000)]
		rows, err := r.pool.Query(ctx,
			`SELECT content_id, series_id FROM episodes WHERE content_id = ANY($1) AND series_id IS NOT NULL AND series_id <> ''`,
			chunk)
		if err != nil {
			return nil, fmt.Errorf("resolving episode series: %w", err)
		}
		for rows.Next() {
			var episodeID, seriesID string
			if err := rows.Scan(&episodeID, &seriesID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scanning episode series: %w", err)
			}
			seriesOf[episodeID] = seriesID
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterating episode series: %w", err)
		}
	}
	return seriesOf, nil
}

// Drop drops a series for a profile now. Dropping an already dropped series
// refreshes dropped_at, which re-drops a series the profile watched since.
func (r *DroppedSeriesRepo) Drop(ctx context.Context, userID int, profileID, seriesID string) error {
	if err := r.pruneEnded(ctx, userID, profileID, seriesID); err != nil {
		return err
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO user_dropped_series (user_id, profile_id, series_id, dropped_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (user_id, profile_id, series_id) DO UPDATE SET dropped_at = EXCLUDED.dropped_at`,
		userID, profileID, seriesID,
	)
	if err != nil {
		return fmt.Errorf("dropping series: %w", err)
	}
	return nil
}

// Undrop removes a profile's drop of a series. Removing an absent drop succeeds.
func (r *DroppedSeriesRepo) Undrop(ctx context.Context, userID int, profileID, seriesID string) error {
	if err := r.pruneEnded(ctx, userID, profileID, seriesID); err != nil {
		return err
	}
	_, err := r.pool.Exec(ctx, `
		DELETE FROM user_dropped_series
		WHERE user_id = $1 AND profile_id = $2 AND series_id = $3`,
		userID, profileID, seriesID,
	)
	if err != nil {
		return fmt.Errorf("undropping series: %w", err)
	}
	return nil
}

// pruneEnded deletes the profile's drops that ended because the profile
// watched the series again, other than keepSeriesID. Watch sync removes them
// too, but a profile without a provider connection would otherwise keep them
// forever, and the oldest drop bounds how much progress the active-drop query
// reads. Rows a provider still has to be told about are kept: their agreed
// row in watch_provider_dropped_items needs the undrop sent first.
func (r *DroppedSeriesRepo) pruneEnded(ctx context.Context, userID int, profileID, keepSeriesID string) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM user_dropped_series t
		USING (`+droppedSeriesStateQuery("")+`) s
		WHERE t.user_id = $1 AND t.profile_id = $2 AND t.series_id = s.series_id
		  AND t.dropped_at = s.dropped_at AND NOT s.active AND t.series_id <> $3
		  AND NOT EXISTS (
			SELECT 1 FROM watch_provider_dropped_items w
			JOIN watch_provider_connections c ON c.id = w.connection_id
			WHERE c.user_id = $1 AND c.profile_id = $2 AND w.series_id = t.series_id
		  )`,
		userID, profileID, keepSeriesID,
	)
	if err != nil {
		return fmt.Errorf("pruning ended drops: %w", err)
	}
	return nil
}

// PurgeProfile deletes a deleted profile's drops. The table has no profile
// foreign key because profiles may live in the SQLite user store.
func (r *DroppedSeriesRepo) PurgeProfile(ctx context.Context, userID int, profileID string) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM user_dropped_series WHERE user_id = $1 AND profile_id = $2`, userID, profileID); err != nil {
		return fmt.Errorf("purging profile drops: %w", err)
	}
	return nil
}

// ActiveSeriesIDs returns the series whose drop is active for a profile.
func (r *DroppedSeriesRepo) ActiveSeriesIDs(ctx context.Context, userID int, profileID string) ([]string, error) {
	return activeDroppedSeriesIDs(ctx, r.pool, userID, profileID)
}

func activeDroppedSeriesIDs(ctx context.Context, pool *pgxpool.Pool, userID int, profileID string) ([]string, error) {
	if pool == nil || userID <= 0 || profileID == "" {
		return nil, nil
	}
	rows, err := pool.Query(ctx,
		`SELECT series_id FROM (`+droppedSeriesStateQuery("")+`) s WHERE s.active`,
		userID, profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing active dropped series: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("scanning active dropped series: %w", err)
	}
	return ids, nil
}

// ListDropped returns a profile's dropped-series rows, active or not. A nil
// seriesIDs lists every row; otherwise only the named series.
func (r *DroppedSeriesRepo) ListDropped(ctx context.Context, userID int, profileID string, seriesIDs []string) ([]DroppedSeries, error) {
	query := droppedSeriesStateQuery("")
	args := []any{userID, profileID}
	if seriesIDs != nil {
		query = droppedSeriesStateQuery("AND series_id = ANY($3)")
		args = append(args, seriesIDs)
	}
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing dropped series: %w", err)
	}
	dropped, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DroppedSeries, error) {
		var d DroppedSeries
		err := row.Scan(&d.SeriesID, &d.DroppedAt, &d.Active)
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("scanning dropped series: %w", err)
	}
	return dropped, nil
}

// LatestActivity returns, per series, the newest progress or history-hide
// stamp the profile has on any of its episodes. Series without activity are
// absent.
func (r *DroppedSeriesRepo) LatestActivity(ctx context.Context, userID int, profileID string, seriesIDs []string) (map[string]time.Time, error) {
	latest := make(map[string]time.Time)
	if len(seriesIDs) == 0 {
		return latest, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT e.series_id, max(a.updated_at)
		FROM (
			SELECT media_item_id, updated_at FROM user_watch_progress
			WHERE user_id = $1 AND profile_id = $2
			UNION ALL
			SELECT media_item_id, updated_at FROM user_history_hidden_items
			WHERE user_id = $1 AND profile_id = $2
		) a
		JOIN episodes e ON e.content_id = a.media_item_id
		WHERE e.series_id = ANY($3)
		GROUP BY e.series_id`,
		userID, profileID, seriesIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("listing dropped series activity: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seriesID string
		var at time.Time
		if err := rows.Scan(&seriesID, &at); err != nil {
			return nil, fmt.Errorf("scanning dropped series activity: %w", err)
		}
		latest[seriesID] = at
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating dropped series activity: %w", err)
	}
	return latest, nil
}

// ImportDrop records a drop observed on a watch provider, but only if the
// profile's row is still the one the caller read: observed is the dropped_at
// read earlier, or nil when there was no row. It reports whether the write
// applied; false means a concurrent change won.
func (r *DroppedSeriesRepo) ImportDrop(ctx context.Context, userID int, profileID, seriesID string, droppedAt time.Time, observed *time.Time) (bool, error) {
	var tag pgconn.CommandTag
	var err error
	if observed == nil {
		tag, err = r.pool.Exec(ctx, `
			INSERT INTO user_dropped_series (user_id, profile_id, series_id, dropped_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id, profile_id, series_id) DO NOTHING`,
			userID, profileID, seriesID, droppedAt,
		)
	} else {
		tag, err = r.pool.Exec(ctx, `
			UPDATE user_dropped_series SET dropped_at = $4
			WHERE user_id = $1 AND profile_id = $2 AND series_id = $3 AND dropped_at = $5`,
			userID, profileID, seriesID, droppedAt, *observed,
		)
	}
	if err != nil {
		return false, fmt.Errorf("importing dropped series: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// DeleteIfUnchanged removes a drop the provider no longer holds, but only if
// its dropped_at is still the observed one. It reports whether a row was
// removed.
func (r *DroppedSeriesRepo) DeleteIfUnchanged(ctx context.Context, userID int, profileID, seriesID string, observed time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM user_dropped_series
		WHERE user_id = $1 AND profile_id = $2 AND series_id = $3 AND dropped_at = $4`,
		userID, profileID, seriesID, observed,
	)
	if err != nil {
		return false, fmt.Errorf("deleting dropped series: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// DeleteInactive removes the named rows whose drop is no longer active. A row
// the profile re-dropped in the meantime has a new dropped_at and survives.
func (r *DroppedSeriesRepo) DeleteInactive(ctx context.Context, userID int, profileID string, seriesIDs []string) error {
	if len(seriesIDs) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		DELETE FROM user_dropped_series t
		USING (`+droppedSeriesStateQuery("AND series_id = ANY($3)")+`) s
		WHERE t.user_id = $1 AND t.profile_id = $2 AND t.series_id = s.series_id
		  AND t.dropped_at = s.dropped_at AND NOT s.active`,
		userID, profileID, seriesIDs,
	)
	if err != nil {
		return fmt.Errorf("deleting inactive dropped series: %w", err)
	}
	return nil
}

// DroppedSeriesSet is one request's snapshot of a profile's active drops, used
// to filter progress entries page by page without re-reading the drops.
type DroppedSeriesSet struct {
	pool      *pgxpool.Pool
	seriesIDs []string
}

// Empty reports whether the profile has no active drop.
func (s DroppedSeriesSet) Empty() bool { return len(s.seriesIDs) == 0 }

// FilterProgress removes the episode entries whose series is dropped.
func (s DroppedSeriesSet) FilterProgress(ctx context.Context, entries []userstore.WatchProgress) ([]userstore.WatchProgress, error) {
	if s.Empty() || s.pool == nil || len(entries) == 0 {
		return entries, nil
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.MediaItemID)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT content_id FROM episodes
		WHERE content_id = ANY($1) AND series_id = ANY($2)`,
		ids, s.seriesIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("querying dropped series episodes: %w", err)
	}
	dropped, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("scanning dropped series episodes: %w", err)
	}
	if len(dropped) == 0 {
		return entries, nil
	}
	hidden := make(map[string]struct{}, len(dropped))
	for _, id := range dropped {
		hidden[id] = struct{}{}
	}
	filtered := make([]userstore.WatchProgress, 0, len(entries)-len(hidden))
	for _, entry := range entries {
		if _, ok := hidden[entry.MediaItemID]; !ok {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}

// ActiveDroppedSeries loads a profile's active drops for filtering Continue
// Watching entries. A filter without a pool returns an empty set.
func (f *ContinueWatchingProgressFilter) ActiveDroppedSeries(ctx context.Context, userID int, profileID string) (DroppedSeriesSet, error) {
	if f == nil || f.pool == nil {
		return DroppedSeriesSet{}, nil
	}
	ids, err := activeDroppedSeriesIDs(ctx, f.pool, userID, profileID)
	if err != nil {
		return DroppedSeriesSet{}, err
	}
	return DroppedSeriesSet{pool: f.pool, seriesIDs: ids}, nil
}

// FilterDroppedProgress removes the entries whose series the profile dropped.
// It is the one-shot form of ActiveDroppedSeries followed by FilterProgress.
func (f *ContinueWatchingProgressFilter) FilterDroppedProgress(ctx context.Context, userID int, profileID string, entries []userstore.WatchProgress) ([]userstore.WatchProgress, error) {
	if len(entries) == 0 {
		return entries, nil
	}
	set, err := f.ActiveDroppedSeries(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	return set.FilterProgress(ctx, entries)
}
