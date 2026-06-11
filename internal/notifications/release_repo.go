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
		ON CONFLICT (library_id, episode_id) DO NOTHING` + availabilityReturning
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
		ON CONFLICT (library_id, episode_id) DO NOTHING` + availabilityReturning
	return r.recordAvailability(ctx, libraryID, emitEvents, query, args)
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
				fmt.Sprintf("%d:%s", libraryID, row.EpisodeID),
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
		SELECT id, library_id, series_id, episode_id, season_number, episode_number,
		       episode_key, available_at, dedupe_key, created_at
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

	events := make([]ReleaseEvent, 0, limit)
	for rows.Next() {
		var event ReleaseEvent
		if err := rows.Scan(
			&event.ID, &event.LibraryID, &event.SeriesID, &event.EpisodeID,
			&event.SeasonNumber, &event.EpisodeNumber, &event.EpisodeKey,
			&event.AvailableAt, &event.DedupeKey, &event.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan release event: %w", err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
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
