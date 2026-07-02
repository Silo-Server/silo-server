package notifications

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// ReleaseRepository owns episode_availability, notification_library_seed_state,
// and release_events.
type ReleaseRepository struct {
	pool *pgxpool.Pool
}

// NewReleaseRepository creates a ReleaseRepository.
func NewReleaseRepository(pool *pgxpool.Pool) *ReleaseRepository {
	return &ReleaseRepository{pool: pool}
}

// IsLibrarySeeded reports whether availability seeding completed for the
// library. Unseeded libraries record availability silently (no release
// events).
func (r *ReleaseRepository) IsLibrarySeeded(ctx context.Context, libraryID int) (bool, error) {
	var seeded bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM notification_library_seed_state WHERE library_id = $1)`,
		libraryID,
	).Scan(&seeded)
	return seeded, err
}

// MarkLibrarySeeded records that availability seeding completed for the
// library. Idempotent.
func (r *ReleaseRepository) MarkLibrarySeeded(ctx context.Context, libraryID int) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_library_seed_state (library_id, seeded_at)
		VALUES ($1, now())
		ON CONFLICT (library_id) DO NOTHING`, libraryID)
	return err
}

// availabilityInsertColumns is shared by the library-wide and path-scoped
// availability inserts.
const availabilityReturning = ` RETURNING episode_id, series_id, season_number, episode_number, episode_key, available_at`

// availabilityOrdinalGuard excludes episode rows whose ordinals cannot fold
// into an int4 episode_key; without the season upper bound the key expression
// overflows in Postgres and aborts the whole insert. Must stay in sync with
// ValidEpisodeOrdinals (episode_key.go).
var availabilityOrdinalGuard = fmt.Sprintf(
	`e.season_number BETWEEN 0 AND %d AND e.episode_number BETWEEN 0 AND %d`,
	episodeKeyMaxSeason, episodeKeySeasonMultiplier-1)

// availabilityKeyExpr computes episode_key in SQL with the same fold as
// EpisodeKey (episode_key.go).
var availabilityKeyExpr = fmt.Sprintf(
	`e.season_number * %d + e.episode_number`, episodeKeySeasonMultiplier)

// RecordAvailabilityForLibrary inserts episode_availability rows for every
// episode currently present in the library (one-way, idempotent) and, when
// emitEvents is true, creates release events for the newly inserted rows.
// Returns (availability rows inserted, release events created).
func (r *ReleaseRepository) RecordAvailabilityForLibrary(ctx context.Context, libraryID int, emitEvents bool) (int, int, error) {
	query := `
		INSERT INTO episode_availability
			(library_id, episode_id, series_id, season_number, episode_number, episode_key)
		SELECT el.media_folder_id, e.content_id, e.series_id, e.season_number, e.episode_number,
		       ` + availabilityKeyExpr + `
		FROM episode_libraries el
		JOIN episodes e ON e.content_id = el.episode_id
		WHERE el.media_folder_id = $1
		  AND ` + availabilityOrdinalGuard + `
		ON CONFLICT DO NOTHING` + availabilityReturning
	return r.recordAvailability(ctx, libraryID, emitEvents, query, []any{libraryID})
}

// RecordAvailabilityForPaths inserts availability rows for episodes whose
// playable files live under the given scope paths (subtree/file ingest), and
// optionally creates release events for newly inserted rows.
func (r *ReleaseRepository) RecordAvailabilityForPaths(ctx context.Context, libraryID int, scopePaths []string, emitEvents bool) (int, int, error) {
	if len(scopePaths) == 0 {
		return 0, 0, nil
	}
	args := []any{libraryID}
	scopeConds := make([]string, 0, len(scopePaths))
	for _, path := range scopePaths {
		args = append(args, path)
		idx := len(args)
		scopeConds = append(scopeConds,
			fmt.Sprintf("(mf.file_path = $%d OR starts_with(mf.file_path, $%d || '/'))", idx, idx))
	}
	query := `
		INSERT INTO episode_availability
			(library_id, episode_id, series_id, season_number, episode_number, episode_key)
		SELECT DISTINCT mf.media_folder_id, e.content_id, e.series_id, e.season_number, e.episode_number,
		       ` + availabilityKeyExpr + `
		FROM media_files mf
		JOIN episodes e ON e.content_id = mf.episode_id
		WHERE mf.media_folder_id = $1
		  AND mf.missing_since IS NULL
		  AND mf.episode_id IS NOT NULL
		  AND ` + availabilityOrdinalGuard + `
		  AND (` + strings.Join(scopeConds, " OR ") + `)
		ON CONFLICT DO NOTHING` + availabilityReturning
	return r.recordAvailability(ctx, libraryID, emitEvents, query, args)
}

// IsContentSeeded reports whether availability seeding completed for the
// library and content kind. Episodes keep the legacy single-purpose table;
// every later kind shares notification_content_seed_state. The split is
// load-bearing: episode seeding already marked movie libraries (with zero
// movie rows), so reusing those markers would flood a kind's back catalog on
// its first post-upgrade scan.
func (r *ReleaseRepository) IsContentSeeded(ctx context.Context, libraryID int, kind string) (bool, error) {
	if kind == EventKindEpisode {
		return r.IsLibrarySeeded(ctx, libraryID)
	}
	var seeded bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM notification_content_seed_state WHERE library_id = $1 AND kind = $2)`,
		libraryID, kind,
	).Scan(&seeded)
	return seeded, err
}

// MarkContentSeeded records that availability seeding completed for the
// library and content kind. Idempotent.
func (r *ReleaseRepository) MarkContentSeeded(ctx context.Context, libraryID int, kind string) error {
	if kind == EventKindEpisode {
		return r.MarkLibrarySeeded(ctx, libraryID)
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_content_seed_state (library_id, kind, seeded_at)
		VALUES ($1, $2, now())
		ON CONFLICT (library_id, kind) DO NOTHING`, libraryID, kind)
	return err
}

// RecordItemAvailabilityForLibrary inserts availability rows for every item
// of the kind currently present in the library (one-way, idempotent) and,
// when emitEvents is true, creates release events for the newly inserted
// rows. Shared by every flat item kind (item_kind.go). Returns (availability
// rows inserted, release events created).
func (r *ReleaseRepository) RecordItemAvailabilityForLibrary(ctx context.Context, k flatItemKind, libraryID int, emitEvents bool) (int, int, error) {
	args := []any{libraryID, k.ItemType}
	var query string
	if k.AvailabilityTable == movieAvailabilityTable {
		query = `
			INSERT INTO movie_availability (library_id, item_id)
			SELECT mil.media_folder_id, mi.content_id
			FROM media_item_libraries mil
			JOIN media_items mi ON mi.content_id = mil.content_id AND mi.type = $2
			WHERE mil.media_folder_id = $1
			ON CONFLICT (library_id, item_id) DO NOTHING
			RETURNING item_id, available_at`
	} else {
		query = `
			INSERT INTO item_availability (library_id, item_id, kind)
			SELECT mil.media_folder_id, mi.content_id, $3
			FROM media_item_libraries mil
			JOIN media_items mi ON mi.content_id = mil.content_id AND mi.type = $2
			WHERE mil.media_folder_id = $1
			ON CONFLICT (library_id, item_id, kind) DO NOTHING
			RETURNING item_id, available_at`
		args = append(args, k.Kind)
	}
	return r.recordItemAvailability(ctx, k, libraryID, emitEvents, query, args)
}

// RecordItemAvailabilityForPaths inserts availability rows for items of the
// kind whose files live under the given scope paths (subtree/file ingest),
// and optionally creates release events for newly inserted rows.
func (r *ReleaseRepository) RecordItemAvailabilityForPaths(ctx context.Context, k flatItemKind, libraryID int, scopePaths []string, emitEvents bool) (int, int, error) {
	if len(scopePaths) == 0 {
		return 0, 0, nil
	}
	args := []any{libraryID, k.ItemType}
	// The column list doubles as the ON CONFLICT target: both tables' primary
	// keys are exactly their insert columns.
	insertCols, selectExtra := "(library_id, item_id)", ""
	if k.AvailabilityTable != movieAvailabilityTable {
		args = append(args, k.Kind)
		insertCols, selectExtra = "(library_id, item_id, kind)", ", $3"
	}
	scopeConds := make([]string, 0, len(scopePaths))
	for _, path := range scopePaths {
		args = append(args, path)
		idx := len(args)
		scopeConds = append(scopeConds,
			fmt.Sprintf("(mf.file_path = $%d OR starts_with(mf.file_path, $%d || '/'))", idx, idx))
	}
	query := `
		INSERT INTO ` + k.AvailabilityTable + ` ` + insertCols + `
		SELECT DISTINCT mf.media_folder_id, mi.content_id` + selectExtra + `
		FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id AND mi.type = $2
		WHERE mf.media_folder_id = $1
		  AND mf.missing_since IS NULL
		  AND mf.episode_id IS NULL
		  AND mf.content_id IS NOT NULL
		  AND (` + strings.Join(scopeConds, " OR ") + `)
		ON CONFLICT ` + insertCols + ` DO NOTHING
		RETURNING item_id, available_at`
	return r.recordItemAvailability(ctx, k, libraryID, emitEvents, query, args)
}

// recordItemAvailability is the flat-item counterpart of recordAvailability:
// insert availability facts and the optional release events in one
// transaction.
func (r *ReleaseRepository) recordItemAvailability(ctx context.Context, k flatItemKind, libraryID int, emitEvents bool, query string, args []any) (int, int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("begin item availability tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return 0, 0, fmt.Errorf("insert %s availability: %w", k.Kind, err)
	}
	type newItem struct {
		ItemID      string
		AvailableAt time.Time
	}
	inserted := make([]newItem, 0, 16)
	for rows.Next() {
		var row newItem
		if err := rows.Scan(&row.ItemID, &row.AvailableAt); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("scan inserted %s availability: %w", k.Kind, err)
		}
		inserted = append(inserted, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("read inserted %s availability: %w", k.Kind, err)
	}

	events := 0
	if emitEvents && len(inserted) > 0 {
		const chunkSize = 500
		for start := 0; start < len(inserted); start += chunkSize {
			end := min(start+chunkSize, len(inserted))
			chunk := inserted[start:end]

			var sb strings.Builder
			sb.WriteString(`
				INSERT INTO release_events
					(id, library_id, kind, item_id, available_at, dedupe_key)
				VALUES `)
			eventArgs := make([]any, 0, len(chunk)*6)
			for i, row := range chunk {
				if i > 0 {
					sb.WriteString(", ")
				}
				base := len(eventArgs)
				sb.WriteString(fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d)",
					base+1, base+2, base+3, base+4, base+5, base+6))
				eventArgs = append(eventArgs,
					ulid.Make().String(),
					libraryID,
					k.Kind,
					row.ItemID,
					row.AvailableAt,
					ItemDedupeKey(k.Kind, libraryID, row.ItemID),
				)
			}
			sb.WriteString(" ON CONFLICT (dedupe_key) DO NOTHING")
			tag, err := tx.Exec(ctx, sb.String(), eventArgs...)
			if err != nil {
				return 0, 0, fmt.Errorf("insert %s release events: %w", k.Kind, err)
			}
			events += int(tag.RowsAffected())
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit item availability tx: %w", err)
	}
	return len(inserted), events, nil
}

// EpisodeDedupeKey composes the release_events dedupe key for an episode.
// It keys the logical episode in a library, not the catalog row id, so a
// series re-ID or episode-row re-mint that remaps the episode id does not
// become a new release.
func EpisodeDedupeKey(libraryID int, seriesID string, episodeKey int) string {
	return fmt.Sprintf("episode:%d:%s:%d", libraryID, seriesID, episodeKey)
}

// ItemDedupeKey composes the release_events dedupe key for a flat item kind
// (movie, audiobook, ebook). The kind prefix keeps each kind's keyspace
// disjoint from the others and from episode keys.
func ItemDedupeKey(kind string, libraryID int, itemID string) string {
	return fmt.Sprintf("%s:%d:%s", kind, libraryID, itemID)
}

type newAvailability struct {
	EpisodeID     string
	SeriesID      string
	SeasonNumber  int
	EpisodeNumber int
	EpisodeKey    int
	AvailableAt   time.Time
}

// recordAvailability runs the availability insert and the optional release
// event insert in one short transaction, so an event is never created without
// its availability fact.
func (r *ReleaseRepository) recordAvailability(ctx context.Context, libraryID int, emitEvents bool, query string, args []any) (int, int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("begin availability tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return 0, 0, fmt.Errorf("insert episode availability: %w", err)
	}
	inserted := make([]newAvailability, 0, 16)
	for rows.Next() {
		var row newAvailability
		if err := rows.Scan(&row.EpisodeID, &row.SeriesID, &row.SeasonNumber, &row.EpisodeNumber, &row.EpisodeKey, &row.AvailableAt); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("scan inserted availability: %w", err)
		}
		inserted = append(inserted, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("read inserted availability: %w", err)
	}

	events := 0
	if emitEvents && len(inserted) > 0 {
		events, err = insertReleaseEvents(ctx, tx, libraryID, inserted)
		if err != nil {
			return 0, 0, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit availability tx: %w", err)
	}
	return len(inserted), events, nil
}

func insertReleaseEvents(ctx context.Context, tx pgx.Tx, libraryID int, rows []newAvailability) (int, error) {
	const chunkSize = 500
	total := 0
	for start := 0; start < len(rows); start += chunkSize {
		end := min(start+chunkSize, len(rows))
		chunk := rows[start:end]

		var sb strings.Builder
		sb.WriteString(`
			INSERT INTO release_events
				(id, library_id, series_id, episode_id, season_number, episode_number, episode_key, available_at, dedupe_key)
			VALUES `)
		args := make([]any, 0, len(chunk)*9)
		for i, row := range chunk {
			if i > 0 {
				sb.WriteString(", ")
			}
			base := len(args)
			sb.WriteString(fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
				base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9))
			args = append(args,
				ulid.Make().String(),
				libraryID,
				row.SeriesID,
				row.EpisodeID,
				row.SeasonNumber,
				row.EpisodeNumber,
				row.EpisodeKey,
				row.AvailableAt,
				EpisodeDedupeKey(libraryID, row.SeriesID, row.EpisodeKey),
			)
		}
		sb.WriteString(" ON CONFLICT (dedupe_key) DO NOTHING")
		tag, err := tx.Exec(ctx, sb.String(), args...)
		if err != nil {
			return total, fmt.Errorf("insert release events: %w", err)
		}
		total += int(tag.RowsAffected())
	}
	return total, nil
}

// ClaimUnprocessed locks and returns up to limit unprocessed release events
// older than the settle delay. Must run inside the caller's transaction;
// FOR UPDATE SKIP LOCKED keeps multiple nodes from double-processing.
func (r *ReleaseRepository) ClaimUnprocessed(ctx context.Context, tx pgx.Tx, settle time.Duration, limit int) ([]ReleaseEvent, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+releaseEventColumns+`
		FROM release_events
		WHERE processed_at IS NULL
		  AND created_at <= now() - ($1 * interval '1 second')
		ORDER BY created_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED`,
		settle.Seconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("claim release events: %w", err)
	}
	defer rows.Close()
	return scanReleaseEvents(rows, limit)
}

// releaseEventColumns is the shared event SELECT list. Episode columns are
// nullable since the movie kind landed; COALESCE keeps episode rows scanning
// into the flat struct and movie rows reading as zero values.
const releaseEventColumns = `id, library_id, kind, COALESCE(item_id, ''),
	COALESCE(series_id, ''), COALESCE(episode_id, ''),
	COALESCE(season_number, 0), COALESCE(episode_number, 0),
	COALESCE(episode_key, 0), available_at, dedupe_key, created_at`

func scanReleaseEvents(rows pgx.Rows, capacityHint int) ([]ReleaseEvent, error) {
	events := make([]ReleaseEvent, 0, capacityHint)
	for rows.Next() {
		var event ReleaseEvent
		if err := rows.Scan(
			&event.ID, &event.LibraryID, &event.Kind, &event.ItemID,
			&event.SeriesID, &event.EpisodeID,
			&event.SeasonNumber, &event.EpisodeNumber, &event.EpisodeKey,
			&event.AvailableAt, &event.DedupeKey, &event.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan release event: %w", err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// HasEventsSince cheaply reports whether any release event matured past the
// batch window exists beyond the cursor, so idle server channels don't open a
// claim transaction every sweep pass. Shares ListEventsSince's predicate.
func (r *ReleaseRepository) HasEventsSince(ctx context.Context, since Cursor, batchAge time.Duration) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM release_events
			WHERE (created_at, id) > ($1, $2)
			  AND created_at <= now() - ($3 * interval '1 second')
		)`,
		since.CreatedAt, since.ID, batchAge.Seconds()).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check pending release events: %w", err)
	}
	return exists, nil
}

// ListEventsSince returns release events past the (created_at, id) cursor in
// sweep order, regardless of processed/suppressed state: the server-channel
// broadcast feed wants burst-suppressed episodes too (grouping absorbs the
// volume). batchAge holds back rows younger than the batch window so an
// in-flight availability transaction can never commit behind the watermark.
// Must run inside the caller's transaction holding the channel claim.
func (r *ReleaseRepository) ListEventsSince(ctx context.Context, tx pgx.Tx, since Cursor, batchAge time.Duration, limit int) ([]ReleaseEvent, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+releaseEventColumns+`
		FROM release_events
		WHERE (created_at, id) > ($1, $2)
		  AND created_at <= now() - ($3 * interval '1 second')
		ORDER BY created_at, id
		LIMIT $4`,
		since.CreatedAt, since.ID, batchAge.Seconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("list release events since cursor: %w", err)
	}
	defer rows.Close()
	return scanReleaseEvents(rows, limit)
}

// MarkProcessed marks events processed, optionally tagging them with a
// suppression reason.
func (r *ReleaseRepository) MarkProcessed(ctx context.Context, tx pgx.Tx, ids []string, suppressedReason *string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE release_events
		SET processed_at = now(), suppressed_reason = $2
		WHERE id = ANY($1)`,
		ids, suppressedReason)
	if err != nil {
		return fmt.Errorf("mark release events processed: %w", err)
	}
	return nil
}

// DeleteProcessedBefore prunes processed release events older than the cutoff
// (retention). Inbox rows survive via ON DELETE SET NULL.
func (r *ReleaseRepository) DeleteProcessedBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM release_events
		WHERE processed_at IS NOT NULL AND created_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DeleteUnprocessedBefore prunes unprocessed release events older than the
// fanout staleness horizon. These accumulate without bound when fanout is
// disabled while availability detection keeps emitting events; the fanout
// worker suppresses them as stale rather than delivering them, so retention
// can reclaim them directly.
func (r *ReleaseRepository) DeleteUnprocessedBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM release_events
		WHERE processed_at IS NULL AND created_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
