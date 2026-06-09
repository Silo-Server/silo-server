package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func scanWatchProgress(scanner interface {
	Scan(dest ...any) error
}) (*userstore.WatchProgress, error) {
	var wp userstore.WatchProgress
	var updatedAt time.Time
	err := scanner.Scan(
		&wp.ProfileID, &wp.MediaItemID, &wp.PositionSeconds,
		&wp.DurationSeconds, &wp.Completed, &updatedAt,
		&wp.LastFileID, &wp.LastResolution, &wp.LastHDR, &wp.LastCodecVideo, &wp.LastEditionKey,
	)
	if err != nil {
		return nil, err
	}
	wp.UpdatedAt = timeToString(updatedAt)
	return &wp, nil
}

func scanWatchHistoryEntry(scanner interface {
	Scan(dest ...any) error
}) (*userstore.WatchHistoryEntry, error) {
	var entry userstore.WatchHistoryEntry
	var watchedAt time.Time
	var identityJSON string
	err := scanner.Scan(
		&entry.ID, &entry.ProfileID, &entry.MediaItemID,
		&watchedAt, &entry.DurationSeconds, &entry.Completed, &entry.Source,
		&identityJSON,
	)
	if err != nil {
		return nil, err
	}
	entry.WatchedAt = timeToString(watchedAt)
	if identityJSON != "" && identityJSON != "{}" {
		_ = json.Unmarshal([]byte(identityJSON), &entry.Identity)
	}
	return &entry, nil
}

func (s *PostgresUserStore) UpdateProgress(ctx context.Context, profileID, mediaItemID string, position, duration float64, thresholds userstore.ProgressThresholds) error {
	if duration > 0 && position > 0 && position/duration < userstore.MinResumeFraction(thresholds.MinResumePct) {
		return nil
	}
	now := nowUTC()
	completed := false
	if duration > 0 && position/duration > userstore.WatchedFraction(thresholds.WatchedPct) {
		completed = true
		position = 0 // match MarkWatched() — completed rows hold no resume point
	}
	// `completed` is a one-way watched latch; position resets to 0 on
	// completion so `position_seconds > 0` means "has a resume point". A
	// rewatch heartbeat on a completed row therefore re-enters Continue
	// Watching through plain GREATEST (stored position is 0) while the
	// watched flag survives.
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(user_id, profile_id, media_item_id) DO UPDATE SET
			position_seconds = CASE WHEN excluded.completed THEN 0
				ELSE GREATEST(excluded.position_seconds, user_watch_progress.position_seconds) END,
			duration_seconds = excluded.duration_seconds,
			completed = CASE WHEN excluded.completed
				THEN TRUE ELSE user_watch_progress.completed END,
			updated_at = excluded.updated_at`,
		s.userID, profileID, mediaItemID, position, duration, completed, now,
	)
	if err != nil {
		return fmt.Errorf("updating progress: %w", err)
	}
	return nil
}

func (s *PostgresUserStore) SetProgress(ctx context.Context, profileID, mediaItemID string, position, duration float64, thresholds userstore.ProgressThresholds) error {
	if duration > 0 && position > 0 && position/duration < userstore.MinResumeFraction(thresholds.MinResumePct) {
		return nil
	}
	now := nowUTC()
	completed := false
	if duration > 0 && position/duration > userstore.WatchedFraction(thresholds.WatchedPct) {
		completed = true
		position = 0 // match MarkWatched() — completed rows hold no resume point
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(user_id, profile_id, media_item_id) DO UPDATE SET
			position_seconds = excluded.position_seconds,
			duration_seconds = excluded.duration_seconds,
			completed = user_watch_progress.completed OR excluded.completed,
			updated_at = excluded.updated_at`,
		s.userID, profileID, mediaItemID, position, duration, completed, now,
	)
	if err != nil {
		return fmt.Errorf("setting progress: %w", err)
	}
	return nil
}

func (s *PostgresUserStore) SetProgressAt(ctx context.Context, profileID, mediaItemID string, position, duration float64, completed bool, updatedAt time.Time) error {
	if position < 0 {
		position = 0
	}
	if duration < 0 {
		duration = 0
	}
	if completed {
		position = 0
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	updatedAtText := updatedAt.UTC().Format(time.RFC3339)
	suppressed, err := s.historyIsHidden(ctx, profileID, mediaItemID, updatedAtText)
	if err != nil {
		return err
	}
	if suppressed {
		return nil
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(user_id, profile_id, media_item_id) DO UPDATE SET
			position_seconds = excluded.position_seconds,
			duration_seconds = excluded.duration_seconds,
			completed = user_watch_progress.completed OR excluded.completed,
			updated_at = excluded.updated_at`,
		s.userID, profileID, mediaItemID, position, duration, completed, updatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("setting progress at time: %w", err)
	}
	return nil
}

func (s *PostgresUserStore) SetProgressIfNewer(ctx context.Context, profileID, mediaItemID string, position, duration float64, completed bool, updatedAt time.Time) (bool, error) {
	if position < 0 {
		position = 0
	}
	if duration < 0 {
		duration = 0
	}
	if completed {
		position = 0
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	updatedAtText := updatedAt.UTC().Format(time.RFC3339)
	suppressed, err := s.historyIsHidden(ctx, profileID, mediaItemID, updatedAtText)
	if err != nil {
		return false, err
	}
	if suppressed {
		return false, nil
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(user_id, profile_id, media_item_id) DO UPDATE SET
			position_seconds = EXCLUDED.position_seconds,
			duration_seconds = EXCLUDED.duration_seconds,
			completed = EXCLUDED.completed,
			updated_at = EXCLUDED.updated_at
		WHERE EXCLUDED.updated_at > user_watch_progress.updated_at`,
		s.userID, profileID, mediaItemID, position, duration, completed, updatedAt.UTC(),
	)
	if err != nil {
		return false, fmt.Errorf("setting newer progress: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *PostgresUserStore) MarkWatched(ctx context.Context, profileID, mediaItemID string, duration float64) error {
	if duration < 0 {
		duration = 0
	}

	now := nowUTC()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES ($1, $2, $3, 0, $4, TRUE, $5)
		ON CONFLICT(user_id, profile_id, media_item_id) DO UPDATE SET
			position_seconds = 0,
			duration_seconds = excluded.duration_seconds,
			completed = TRUE,
			updated_at = excluded.updated_at`,
		s.userID, profileID, mediaItemID, duration, now,
	)
	if err != nil {
		return fmt.Errorf("marking watched: %w", err)
	}
	return nil
}

func (s *PostgresUserStore) ClearProgress(ctx context.Context, profileID, mediaItemID string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM user_watch_progress
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		s.userID, profileID, mediaItemID,
	)
	if err != nil {
		return fmt.Errorf("clearing progress: %w", err)
	}
	return nil
}

// MarkProgressBatch upserts a `completed = TRUE` progress row for every entry
// in mediaItemIDs in a single statement. Used by the jellycompat series
// mark-played path so a 200-episode mark collapses to one INSERT.
func (s *PostgresUserStore) MarkProgressBatch(ctx context.Context, profileID string, mediaItemIDs []string, updatedAt time.Time) error {
	mediaItemIDs = compactMediaItemIDs(mediaItemIDs)
	if len(mediaItemIDs) == 0 {
		return nil
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_watch_progress
			(user_id, profile_id, media_item_id, completed, position_seconds, duration_seconds, updated_at)
		SELECT $1, $2, mid, TRUE, 0, 0, $4
		FROM unnest($3::text[]) AS mid
		ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE
		SET completed = TRUE,
		    position_seconds = 0,
		    updated_at = EXCLUDED.updated_at
		WHERE user_watch_progress.completed IS DISTINCT FROM TRUE
		   OR user_watch_progress.updated_at < EXCLUDED.updated_at`,
		s.userID, profileID, mediaItemIDs, updatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("marking progress batch: %w", err)
	}
	return nil
}

// ClearProgressBatch resets every (user, profile, media_item_id) row to
// `completed = FALSE, position_seconds = 0` in a single UPDATE. Used by the
// jellycompat series mark-unplayed path. Rows that don't exist are silently
// skipped — the matching DeleteHistoryBySource call handles removing history.
func (s *PostgresUserStore) ClearProgressBatch(ctx context.Context, profileID string, mediaItemIDs []string, updatedAt time.Time) error {
	mediaItemIDs = compactMediaItemIDs(mediaItemIDs)
	if len(mediaItemIDs) == 0 {
		return nil
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	// Clear partially-watched rows (completed = FALSE with position_seconds > 0)
	// in addition to fully-completed ones — the prior ClearProgress path
	// DELETE-d the row unconditionally, so any non-default state must be
	// cleared. Skip rows already in the target state (completed = FALSE AND
	// position_seconds = 0) to avoid pointless writes.
	_, err := s.pool.Exec(ctx, `
		UPDATE user_watch_progress
		SET completed = FALSE, position_seconds = 0, updated_at = $4
		WHERE user_id = $1 AND profile_id = $2
		  AND media_item_id = ANY($3::text[])
		  AND (completed = TRUE OR position_seconds <> 0)`,
		s.userID, profileID, mediaItemIDs, updatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("clearing progress batch: %w", err)
	}
	return nil
}

func (s *PostgresUserStore) GetProgress(ctx context.Context, profileID, mediaItemID string) (*userstore.WatchProgress, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at,
		       last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
		FROM user_watch_progress
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3
		  AND NOT EXISTS (
			SELECT 1
			FROM user_history_hidden_items hhi
			WHERE hhi.user_id = user_watch_progress.user_id
			  AND hhi.profile_id = user_watch_progress.profile_id
			  AND hhi.media_item_id = user_watch_progress.media_item_id
			  AND user_watch_progress.updated_at <= hhi.hidden_before
		  )`,
		s.userID, profileID, mediaItemID,
	)
	wp, err := scanWatchProgress(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting progress: %w", err)
	}
	return wp, nil
}

func (s *PostgresUserStore) ListProgress(ctx context.Context, profileID, status string, limit, offset int) ([]userstore.WatchProgress, error) {
	var query string
	var args []any

	switch status {
	case "in_progress":
		// position_seconds > 0 (not completed = FALSE): completed rows hold
		// position 0, so a rewatch of a watched item has completed = TRUE with
		// a live resume point and belongs in Continue Watching.
		query = `
			SELECT profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at,
			       last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
			FROM user_watch_progress
			WHERE user_id = $1 AND profile_id = $2 AND position_seconds > 0
			  AND NOT EXISTS (
				SELECT 1
				FROM user_history_hidden_items hhi
				WHERE hhi.user_id = user_watch_progress.user_id
				  AND hhi.profile_id = user_watch_progress.profile_id
				  AND hhi.media_item_id = user_watch_progress.media_item_id
				  AND user_watch_progress.updated_at <= hhi.hidden_before
			  )
			ORDER BY updated_at DESC
			LIMIT $3 OFFSET $4`
		args = []any{s.userID, profileID, limit, offset}
	case "completed":
		query = `
			SELECT profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at,
			       last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
			FROM user_watch_progress
			WHERE user_id = $1 AND profile_id = $2 AND completed = TRUE
			  AND NOT EXISTS (
				SELECT 1
				FROM user_history_hidden_items hhi
				WHERE hhi.user_id = user_watch_progress.user_id
				  AND hhi.profile_id = user_watch_progress.profile_id
				  AND hhi.media_item_id = user_watch_progress.media_item_id
				  AND user_watch_progress.updated_at <= hhi.hidden_before
			  )
			ORDER BY updated_at DESC
			LIMIT $3 OFFSET $4`
		args = []any{s.userID, profileID, limit, offset}
	default:
		query = `
			SELECT profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at,
			       last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
			FROM user_watch_progress
			WHERE user_id = $1 AND profile_id = $2
			  AND NOT EXISTS (
				SELECT 1
				FROM user_history_hidden_items hhi
				WHERE hhi.user_id = user_watch_progress.user_id
				  AND hhi.profile_id = user_watch_progress.profile_id
				  AND hhi.media_item_id = user_watch_progress.media_item_id
				  AND user_watch_progress.updated_at <= hhi.hidden_before
			  )
			ORDER BY updated_at DESC
			LIMIT $3 OFFSET $4`
		args = []any{s.userID, profileID, limit, offset}
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing progress: %w", err)
	}
	defer rows.Close()

	var results []userstore.WatchProgress
	for rows.Next() {
		wp, err := scanWatchProgress(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning progress row: %w", err)
		}
		results = append(results, *wp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating progress rows: %w", err)
	}
	return results, nil
}

func (s *PostgresUserStore) ListProgressByMediaItems(ctx context.Context, profileID string, mediaItemIDs []string) (map[string]userstore.WatchProgress, error) {
	result := make(map[string]userstore.WatchProgress, len(mediaItemIDs))
	if len(mediaItemIDs) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(mediaItemIDs))
	args := make([]any, 0, len(mediaItemIDs)+2)
	args = append(args, s.userID, profileID)
	for i, mediaItemID := range mediaItemIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+3)
		args = append(args, mediaItemID)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at,
		       last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
		FROM user_watch_progress
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id IN (`+strings.Join(placeholders, ",")+`)
		  AND NOT EXISTS (
			SELECT 1
			FROM user_history_hidden_items hhi
			WHERE hhi.user_id = user_watch_progress.user_id
			  AND hhi.profile_id = user_watch_progress.profile_id
			  AND hhi.media_item_id = user_watch_progress.media_item_id
			  AND user_watch_progress.updated_at <= hhi.hidden_before
		  )`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("listing progress by media items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		wp, err := scanWatchProgress(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning progress row: %w", err)
		}
		result[wp.MediaItemID] = *wp
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating progress rows: %w", err)
	}
	return result, nil
}

func (s *PostgresUserStore) UpdateProgressHints(ctx context.Context, profileID, mediaItemID string, hints userstore.VersionHints) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE user_watch_progress
		SET last_file_id = $4, last_resolution = $5, last_hdr = $6, last_codec_video = $7, last_edition_key = $8
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		s.userID, profileID, mediaItemID,
		hints.FileID, hints.Resolution, hints.HDR, hints.CodecVideo, nilIfEmpty(hints.EditionKey),
	)
	if err != nil {
		return fmt.Errorf("updating progress hints: %w", err)
	}
	return nil
}

func nilIfEmpty(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func (s *PostgresUserStore) AddHistory(ctx context.Context, entry userstore.WatchHistoryEntry) error {
	if entry.ID == "" {
		entry.ID = generateUUID()
	}
	if entry.WatchedAt == "" {
		entry.WatchedAt = nowUTC()
	}
	if entry.Source == "" {
		entry.Source = userstore.WatchHistorySourceLegacy
	}
	suppressed, err := s.historyIsHidden(ctx, entry.ProfileID, entry.MediaItemID, entry.WatchedAt)
	if err != nil {
		return err
	}
	if suppressed {
		return nil
	}
	identityJSON, err := json.Marshal(entry.Identity)
	if err != nil {
		return fmt.Errorf("marshaling watch identity: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, duration_seconds, completed, source, watch_identity)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		entry.ID, s.userID, entry.ProfileID, entry.MediaItemID,
		entry.WatchedAt, entry.DurationSeconds, entry.Completed, entry.Source,
		string(identityJSON),
	)
	if err != nil {
		return fmt.Errorf("adding history entry: %w", err)
	}
	return nil
}

func (s *PostgresUserStore) AddHistoryIfMissing(ctx context.Context, entry userstore.WatchHistoryEntry) (bool, error) {
	if entry.WatchedAt == "" {
		entry.WatchedAt = nowUTC()
	}
	suppressed, err := s.historyIsHidden(ctx, entry.ProfileID, entry.MediaItemID, entry.WatchedAt)
	if err != nil {
		return false, err
	}
	if suppressed {
		return false, nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM user_watch_history
			WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3 AND watched_at = $4
		)`,
		s.userID, entry.ProfileID, entry.MediaItemID, entry.WatchedAt,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking history row existence: %w", err)
	}
	if exists {
		return false, nil
	}
	if err := s.AddHistory(ctx, entry); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PostgresUserStore) ListHistory(ctx context.Context, profileID string, limit, offset int) ([]userstore.WatchHistoryEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT h.id, h.profile_id, h.media_item_id, h.watched_at, h.duration_seconds, h.completed, h.source, h.watch_identity::text
		FROM user_watch_history h
		WHERE h.user_id = $1 AND h.profile_id = $2
		  AND NOT EXISTS (
			SELECT 1
			FROM user_history_hidden_items hhi
			WHERE hhi.user_id = h.user_id
			  AND hhi.profile_id = h.profile_id
			  AND hhi.media_item_id = h.media_item_id
			  AND h.watched_at <= hhi.hidden_before
		  )
		ORDER BY watched_at DESC
		LIMIT $3 OFFSET $4`,
		s.userID, profileID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("listing history: %w", err)
	}
	defer rows.Close()

	var results []userstore.WatchHistoryEntry
	for rows.Next() {
		entry, err := scanWatchHistoryEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning history row: %w", err)
		}
		results = append(results, *entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating history rows: %w", err)
	}
	return results, nil
}

func (s *PostgresUserStore) ListCompletedHistory(ctx context.Context, query userstore.CompletedHistoryQuery) ([]userstore.WatchHistoryEntry, error) {
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	sources := make([]string, 0, len(query.ExcludeSources))
	for _, source := range query.ExcludeSources {
		sources = append(sources, string(source))
	}
	includeSources := make([]string, 0, len(query.IncludeSources))
	for _, source := range query.IncludeSources {
		includeSources = append(includeSources, string(source))
	}
	mediaItemIDs := compactMediaItemIDs(query.MediaItemIDs)
	rows, err := s.pool.Query(ctx, `
		SELECT h.id, h.profile_id, h.media_item_id, h.watched_at, h.duration_seconds, h.completed, h.source, h.watch_identity::text
		FROM user_watch_history h
		WHERE h.user_id = $1
		  AND h.profile_id = $2
		  AND h.completed = true
		  AND (cardinality($3::text[]) = 0 OR h.source = ANY($3::text[]))
		  AND (cardinality($4::text[]) = 0 OR h.source <> ALL($4::text[]))
		  AND (cardinality($5::text[]) = 0 OR h.media_item_id = ANY($5::text[]))
		  AND NOT EXISTS (
			SELECT 1
			FROM user_history_hidden_items hhi
			WHERE hhi.user_id = h.user_id
			  AND hhi.profile_id = h.profile_id
			  AND hhi.media_item_id = h.media_item_id
			  AND h.watched_at <= hhi.hidden_before
		)
		ORDER BY h.watched_at ASC
		LIMIT $6 OFFSET $7`,
		s.userID, query.ProfileID, includeSources, sources, mediaItemIDs, limit, query.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("listing completed history: %w", err)
	}
	defer rows.Close()

	var results []userstore.WatchHistoryEntry
	for rows.Next() {
		entry, err := scanWatchHistoryEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning completed history row: %w", err)
		}
		results = append(results, *entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating completed history rows: %w", err)
	}
	return results, nil
}

func (s *PostgresUserStore) RemoveHistoryItems(
	ctx context.Context,
	profileID string,
	mediaItemIDs []string,
	removedAt time.Time,
) error {
	mediaItemIDs = compactMediaItemIDs(mediaItemIDs)
	if len(mediaItemIDs) == 0 {
		return nil
	}
	if removedAt.IsZero() {
		removedAt = time.Now().UTC()
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin remove history items: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO user_history_hidden_items (user_id, profile_id, media_item_id, hidden_before, updated_at)
		SELECT $1, $2, unnest($3::text[]), $4, $4
		ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE SET
			hidden_before = GREATEST(user_history_hidden_items.hidden_before, EXCLUDED.hidden_before),
			updated_at = EXCLUDED.updated_at
	`, s.userID, profileID, mediaItemIDs, removedAt.UTC()); err != nil {
		return fmt.Errorf("upserting hidden history items: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM user_watch_history
		WHERE user_id = $1
		  AND profile_id = $2
		  AND media_item_id = ANY($3::text[])
		  AND watched_at <= $4
	`, s.userID, profileID, mediaItemIDs, removedAt.UTC()); err != nil {
		return fmt.Errorf("deleting removed history rows: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM user_watch_progress
		WHERE user_id = $1
		  AND profile_id = $2
		  AND media_item_id = ANY($3::text[])
	`, s.userID, profileID, mediaItemIDs); err != nil {
		return fmt.Errorf("deleting removed progress rows: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit remove history items: %w", err)
	}
	return nil
}

func (s *PostgresUserStore) DeleteHistoryBySource(ctx context.Context, profileID string, mediaItemIDs []string, source userstore.WatchHistorySource) error {
	if len(mediaItemIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM user_watch_history
		WHERE user_id = $1 AND profile_id = $2 AND source = $3 AND media_item_id = ANY($4::text[])`,
		s.userID, profileID, source, mediaItemIDs,
	)
	if err != nil {
		return fmt.Errorf("deleting history by source: %w", err)
	}
	return nil
}

func (s *PostgresUserStore) historyIsHidden(
	ctx context.Context,
	profileID, mediaItemID, watchedAt string,
) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM user_history_hidden_items
			WHERE user_id = $1
			  AND profile_id = $2
			  AND media_item_id = $3
			  AND hidden_before >= $4::timestamptz
		)
	`, s.userID, profileID, mediaItemID, watchedAt).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking hidden history item: %w", err)
	}
	return exists, nil
}

func compactMediaItemIDs(mediaItemIDs []string) []string {
	result := make([]string, 0, len(mediaItemIDs))
	seen := make(map[string]struct{}, len(mediaItemIDs))
	for _, mediaItemID := range mediaItemIDs {
		mediaItemID = strings.TrimSpace(mediaItemID)
		if mediaItemID == "" {
			continue
		}
		if _, ok := seen[mediaItemID]; ok {
			continue
		}
		seen[mediaItemID] = struct{}{}
		result = append(result, mediaItemID)
	}
	return result
}
