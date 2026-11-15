package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/librarykind"
)

// audiobook_group_stats (migration 20261115202419_audiobook_group_stats.sql)
// holds every library's author, narrator and series groups with their book
// counts, listening time and member books, so a page of groups is an indexed
// read instead of a grouping of every credit in the library. It counts every
// audiobook of a library as of its last refresh, so it answers only a viewer
// without library or maturity limits; the live query answers everyone else.
// The viewer's progress counts and the cover posters are read per request.

// listAudiobookGroupsFromStats answers a cursor page from
// audiobook_group_stats. served is false when the live query must answer
// instead: the viewer has library or maturity limits, or the view has not
// reached the library yet.
func listAudiobookGroupsFromStats(ctx context.Context, pool *pgxpool.Pool, q AudiobookGroupsQuery, filter AccessFilter, maxLimit int) (result AudiobookGroupsResult, served bool, err error) {
	if !audiobookGroupStatsServe(q.LibraryID, filter) {
		return AudiobookGroupsResult{}, false, nil
	}
	probeProgress, err := viewerHasLongProgressHistory(ctx, pool, filter)
	if err != nil {
		return AudiobookGroupsResult{}, false, err
	}
	plan, err := buildAudiobookGroupStatsSQL(q, filter, maxLimit, probeProgress)
	if err != nil {
		return AudiobookGroupsResult{}, false, err
	}
	result, err = runAudiobookGroupsPlan(ctx, pool, q, plan)
	if err != nil || len(result.Groups) > 0 {
		return result, err == nil, err
	}
	// An empty page is the answer once the view holds the library's groups.
	// A library the view has none for may be new since the last refresh, so
	// the live query answers it, including the pages after one it answered.
	var reached bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM audiobook_group_stats WHERE library_id = $1 AND group_by = $2)`,
		q.LibraryID, string(q.GroupBy)).Scan(&reached); err != nil {
		return AudiobookGroupsResult{}, false, fmt.Errorf("checking audiobook group stats: %w", err)
	}
	return result, reached, nil
}

// audiobookGroupStatsServe reports whether audiobook_group_stats is exact for
// the viewer. Until the next refresh the view keeps a book in a library's
// groups after it leaves the library, possibly for one the viewer may not
// see, so it serves only a viewer without library limits for whom the
// library's access conditions add nothing. Any condition a new access rule
// adds sends its viewers to the live query.
func audiobookGroupStatsServe(libraryID int, filter AccessFilter) bool {
	if filter.AllowedLibraryIDs != nil || len(filter.DisabledLibraryIDs) > 0 {
		return false
	}
	var conditions []string
	var args []any
	argIdx := 1
	return appendLibraryAudiobookAccessConditions("mi", libraryID, filter, &conditions, &args, &argIdx) && len(conditions) == 0
}

// audiobookGroupProgressHashRows is the most progress rows a viewer may have
// for a group page to read them all and hash them. Hashing costs one row per
// progress row, probing one index search per book on the page. A page of the
// largest groups holds tens of thousands of books, more than all but a
// history import's worth of progress rows.
const audiobookGroupProgressHashRows = 10_000

// viewerHasLongProgressHistory reports whether the viewer has more than
// audiobookGroupProgressHashRows progress rows. It reads at most one more.
func viewerHasLongProgressHistory(ctx context.Context, pool *pgxpool.Pool, filter AccessFilter) (bool, error) {
	var long bool
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) > $3 FROM (
			SELECT 1 FROM user_watch_progress WHERE user_id = $1 AND profile_id = $2 LIMIT $3 + 1
		) rows`, filter.UserID, filter.ProfileID, audiobookGroupProgressHashRows).Scan(&long); err != nil {
		return false, fmt.Errorf("counting viewer progress: %w", err)
	}
	return long, nil
}

// buildAudiobookGroupStatsSQL is buildAudiobookGroupsSQLWithLimit for a
// cursor page read from audiobook_group_stats. It selects the same columns.
// probeProgress looks up the viewer's progress per book of the page instead
// of hashing all of it (viewerHasLongProgressHistory).
func buildAudiobookGroupStatsSQL(q AudiobookGroupsQuery, filter AccessFilter, maxLimit int, probeProgress bool) (audiobookGroupsSQLPlan, error) {
	if q.LibraryID <= 0 {
		return audiobookGroupsSQLPlan{}, errors.New("audiobook groups: library id is required")
	}
	switch q.GroupBy {
	case AudiobookGroupByAuthor, AudiobookGroupByNarrator, AudiobookGroupBySeries:
	default:
		return audiobookGroupsSQLPlan{}, fmt.Errorf("audiobook groups: unsupported group_by %q", q.GroupBy)
	}
	switch strings.ToLower(strings.TrimSpace(q.Sort)) {
	case "", "name", audiobookGroupSortCount, audiobookGroupSortDuration:
	default:
		return audiobookGroupsSQLPlan{}, fmt.Errorf("audiobook groups: unsupported sort %q", q.Sort)
	}
	limit, fetchLimit, _ := audiobookGroupsPageSize(q, maxLimit)

	args := []any{q.LibraryID, string(q.GroupBy)}
	argIdx := 3
	statsWhere := "library_id = $1 AND group_by = $2"
	if search := audiobookGroupSearchPattern(q.SearchPrefix); search != "" {
		statsWhere += fmt.Sprintf(` AND group_key LIKE $%d ESCAPE '\'`, argIdx)
		args = append(args, search)
		argIdx++
	}
	countSQL := "SELECT COUNT(*)::int FROM audiobook_group_stats WHERE " + statsWhere
	countArgs := append([]any(nil), args...)
	totalColumn := ""
	if q.IncludeTotal {
		totalColumn = ",\n\t\t\t(" + countSQL + ") AS total_groups"
	}

	userArg, profileArg := argIdx, argIdx+1
	args = append(args, filter.UserID, filter.ProfileID)
	argIdx += 2
	order, seek := audiobookGroupSeek(q, &args, &argIdx)
	if seek != "" {
		statsWhere += " AND " + seek
	}
	limitArg := argIdx
	args = append(args, fetchLimit)

	viewerProgress, progressJoin := audiobookGroupProgressSQL(userArg, profileArg, probeProgress)

	// content_ids repeats a book once per credit and orders the books with a
	// poster first, so the first four with a poster when the view was built
	// are the cover stack; eight leave room for posters removed since.
	query := fmt.Sprintf(`
		WITH page AS (
			SELECT group_key, name, item_count, total_duration_seconds, content_ids
			FROM audiobook_group_stats
			WHERE %s
			ORDER BY %s
			LIMIT $%d
		),%s
		progress AS (
			SELECT
				pg.group_key,
				COUNT(*) FILTER (WHERE NOT vp.completed)::int AS in_progress_count,
				COUNT(*) FILTER (WHERE vp.completed)::int AS finished_count
			FROM page pg
			CROSS JOIN LATERAL unnest(pg.content_ids) AS m(content_id)
			%s
			GROUP BY pg.group_key
		)
		SELECT
			pg.name,
			pg.item_count,
			pg.total_duration_seconds,
			COALESCE(progress.in_progress_count, 0) AS in_progress_count,
			COALESCE(progress.finished_count, 0) AS finished_count,
			COALESCE(covers.poster_paths, '{}'::text[]) AS poster_paths%s,
			pg.group_key
		FROM page pg
		LEFT JOIN progress USING (group_key)
		CROSS JOIN LATERAL (
			SELECT array_agg(c.poster_path ORDER BY c.ord) AS poster_paths
			FROM (
				SELECT mi.poster_path, m.ord
				FROM unnest(pg.content_ids[1:8]) WITH ORDINALITY AS m(content_id, ord)
				JOIN media_items mi ON mi.content_id = m.content_id
				WHERE NULLIF(mi.poster_path, '') IS NOT NULL
				ORDER BY m.ord
				LIMIT 4
			) c
		) covers
		ORDER BY %s`,
		statsWhere, order, limitArg, viewerProgress, progressJoin, totalColumn, order,
	)

	return audiobookGroupsSQLPlan{
		SQL:          query,
		CountSQL:     countSQL,
		CountArgs:    countArgs,
		Args:         args,
		Limit:        limit,
		FetchLimit:   fetchLimit,
		IncludeTotal: q.IncludeTotal,
	}, nil
}

// audiobookGroupProgressSQL returns the CTE that reads the viewer's progress,
// if any, and the join that matches it to a page's books (m.content_id): a
// hash of all the viewer's progress rows, or with probeProgress a lookup per
// book.
func audiobookGroupProgressSQL(userArg, profileArg int, probeProgress bool) (cte, join string) {
	if probeProgress {
		return "", fmt.Sprintf(`JOIN user_watch_progress vp
			  ON vp.user_id = $%d
			 AND vp.profile_id = $%d
			 AND vp.media_item_id = m.content_id`, userArg, profileArg)
	}
	return fmt.Sprintf(`
		viewer_progress AS MATERIALIZED (
			SELECT media_item_id, completed
			FROM user_watch_progress
			WHERE user_id = $%d AND profile_id = $%d
		),`, userArg, profileArg), "JOIN viewer_progress vp ON vp.media_item_id = m.content_id"
}

// audiobookGroupStatsRefreshLock serializes refreshes of
// audiobook_group_stats across API servers.
const audiobookGroupStatsRefreshLock int64 = 0x4142_4752_5053_5431 // "ABGRPST1"

// RefreshAudiobookGroupStats rebuilds audiobook_group_stats unless some
// server started a refresh within skipWithin; zero always refreshes. busy
// reports that another server is refreshing it now. CONCURRENTLY keeps the
// view readable meanwhile. Every statement runs on the connection that holds
// the lock, so a pool of one connection does not wait on itself.
func RefreshAudiobookGroupStats(ctx context.Context, pool *pgxpool.Pool, skipWithin time.Duration) (busy bool, err error) {
	if pool == nil {
		return false, nil
	}
	lock, acquired, err := pglock.TryAcquire(ctx, pool, audiobookGroupStatsRefreshLock)
	if err != nil {
		return false, fmt.Errorf("locking audiobook group stats refresh: %w", err)
	}
	if !acquired {
		return true, nil
	}
	defer func() { _ = lock.Release(ctx) }()
	conn := lock.Conn()
	// Record when the refresh started, not when it finished: it reads only
	// what was committed by then.
	var startedAt time.Time
	var fresh bool
	if err := conn.QueryRow(ctx, `
		SELECT now(), EXISTS (SELECT 1 FROM audiobook_group_stats_refresh WHERE refreshed_at > now() - $1::interval)`,
		skipWithin).Scan(&startedAt, &fresh); err != nil {
		return false, fmt.Errorf("reading audiobook group stats refresh: %w", err)
	}
	if fresh {
		return false, nil
	}
	if _, err := conn.Exec(ctx, `REFRESH MATERIALIZED VIEW CONCURRENTLY audiobook_group_stats`); err != nil {
		return false, fmt.Errorf("refreshing audiobook group stats: %w", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO audiobook_group_stats_refresh (singleton, refreshed_at) VALUES (true, $1)
		ON CONFLICT (singleton) DO UPDATE SET refreshed_at = EXCLUDED.refreshed_at`, startedAt); err != nil {
		return false, fmt.Errorf("recording audiobook group stats refresh: %w", err)
	}
	return false, nil
}

// AudiobookGroupStatsRefresher keeps audiobook_group_stats current: once the
// last refresh by any server is interval old, which catches metadata
// enrichment and edits, and soon after a scan of an audiobook library, on the
// server that ran it.
type AudiobookGroupStatsRefresher struct {
	interval time.Duration
	// poll is how often each server checks the last refresh's age, so a
	// change shows within interval plus poll plus the refresh itself.
	poll time.Duration
	// delay folds a burst of scans, such as autoscan ingesting one book at a
	// time, into one refresh.
	delay   time.Duration
	refresh func(ctx context.Context, skipWithin time.Duration) (busy bool, err error)
	// holdAudiobooks reports whether any of the libraries is an audiobook
	// library.
	holdAudiobooks func(ctx context.Context, libraryIDs []int) (bool, error)

	mu      sync.Mutex
	pending map[int]struct{}
	// changedAt is when the latest pending change arrived.
	changedAt time.Time
	wake      chan struct{}
}

// NewAudiobookGroupStatsRefresher returns a refresher for pool. Start it with
// Run.
func NewAudiobookGroupStatsRefresher(pool *pgxpool.Pool, interval, poll, delay time.Duration) *AudiobookGroupStatsRefresher {
	return newAudiobookGroupStatsRefresher(interval, poll, delay,
		func(ctx context.Context, skipWithin time.Duration) (bool, error) {
			return RefreshAudiobookGroupStats(ctx, pool, skipWithin)
		},
		func(ctx context.Context, libraryIDs []int) (bool, error) {
			return librariesHoldAudiobooks(ctx, pool, libraryIDs)
		})
}

func newAudiobookGroupStatsRefresher(
	interval, poll, delay time.Duration,
	refresh func(context.Context, time.Duration) (bool, error),
	holdAudiobooks func(context.Context, []int) (bool, error),
) *AudiobookGroupStatsRefresher {
	return &AudiobookGroupStatsRefresher{
		interval:       interval,
		poll:           poll,
		delay:          delay,
		refresh:        refresh,
		holdAudiobooks: holdAudiobooks,
		pending:        map[int]struct{}{},
		wake:           make(chan struct{}, 1),
	}
}

// LibraryChanged marks a library a scan changed. It never blocks, so it is
// safe to call from an event bus observer.
func (r *AudiobookGroupStatsRefresher) LibraryChanged(libraryID int) {
	if r == nil || libraryID <= 0 {
		return
	}
	r.mu.Lock()
	r.pending[libraryID] = struct{}{}
	r.changedAt = time.Now()
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run refreshes until ctx ends.
func (r *AudiobookGroupStatsRefresher) Run(ctx context.Context) {
	ticker := time.NewTicker(r.poll)
	defer ticker.Stop()
	// due fires delay after the first change since the last refresh.
	var due <-chan time.Time
	// retry is set when another server held the refresh a scan asked for,
	// and scannedAt is when the latest scan that asked for one arrived.
	retry := false
	var scannedAt time.Time
	// heldUntil pauses refreshes for an interval after one fails, so a
	// refresh that keeps failing does not run every poll or every scan.
	var heldUntil time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Now().Before(heldUntil) {
				continue
			}
			// Any server's refresh within the interval will do.
			if _, failed := r.run(ctx, r.interval); failed {
				heldUntil = time.Now().Add(r.interval)
			}
		case <-r.wake:
			if due == nil {
				due = time.After(r.delay)
			}
		case <-due:
			due = nil
			if wait := time.Until(heldUntil); wait > 0 {
				// The scan's libraries stay pending until the pause ends.
				due = time.After(wait)
				continue
			}
			if changed, at := r.changedAudiobooks(ctx); changed {
				scannedAt = at
			} else if !retry {
				continue
			}
			// Any server's refresh that started after the latest scan covers
			// it, so only an older one is redone; one running now may have
			// started before the scan, so a busy refresh is tried again. The
			// time since the scan is measured here and compared on the
			// database's clock, so clock skew cannot skip a refresh.
			var failed bool
			retry, failed = r.run(ctx, time.Since(scannedAt))
			if failed {
				heldUntil = time.Now().Add(r.interval)
			}
			if retry {
				due = time.After(r.delay)
			}
		}
	}
}

// run refreshes the view, bounded by the interval, and reports whether
// another server was refreshing it or the refresh failed.
func (r *AudiobookGroupStatsRefresher) run(ctx context.Context, skipWithin time.Duration) (busy, failed bool) {
	refreshCtx, cancel := context.WithTimeout(ctx, r.interval)
	defer cancel()
	busy, err := r.refresh(refreshCtx, skipWithin)
	if err != nil {
		slog.WarnContext(ctx, "audiobook group stats refresh failed", "component", "catalog", "err", err)
		return false, true
	}
	return busy, false
}

// changedAudiobooks takes the libraries changed since the last call and
// reports whether one is an audiobook library, and when the latest change
// arrived. It reports true when that cannot be read, so a scan's refresh is
// not lost.
func (r *AudiobookGroupStatsRefresher) changedAudiobooks(ctx context.Context) (bool, time.Time) {
	r.mu.Lock()
	libraryIDs := slices.Collect(maps.Keys(r.pending))
	clear(r.pending)
	at := r.changedAt
	r.mu.Unlock()
	if len(libraryIDs) == 0 {
		return false, at
	}
	holds, err := r.holdAudiobooks(ctx, libraryIDs)
	if err != nil {
		slog.WarnContext(ctx, "audiobook group stats: reading changed libraries failed", "component", "catalog", "err", err)
		return true, at
	}
	return holds, at
}

// librariesHoldAudiobooks reports whether any of the libraries is an
// audiobook library.
func librariesHoldAudiobooks(ctx context.Context, pool *pgxpool.Pool, libraryIDs []int) (bool, error) {
	rows, err := pool.Query(ctx, `SELECT type FROM media_folders WHERE id = ANY($1)`, libraryIDs)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var libraryType string
		if err := rows.Scan(&libraryType); err != nil {
			return false, err
		}
		if librarykind.IsAudiobook(libraryType) {
			return true, nil
		}
	}
	return false, rows.Err()
}
