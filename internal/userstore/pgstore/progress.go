package pgstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
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
	position, completed, skip := userstore.ResolveProgressState(position, duration, thresholds)
	if skip {
		return nil
	}
	now := time.Now().UTC()
	// `completed` is a one-way watched latch; position resets to 0 on
	// completion so `position_seconds > 0` means "has a resume point".
	// Position itself is last-write-wins: a deliberate backward seek is a
	// legitimate resume point (the old GREATEST clamp made "rewind and stop"
	// resume at the stale, later position on every client). Rewatching a
	// completed row still re-enters Continue Watching (stored position is 0,
	// any heartbeat replaces it) while the watched flag survives.
	_, err := s.pool.Exec(ctx, `
		WITH visible AS (
			SELECT
				CASE
					WHEN hhi.hidden_before IS NOT NULL AND $7::timestamptz <= hhi.hidden_before
					THEN hhi.hidden_before + interval '1 second'
					ELSE $7::timestamptz
				END AS updated_at
			FROM (SELECT 1) seed
			LEFT JOIN user_history_hidden_items hhi
			  ON hhi.user_id = $1
			 AND hhi.profile_id = $2
			 AND hhi.media_item_id = $3
		)
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		SELECT $1, $2, $3, $4, $5, $6, updated_at
		FROM visible
		ON CONFLICT(user_id, profile_id, media_item_id) DO UPDATE SET
			position_seconds = CASE WHEN excluded.completed THEN 0
				ELSE excluded.position_seconds END,
			duration_seconds = excluded.duration_seconds,
			completed = CASE WHEN excluded.completed
				THEN TRUE ELSE user_watch_progress.completed END,
			updated_at = excluded.updated_at,
			event_at = excluded.updated_at`,
		s.userID, profileID, mediaItemID, position, duration, completed, now,
	)
	if err != nil {
		return fmt.Errorf("updating progress: %w", err)
	}
	return nil
}

// SetProgress bypasses the forward-only guard after the min-resume threshold.
func (s *PostgresUserStore) SetProgress(ctx context.Context, profileID, mediaItemID string, position, duration float64, thresholds userstore.ProgressThresholds) error {
	position, completed, skip := userstore.ResolveProgressState(position, duration, thresholds)
	if skip {
		return nil
	}
	now := time.Now().UTC()
	_, err := s.pool.Exec(ctx, `
		WITH visible AS (
			SELECT
				CASE
					WHEN hhi.hidden_before IS NOT NULL AND $7::timestamptz <= hhi.hidden_before
					THEN hhi.hidden_before + interval '1 second'
					ELSE $7::timestamptz
				END AS updated_at
			FROM (SELECT 1) seed
			LEFT JOIN user_history_hidden_items hhi
			  ON hhi.user_id = $1
			 AND hhi.profile_id = $2
			 AND hhi.media_item_id = $3
		)
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		SELECT $1, $2, $3, $4, $5, $6, updated_at
		FROM visible
		ON CONFLICT(user_id, profile_id, media_item_id) DO UPDATE SET
			position_seconds = excluded.position_seconds,
			duration_seconds = excluded.duration_seconds,
			completed = user_watch_progress.completed OR excluded.completed,
			updated_at = excluded.updated_at,
			event_at = excluded.updated_at`,
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
			updated_at = excluded.updated_at,
			event_at = excluded.updated_at`,
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
	// event_at is the LWW comparison key (the clamped client event time); the
	// synced_seq cursor is stamped server-side by the user_watch_progress trigger.
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at, event_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		ON CONFLICT(user_id, profile_id, media_item_id) DO UPDATE SET
				position_seconds = EXCLUDED.position_seconds,
				duration_seconds = EXCLUDED.duration_seconds,
				completed = user_watch_progress.completed OR EXCLUDED.completed,
				updated_at = EXCLUDED.updated_at,
				event_at = EXCLUDED.event_at
			WHERE EXCLUDED.event_at > user_watch_progress.event_at`,
		s.userID, profileID, mediaItemID, position, duration, completed, updatedAt.UTC(),
	)
	if err != nil {
		return false, fmt.Errorf("setting newer progress: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListProgressSince returns user_watch_progress rows whose server cursor
// (synced_seq) exceeds cursor, in cursor order, with the next cursor to resume
// from. Delta delivery is driven only by synced_seq, never a client clock.
func (s *PostgresUserStore) ListProgressSince(ctx context.Context, profileID, cursor string) ([]userstore.WatchProgress, string, error) {
	c, _ := strconv.ParseInt(cursor, 10, 64) // empty/invalid cursor → 0 (full delta)
	const limit = 500
	rows, err := s.pool.Query(ctx, `
		SELECT profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at, synced_seq,
		       last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
		FROM user_watch_progress
		WHERE user_id = $1 AND profile_id = $2 AND synced_seq IS NOT NULL AND synced_seq > $3
		  AND NOT EXISTS (
			SELECT 1
			FROM user_history_hidden_items hhi
			WHERE hhi.user_id = user_watch_progress.user_id
			  AND hhi.profile_id = user_watch_progress.profile_id
			  AND hhi.media_item_id = user_watch_progress.media_item_id
			  AND user_watch_progress.updated_at <= hhi.hidden_before
		  )
		ORDER BY synced_seq ASC
		LIMIT $4`,
		s.userID, profileID, c, limit,
	)
	if err != nil {
		return nil, cursor, fmt.Errorf("listing progress since: %w", err)
	}
	defer rows.Close()

	next := c
	var results []userstore.WatchProgress
	for rows.Next() {
		var wp userstore.WatchProgress
		var updatedAt time.Time
		var seq int64
		if err := rows.Scan(
			&wp.ProfileID, &wp.MediaItemID, &wp.PositionSeconds, &wp.DurationSeconds, &wp.Completed, &updatedAt, &seq,
			&wp.LastFileID, &wp.LastResolution, &wp.LastHDR, &wp.LastCodecVideo, &wp.LastEditionKey,
		); err != nil {
			return nil, cursor, fmt.Errorf("scanning progress since row: %w", err)
		}
		wp.UpdatedAt = timeToString(updatedAt)
		if seq > next {
			next = seq
		}
		results = append(results, wp)
	}
	if err := rows.Err(); err != nil {
		return nil, cursor, fmt.Errorf("iterating progress since rows: %w", err)
	}
	return results, strconv.FormatInt(next, 10), nil
}

func (s *PostgresUserStore) MarkWatched(ctx context.Context, profileID, mediaItemID string, duration float64) error {
	if duration < 0 {
		duration = 0
	}

	now := time.Now().UTC()
	_, err := s.pool.Exec(ctx, `
		WITH visible AS (
			SELECT
				CASE
					WHEN hhi.hidden_before IS NOT NULL AND $5::timestamptz <= hhi.hidden_before
					THEN hhi.hidden_before + interval '1 second'
					ELSE $5::timestamptz
				END AS updated_at
			FROM (SELECT 1) seed
			LEFT JOIN user_history_hidden_items hhi
			  ON hhi.user_id = $1
			 AND hhi.profile_id = $2
			 AND hhi.media_item_id = $3
		)
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		SELECT $1, $2, $3, 0, $4, TRUE, updated_at
		FROM visible
		ON CONFLICT(user_id, profile_id, media_item_id) DO UPDATE SET
			position_seconds = 0,
			duration_seconds = excluded.duration_seconds,
			completed = TRUE,
			updated_at = excluded.updated_at,
			event_at = excluded.updated_at`,
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
		WITH target(media_item_id) AS (
			SELECT unnest($3::text[])
		),
		visible AS (
			SELECT
				t.media_item_id,
				CASE
					WHEN hhi.hidden_before IS NOT NULL AND $4::timestamptz <= hhi.hidden_before
					THEN hhi.hidden_before + interval '1 second'
					ELSE $4::timestamptz
				END AS updated_at
			FROM target t
			LEFT JOIN user_history_hidden_items hhi
			  ON hhi.user_id = $1
			 AND hhi.profile_id = $2
			 AND hhi.media_item_id = t.media_item_id
		)
		INSERT INTO user_watch_progress
			(user_id, profile_id, media_item_id, completed, position_seconds, duration_seconds, updated_at)
		SELECT $1, $2, media_item_id, TRUE, 0, 0, updated_at
		FROM visible
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
// `completed = FALSE, position_seconds = 0` in a single UPDATE. Rows that
// don't exist are silently skipped. (Mark-unplayed flows currently go through
// RemoveHistoryItems; this remains on the interface for bulk resets.)
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

// ListProgressFiltered mirrors the status branches of ListProgress and AND-s in
// an EXISTS pre-filter so the requested item types and/or library are resolved
// in SQL instead of after a full-set scan. Movies/series resolve through
// media_items; episodes live in the separate episodes table joined via
// series_id → media_items (a plain media_items join would miss them); the
// optional library predicate hits media_item_libraries. The completed branch's
// `completed = TRUE` + `ORDER BY updated_at DESC` shape keeps
// idx_uwp_profile_completed in play, while the EXISTS sub-selects ride
// idx_item_libraries_content. The filter is coarse (callers re-check
// access/parental exclusions over the hydrated rows), and an empty types slice
// with a nil libraryID degrades to the plain status listing.
func (s *PostgresUserStore) ListProgressFiltered(ctx context.Context, profileID, status string, types []string, libraryID *int, limit, offset int) ([]userstore.WatchProgress, error) {
	args := []any{s.userID, profileID}

	var statusClause string
	switch status {
	case "in_progress":
		statusClause = "position_seconds > 0"
	case "completed":
		statusClause = "completed = TRUE"
	default:
		statusClause = "TRUE"
	}

	var filterClause string
	filterClause, args = buildProgressCatalogFilter(types, libraryID, args)

	args = append(args, limit, offset)
	limitPlaceholder := fmt.Sprintf("$%d", len(args)-1)
	offsetPlaceholder := fmt.Sprintf("$%d", len(args))

	query := `
		SELECT profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at,
		       last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
		FROM user_watch_progress
		WHERE user_id = $1 AND profile_id = $2 AND ` + statusClause + filterClause + `
		  AND NOT EXISTS (
			SELECT 1
			FROM user_history_hidden_items hhi
			WHERE hhi.user_id = user_watch_progress.user_id
			  AND hhi.profile_id = user_watch_progress.profile_id
			  AND hhi.media_item_id = user_watch_progress.media_item_id
			  AND user_watch_progress.updated_at <= hhi.hidden_before
		  )
		ORDER BY updated_at DESC
		LIMIT ` + limitPlaceholder + ` OFFSET ` + offsetPlaceholder

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing filtered progress: %w", err)
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

// buildProgressCatalogFilter builds the EXISTS pre-filter that constrains
// user_watch_progress rows to the requested item types and/or library. It
// appends any new bind args to args and returns the SQL fragment (a leading
// " AND (...)" or empty when no filter applies) plus the grown args slice.
func buildProgressCatalogFilter(types []string, libraryID *int, args []any) (string, []any) {
	wantEpisode := false
	nonEpisode := make([]string, 0, len(types))
	for _, t := range types {
		switch lt := strings.ToLower(strings.TrimSpace(t)); lt {
		case "":
			// skip blanks
		case "episode":
			wantEpisode = true
		default:
			nonEpisode = append(nonEpisode, lt)
		}
	}

	typed := len(nonEpisode) > 0 || wantEpisode
	if !typed && libraryID == nil {
		return "", args
	}

	// A library-only request (no types) must match both movies and episodes in
	// that library, so include both branches when no type was requested.
	includeMovie := !typed || len(nonEpisode) > 0
	includeEpisode := !typed || wantEpisode

	var typePlaceholder, libPlaceholder string
	if len(nonEpisode) > 0 {
		args = append(args, nonEpisode)
		typePlaceholder = fmt.Sprintf("$%d", len(args))
	}
	if libraryID != nil {
		args = append(args, *libraryID)
		libPlaceholder = fmt.Sprintf("$%d", len(args))
	}

	branches := make([]string, 0, 2)
	if includeMovie {
		var sb strings.Builder
		sb.WriteString("EXISTS (SELECT 1 FROM media_items mi")
		if libPlaceholder != "" {
			sb.WriteString(" JOIN media_item_libraries mil ON mi.content_id = mil.content_id")
		}
		sb.WriteString(" WHERE mi.content_id = user_watch_progress.media_item_id")
		if typePlaceholder != "" {
			sb.WriteString(" AND lower(mi.type) = ANY(" + typePlaceholder + ")")
		}
		if libPlaceholder != "" {
			sb.WriteString(" AND mil.media_folder_id = " + libPlaceholder)
		}
		sb.WriteString(")")
		branches = append(branches, sb.String())
	}
	if includeEpisode {
		var sb strings.Builder
		sb.WriteString("EXISTS (SELECT 1 FROM episodes e")
		if libPlaceholder != "" {
			sb.WriteString(" JOIN media_items si ON e.series_id = si.content_id")
			sb.WriteString(" JOIN media_item_libraries mil ON si.content_id = mil.content_id")
		}
		sb.WriteString(" WHERE e.content_id = user_watch_progress.media_item_id")
		if libPlaceholder != "" {
			sb.WriteString(" AND mil.media_folder_id = " + libPlaceholder)
		}
		sb.WriteString(")")
		branches = append(branches, sb.String())
	}

	return " AND (" + strings.Join(branches, " OR ") + ")", args
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

func (s *PostgresUserStore) AddVisibleHistory(ctx context.Context, entry userstore.WatchHistoryEntry) (userstore.WatchHistoryEntry, error) {
	if entry.ID == "" {
		entry.ID = generateUUID()
	}
	if entry.WatchedAt == "" {
		entry.WatchedAt = nowUTC()
	}
	if entry.Source == "" {
		entry.Source = userstore.WatchHistorySourceLegacy
	}
	identityJSON, err := json.Marshal(entry.Identity)
	if err != nil {
		return entry, fmt.Errorf("marshaling watch identity: %w", err)
	}
	var watchedAt time.Time
	if err := s.pool.QueryRow(ctx, `
		WITH visible AS (
			SELECT
				CASE
					WHEN hhi.hidden_before IS NOT NULL AND $5::timestamptz <= hhi.hidden_before
					THEN hhi.hidden_before + interval '1 second'
					ELSE $5::timestamptz
				END AS watched_at
			FROM (SELECT 1) seed
			LEFT JOIN user_history_hidden_items hhi
			  ON hhi.user_id = $2
			 AND hhi.profile_id = $3
			 AND hhi.media_item_id = $4
		)
		INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, duration_seconds, completed, source, watch_identity)
		SELECT $1, $2, $3, $4, watched_at, $6, $7, $8, $9
		FROM visible
		RETURNING watched_at`,
		entry.ID, s.userID, entry.ProfileID, entry.MediaItemID, entry.WatchedAt,
		entry.DurationSeconds, entry.Completed, entry.Source, string(identityJSON),
	).Scan(&watchedAt); err != nil {
		return entry, fmt.Errorf("adding visible history entry: %w", err)
	}
	entry.WatchedAt = timeToString(watchedAt)
	return entry, nil
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
	includeSources, excludeSources, mediaItemIDs := completedHistoryFilterArgs(query.MediaItemIDs, query.IncludeSources, query.ExcludeSources)
	rows, err := s.pool.Query(ctx, `
		SELECT h.id, h.profile_id, h.media_item_id, h.watched_at, h.duration_seconds, h.completed, h.source, h.watch_identity::text
		FROM user_watch_history h
		WHERE h.user_id = $1
		  AND h.profile_id = $2
		  AND h.completed = true
		  AND (cardinality($3::text[]) = 0 OR h.source = ANY($3::text[]))
		  AND (cardinality($4::text[]) = 0 OR h.source <> ALL($4::text[]))
		  AND (cardinality($5::text[]) = 0 OR h.media_item_id = ANY($5::text[]))
		`+completedHistoryVisibleSQL+`
		ORDER BY h.watched_at ASC, h.id ASC
		LIMIT $6 OFFSET $7`,
		s.userID, query.ProfileID, includeSources, excludeSources, mediaItemIDs, limit, query.Offset,
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

func (s *PostgresUserStore) ListCompletedHistoryItems(ctx context.Context, query userstore.CompletedHistoryItemQuery) ([]userstore.CompletedHistoryItem, error) {
	includeSources, excludeSources, mediaItemIDs := completedHistoryFilterArgs(query.MediaItemIDs, query.IncludeSources, query.ExcludeSources)
	rows, err := s.pool.Query(ctx, `
		SELECT h.media_item_id, MAX(h.watched_at)
		FROM user_watch_history h
		WHERE h.user_id = $1
		  AND h.profile_id = $2
		  AND h.completed = true
		  AND (cardinality($3::text[]) = 0 OR h.source = ANY($3::text[]))
		  AND (cardinality($4::text[]) = 0 OR h.source <> ALL($4::text[]))
		  AND (cardinality($5::text[]) = 0 OR h.media_item_id = ANY($5::text[]))
		`+completedHistoryVisibleSQL+`
		GROUP BY h.media_item_id
		ORDER BY h.media_item_id ASC`,
		s.userID, query.ProfileID, includeSources, excludeSources, mediaItemIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("listing completed history items: %w", err)
	}
	defer rows.Close()

	var results []userstore.CompletedHistoryItem
	for rows.Next() {
		var item userstore.CompletedHistoryItem
		var watchedAt time.Time
		if err := rows.Scan(&item.MediaItemID, &watchedAt); err != nil {
			return nil, fmt.Errorf("scanning completed history item: %w", err)
		}
		item.WatchedAt = timeToString(watchedAt)
		results = append(results, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating completed history items: %w", err)
	}
	return results, nil
}

func (s *PostgresUserStore) VisibleHistoryTimestamps(ctx context.Context, profileID string, mediaItemIDs []string, at time.Time) (map[string]string, error) {
	mediaItemIDs = compactMediaItemIDs(mediaItemIDs)
	result := make(map[string]string, len(mediaItemIDs))
	if len(mediaItemIDs) == 0 {
		return result, nil
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	rows, err := s.pool.Query(ctx, `
		SELECT t.media_item_id, hhi.hidden_before
		FROM unnest($3::text[]) AS t(media_item_id)
		LEFT JOIN user_history_hidden_items hhi
		  ON hhi.user_id = $1
		 AND hhi.profile_id = $2
		 AND hhi.media_item_id = t.media_item_id`,
		s.userID, profileID, mediaItemIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("listing visible history timestamps: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var mediaItemID string
		var hiddenBefore sql.NullTime
		if err := rows.Scan(&mediaItemID, &hiddenBefore); err != nil {
			return nil, fmt.Errorf("scanning visible history timestamp: %w", err)
		}
		result[mediaItemID] = visibleTimestampAfterHiddenTime(at, hiddenBefore)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating visible history timestamps: %w", err)
	}
	return result, nil
}

const completedHistoryVisibleSQL = `
		  AND NOT EXISTS (
			SELECT 1
			FROM user_history_hidden_items hhi
			WHERE hhi.user_id = h.user_id
			  AND hhi.profile_id = h.profile_id
			  AND hhi.media_item_id = h.media_item_id
			  AND h.watched_at <= hhi.hidden_before
		)`

func completedHistoryFilterArgs(
	mediaItemIDs []string,
	includeSources []userstore.WatchHistorySource,
	excludeSources []userstore.WatchHistorySource,
) ([]string, []string, []string) {
	include := make([]string, 0, len(includeSources))
	for _, source := range includeSources {
		include = append(include, string(source))
	}
	exclude := make([]string, 0, len(excludeSources))
	for _, source := range excludeSources {
		exclude = append(exclude, string(source))
	}
	return include, exclude, compactMediaItemIDs(mediaItemIDs)
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
		WITH target(media_item_id) AS (
			SELECT unnest($3::text[])
		),
		watermark AS (
			SELECT
				t.media_item_id,
				GREATEST($4::timestamptz, COALESCE(MAX(h.watched_at), $4::timestamptz)) AS hidden_before
			FROM target t
			LEFT JOIN user_watch_history h
			  ON h.user_id = $1
			 AND h.profile_id = $2
			 AND h.media_item_id = t.media_item_id
			GROUP BY t.media_item_id
		)
		INSERT INTO user_history_hidden_items (user_id, profile_id, media_item_id, hidden_before, updated_at)
		SELECT $1, $2, media_item_id, hidden_before, $4
		FROM watermark
		ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE SET
			hidden_before = GREATEST(user_history_hidden_items.hidden_before, EXCLUDED.hidden_before),
			updated_at = EXCLUDED.updated_at
	`, s.userID, profileID, mediaItemIDs, removedAt.UTC()); err != nil {
		return fmt.Errorf("upserting hidden history items: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM user_watch_history h
		USING user_history_hidden_items hhi
		WHERE h.user_id = $1
		  AND h.profile_id = $2
		  AND h.media_item_id = ANY($3::text[])
		  AND hhi.user_id = h.user_id
		  AND hhi.profile_id = h.profile_id
		  AND hhi.media_item_id = h.media_item_id
		  AND h.watched_at <= hhi.hidden_before
	`, s.userID, profileID, mediaItemIDs); err != nil {
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

func visibleTimestampAfterHiddenTime(at time.Time, hiddenBefore sql.NullTime) string {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	at = at.UTC()
	if !hiddenBefore.Valid || at.After(hiddenBefore.Time) {
		return timeToString(at)
	}
	return timeToString(hiddenBefore.Time.UTC().Add(time.Second))
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
