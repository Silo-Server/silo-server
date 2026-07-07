package userdb

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// WatchProgress is an alias for the canonical type in userstore.
type WatchProgress = userstore.WatchProgress

// WatchHistoryEntry is an alias for the canonical type in userstore.
type WatchHistoryEntry = userstore.WatchHistoryEntry

// UpdateProgress uses the forward-only guard - position only moves forward.
// The position is only updated if the new value is greater than the existing one.
// The completed flag is set to true when position/duration exceeds the watched threshold.
func UpdateProgress(db *sql.DB, profileID, mediaItemID string, position, duration float64, thresholds userstore.ProgressThresholds) error {
	position, completed, skip := userstore.ResolveProgressState(position, duration, thresholds)
	if skip {
		return nil
	}
	now := nowUTC()
	// Mirrors the Postgres pgstore UpdateProgress: `completed` is a one-way
	// watched latch; position resets to 0 on completion so `position_seconds
	// > 0` means "has a resume point". Position itself is last-write-wins —
	// a deliberate backward seek is a legitimate resume point (the old MAX
	// clamp made "rewind and stop" resume at the stale, later position).
	// Rewatching a completed row still re-enters Continue Watching (stored
	// position is 0, any heartbeat replaces it) while the watched flag
	// survives.
	query := `
		INSERT INTO watch_progress (profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		SELECT ?, ?, ?, ?, ?, ` + visibleTimestampSQL + `
		FROM (SELECT 1) seed
		LEFT JOIN hidden_history_items hhi
		  ON hhi.profile_id = ?
		 AND hhi.media_item_id = ?
		WHERE true
		ON CONFLICT(profile_id, media_item_id) DO UPDATE SET
			position_seconds = CASE WHEN excluded.completed = 1 THEN 0
				ELSE excluded.position_seconds END,
			duration_seconds = excluded.duration_seconds,
			completed = CASE WHEN excluded.completed = 1
				THEN 1 ELSE watch_progress.completed END,
			updated_at = excluded.updated_at,
			event_at = excluded.updated_at
	`
	_, err := db.Exec(query, profileID, mediaItemID, position, duration, completed, now, now, profileID, mediaItemID)
	if err != nil {
		return fmt.Errorf("updating progress: %w", err)
	}
	return nil
}

// SetProgress bypasses the forward-only guard (for rewatches/explicit seek)
// after the min-resume threshold. The completed flag stays a one-way watched
// latch: only ClearProgress/ClearProgressBatch (mark unwatched) release it.
func SetProgress(db *sql.DB, profileID, mediaItemID string, position, duration float64, thresholds userstore.ProgressThresholds) error {
	position, completed, skip := userstore.ResolveProgressState(position, duration, thresholds)
	if skip {
		return nil
	}
	now := nowUTC()
	query := `
		INSERT INTO watch_progress (profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		SELECT ?, ?, ?, ?, ?, ` + visibleTimestampSQL + `
		FROM (SELECT 1) seed
		LEFT JOIN hidden_history_items hhi
		  ON hhi.profile_id = ?
		 AND hhi.media_item_id = ?
		WHERE true
		ON CONFLICT(profile_id, media_item_id) DO UPDATE SET
			position_seconds = excluded.position_seconds,
			duration_seconds = excluded.duration_seconds,
			completed = watch_progress.completed OR excluded.completed,
			updated_at = excluded.updated_at,
			event_at = excluded.updated_at
	`
	_, err := db.Exec(query, profileID, mediaItemID, position, duration, completed, now, now, profileID, mediaItemID)
	if err != nil {
		return fmt.Errorf("setting progress: %w", err)
	}
	return nil
}

// SetProgressAt writes progress using an explicit timestamp and completion state.
func SetProgressAt(db *sql.DB, profileID, mediaItemID string, position, duration float64, completed bool, updatedAt time.Time) error {
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
	suppressed, err := historyIsHidden(db, profileID, mediaItemID, updatedAtText)
	if err != nil {
		return err
	}
	if suppressed {
		return nil
	}
	query := `
		INSERT INTO watch_progress (profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile_id, media_item_id) DO UPDATE SET
			position_seconds = excluded.position_seconds,
			duration_seconds = excluded.duration_seconds,
			completed = watch_progress.completed OR excluded.completed,
			updated_at = excluded.updated_at,
			event_at = excluded.updated_at
	`
	_, err = db.Exec(query, profileID, mediaItemID, position, duration, completed, updatedAtText)
	if err != nil {
		return fmt.Errorf("setting progress at time: %w", err)
	}
	return nil
}

func SetProgressIfNewer(db *sql.DB, profileID, mediaItemID string, position, duration float64, completed bool, updatedAt time.Time) (bool, error) {
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
	suppressed, err := historyIsHidden(db, profileID, mediaItemID, updatedAtText)
	if err != nil {
		return false, err
	}
	if suppressed {
		return false, nil
	}
	// event_at is the LWW comparison key (the clamped client event time); the
	// synced_seq cursor is stamped server-side by the watch_progress triggers.
	res, err := db.Exec(`
		INSERT INTO watch_progress (profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at, event_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile_id, media_item_id) DO UPDATE SET
			position_seconds = excluded.position_seconds,
			duration_seconds = excluded.duration_seconds,
			completed = watch_progress.completed OR excluded.completed,
			updated_at = excluded.updated_at,
			event_at = excluded.event_at
		WHERE excluded.event_at > watch_progress.event_at
	`, profileID, mediaItemID, position, duration, completed, updatedAtText, updatedAtText)
	if err != nil {
		return false, fmt.Errorf("setting newer progress: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

// MarkWatched creates or replaces progress with a completed entry.
func MarkWatched(db *sql.DB, profileID, mediaItemID string, duration float64) error {
	if duration < 0 {
		duration = 0
	}

	now := nowUTC()
	query := `
		INSERT INTO watch_progress (profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		SELECT ?, ?, 0, ?, 1, ` + visibleTimestampSQL + `
		FROM (SELECT 1) seed
		LEFT JOIN hidden_history_items hhi
		  ON hhi.profile_id = ?
		 AND hhi.media_item_id = ?
		WHERE true
		ON CONFLICT(profile_id, media_item_id) DO UPDATE SET
			position_seconds = 0,
			duration_seconds = excluded.duration_seconds,
			completed = 1,
			updated_at = excluded.updated_at,
			event_at = excluded.updated_at
	`
	_, err := db.Exec(query, profileID, mediaItemID, duration, now, now, profileID, mediaItemID)
	if err != nil {
		return fmt.Errorf("marking watched: %w", err)
	}
	return nil
}

// ClearProgress removes any saved resume or watched state for an item.
func ClearProgress(db *sql.DB, profileID, mediaItemID string) error {
	_, err := db.Exec(
		`DELETE FROM watch_progress WHERE profile_id = ? AND media_item_id = ?`,
		profileID,
		mediaItemID,
	)
	if err != nil {
		return fmt.Errorf("clearing progress: %w", err)
	}
	return nil
}

// MarkProgressBatch marks every (profile, media_item_id) pair as completed in a
// single SQLite statement.
func MarkProgressBatch(db *sql.DB, profileID string, mediaItemIDs []string, updatedAt time.Time) error {
	mediaItemIDs = compactText(mediaItemIDs)
	if len(mediaItemIDs) == 0 {
		return nil
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	updatedAtText := updatedAt.UTC().Format(time.RFC3339)
	targetValues := make([]string, len(mediaItemIDs))
	args := make([]any, 0, len(mediaItemIDs)+4)
	for i, mediaItemID := range mediaItemIDs {
		targetValues[i] = "(?)"
		args = append(args, mediaItemID)
	}
	args = append(args, updatedAtText, updatedAtText, profileID, profileID)
	if _, err := db.Exec(`
		WITH target(media_item_id) AS (
			VALUES `+strings.Join(targetValues, ",")+`
		),
		visible AS (
			SELECT
				t.media_item_id,
				`+visibleTimestampSQL+` AS updated_at
			FROM target t
			LEFT JOIN hidden_history_items hhi
			  ON hhi.profile_id = ?
			 AND hhi.media_item_id = t.media_item_id
		)
		INSERT INTO watch_progress (profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		SELECT ?, media_item_id, 0, 0, 1, updated_at
		FROM visible
		WHERE true
		ON CONFLICT(profile_id, media_item_id) DO UPDATE SET
			completed = 1,
			position_seconds = 0,
			updated_at = excluded.updated_at
		WHERE watch_progress.completed != 1
		   OR watch_progress.updated_at < excluded.updated_at
	`, args...); err != nil {
		return fmt.Errorf("marking progress batch: %w", err)
	}
	return nil
}

// ClearProgressBatch resets every (profile, media_item_id) pair to
// completed=false, position=0 in a single statement. SQLite supports IN(...)
// with placeholders so this is truly one UPDATE.
func ClearProgressBatch(db *sql.DB, profileID string, mediaItemIDs []string, updatedAt time.Time) error {
	mediaItemIDs = compactText(mediaItemIDs)
	if len(mediaItemIDs) == 0 {
		return nil
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	updatedAtText := updatedAt.UTC().Format(time.RFC3339)
	placeholders := make([]string, len(mediaItemIDs))
	args := make([]any, 0, len(mediaItemIDs)+2)
	args = append(args, updatedAtText, profileID)
	for i, mediaItemID := range mediaItemIDs {
		placeholders[i] = "?"
		args = append(args, mediaItemID)
	}
	// Clear partially-watched rows (completed = 0 with position_seconds > 0)
	// in addition to fully-completed ones — the prior single-item ClearProgress
	// path DELETE-d unconditionally, so any non-default state must be cleared.
	// Skip rows already in the target state (completed = 0 AND
	// position_seconds = 0) to avoid pointless writes.
	if _, err := db.Exec(`
		UPDATE watch_progress
		SET completed = 0, position_seconds = 0, updated_at = ?
		WHERE profile_id = ?
		  AND media_item_id IN (`+strings.Join(placeholders, ",")+`)
		  AND (completed = 1 OR position_seconds <> 0)`,
		args...,
	); err != nil {
		return fmt.Errorf("clear progress batch: %w", err)
	}
	return nil
}

// UpdateProgressHints writes version hint columns for an existing progress row.
func UpdateProgressHints(db *sql.DB, profileID, mediaItemID string, hints userstore.VersionHints) error {
	_, err := db.Exec(`
		UPDATE watch_progress
		SET last_file_id = ?, last_resolution = ?, last_hdr = ?, last_codec_video = ?, last_edition_key = ?
		WHERE profile_id = ? AND media_item_id = ?`,
		hints.FileID, hints.Resolution, hints.HDR, hints.CodecVideo, nilIfBlank(hints.EditionKey),
		profileID, mediaItemID,
	)
	if err != nil {
		return fmt.Errorf("updating progress hints: %w", err)
	}
	return nil
}

// GetProgress returns progress for a specific item, or nil if not found.
func GetProgress(db *sql.DB, profileID, mediaItemID string) (*WatchProgress, error) {
	query := `
		SELECT profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at,
		       last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
		FROM watch_progress
		WHERE profile_id = ? AND media_item_id = ?
		  AND NOT EXISTS (
			SELECT 1
			FROM hidden_history_items hhi
			WHERE hhi.profile_id = watch_progress.profile_id
			  AND hhi.media_item_id = watch_progress.media_item_id
			  AND watch_progress.updated_at <= hhi.hidden_before
		  )
	`
	var wp WatchProgress
	err := db.QueryRow(query, profileID, mediaItemID).Scan(
		&wp.ProfileID, &wp.MediaItemID, &wp.PositionSeconds,
		&wp.DurationSeconds, &wp.Completed, &wp.UpdatedAt,
		&wp.LastFileID, &wp.LastResolution, &wp.LastHDR, &wp.LastCodecVideo, &wp.LastEditionKey,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting progress: %w", err)
	}
	return &wp, nil
}

// ListProgress returns paginated progress entries, filterable by status.
// Valid status values: "in_progress", "completed", "all" (or empty string for all).
func ListProgress(db *sql.DB, profileID string, status string, limit, offset int) ([]WatchProgress, error) {
	var query string
	var args []any

	switch status {
	case "in_progress":
		// position_seconds > 0 (not completed = 0): completed rows hold
		// position 0, so a rewatch of a watched item has completed = 1 with
		// a live resume point and belongs in Continue Watching.
		query = `
			SELECT profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at,
			       last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
			FROM watch_progress
			WHERE profile_id = ? AND position_seconds > 0
			  AND NOT EXISTS (
				SELECT 1
				FROM hidden_history_items hhi
				WHERE hhi.profile_id = watch_progress.profile_id
				  AND hhi.media_item_id = watch_progress.media_item_id
				  AND watch_progress.updated_at <= hhi.hidden_before
			  )
			ORDER BY updated_at DESC
			LIMIT ? OFFSET ?
		`
		args = []any{profileID, limit, offset}
	case "completed":
		query = `
			SELECT profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at,
			       last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
			FROM watch_progress
			WHERE profile_id = ? AND completed = 1
			  AND NOT EXISTS (
				SELECT 1
				FROM hidden_history_items hhi
				WHERE hhi.profile_id = watch_progress.profile_id
				  AND hhi.media_item_id = watch_progress.media_item_id
				  AND watch_progress.updated_at <= hhi.hidden_before
			  )
			ORDER BY updated_at DESC
			LIMIT ? OFFSET ?
		`
		args = []any{profileID, limit, offset}
	default: // "all" or ""
		query = `
			SELECT profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at,
			       last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
			FROM watch_progress
			WHERE profile_id = ?
			  AND NOT EXISTS (
				SELECT 1
				FROM hidden_history_items hhi
				WHERE hhi.profile_id = watch_progress.profile_id
				  AND hhi.media_item_id = watch_progress.media_item_id
				  AND watch_progress.updated_at <= hhi.hidden_before
			  )
			ORDER BY updated_at DESC
			LIMIT ? OFFSET ?
		`
		args = []any{profileID, limit, offset}
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing progress: %w", err)
	}
	defer rows.Close()

	var results []WatchProgress
	for rows.Next() {
		var wp WatchProgress
		if err := rows.Scan(
			&wp.ProfileID, &wp.MediaItemID, &wp.PositionSeconds,
			&wp.DurationSeconds, &wp.Completed, &wp.UpdatedAt,
			&wp.LastFileID, &wp.LastResolution, &wp.LastHDR, &wp.LastCodecVideo, &wp.LastEditionKey,
		); err != nil {
			return nil, fmt.Errorf("scanning progress row: %w", err)
		}
		results = append(results, wp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating progress rows: %w", err)
	}
	return results, nil
}

// ListProgressSince returns watch_progress rows whose server cursor (synced_seq)
// exceeds cursor, in cursor order, along with the next cursor to resume from.
// Delta delivery is driven only by synced_seq, never a client clock (invariant 1).
func ListProgressSince(db *sql.DB, profileID string, cursor int64, limit int) ([]WatchProgress, int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := db.Query(`
		SELECT profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at, synced_seq,
		       last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
		FROM watch_progress
		WHERE profile_id = ? AND synced_seq IS NOT NULL AND synced_seq > ?
		  AND NOT EXISTS (
			SELECT 1
			FROM hidden_history_items hhi
			WHERE hhi.profile_id = watch_progress.profile_id
			  AND hhi.media_item_id = watch_progress.media_item_id
			  AND watch_progress.updated_at <= hhi.hidden_before
		  )
		ORDER BY synced_seq ASC
		LIMIT ?
	`, profileID, cursor, limit)
	if err != nil {
		return nil, cursor, fmt.Errorf("listing progress since: %w", err)
	}
	defer rows.Close()

	next := cursor
	var results []WatchProgress
	for rows.Next() {
		var wp WatchProgress
		var seq int64
		if err := rows.Scan(
			&wp.ProfileID, &wp.MediaItemID, &wp.PositionSeconds, &wp.DurationSeconds, &wp.Completed, &wp.UpdatedAt, &seq,
			&wp.LastFileID, &wp.LastResolution, &wp.LastHDR, &wp.LastCodecVideo, &wp.LastEditionKey,
		); err != nil {
			return nil, cursor, fmt.Errorf("scanning progress since row: %w", err)
		}
		if seq > next {
			next = seq
		}
		results = append(results, wp)
	}
	if err := rows.Err(); err != nil {
		return nil, cursor, fmt.Errorf("iterating progress since rows: %w", err)
	}
	return results, next, nil
}

func ListProgressByMediaItems(db *sql.DB, profileID string, mediaItemIDs []string) (map[string]WatchProgress, error) {
	result := make(map[string]WatchProgress, len(mediaItemIDs))
	if len(mediaItemIDs) == 0 {
		return result, nil
	}

	placeholders := make([]string, len(mediaItemIDs))
	args := make([]any, 0, len(mediaItemIDs)+1)
	args = append(args, profileID)
	for i, mediaItemID := range mediaItemIDs {
		placeholders[i] = "?"
		args = append(args, mediaItemID)
	}

	rows, err := db.Query(
		`SELECT profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at,
		       last_file_id, last_resolution, last_hdr, last_codec_video, last_edition_key
		FROM watch_progress
		WHERE profile_id = ? AND media_item_id IN (`+strings.Join(placeholders, ",")+`)
		  AND NOT EXISTS (
			SELECT 1
			FROM hidden_history_items hhi
			WHERE hhi.profile_id = watch_progress.profile_id
			  AND hhi.media_item_id = watch_progress.media_item_id
			  AND watch_progress.updated_at <= hhi.hidden_before
		  )`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("listing progress by media items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var wp WatchProgress
		if err := rows.Scan(
			&wp.ProfileID, &wp.MediaItemID, &wp.PositionSeconds,
			&wp.DurationSeconds, &wp.Completed, &wp.UpdatedAt,
			&wp.LastFileID, &wp.LastResolution, &wp.LastHDR, &wp.LastCodecVideo, &wp.LastEditionKey,
		); err != nil {
			return nil, fmt.Errorf("scanning progress row: %w", err)
		}
		result[wp.MediaItemID] = wp
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating progress rows: %w", err)
	}
	return result, nil
}

func nilIfBlank(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// AddHistory adds a watch history entry. If the entry ID is empty, a UUID is generated.
// If WatchedAt is empty, it defaults to the current time.
func AddHistory(db *sql.DB, entry WatchHistoryEntry) error {
	if entry.ID == "" {
		entry.ID = generateUUID()
	}
	if entry.WatchedAt == "" {
		entry.WatchedAt = nowUTC()
	}
	if entry.Source == "" {
		entry.Source = userstore.WatchHistorySourceLegacy
	}
	suppressed, err := historyIsHidden(db, entry.ProfileID, entry.MediaItemID, entry.WatchedAt)
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
	query := `
		INSERT INTO watch_history (id, profile_id, media_item_id, watched_at, duration_seconds, completed, source, watch_identity)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err = db.Exec(query, entry.ID, entry.ProfileID, entry.MediaItemID,
		entry.WatchedAt, entry.DurationSeconds, entry.Completed, entry.Source, string(identityJSON))
	if err != nil {
		return fmt.Errorf("adding history entry: %w", err)
	}
	return nil
}

func AddVisibleHistory(db *sql.DB, entry WatchHistoryEntry) (WatchHistoryEntry, error) {
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
	if err := db.QueryRow(`
		INSERT INTO watch_history (id, profile_id, media_item_id, watched_at, duration_seconds, completed, source, watch_identity)
		SELECT ?, ?, ?, `+visibleTimestampSQL+`, ?, ?, ?, ?
		FROM (SELECT 1) seed
		LEFT JOIN hidden_history_items hhi
		  ON hhi.profile_id = ?
		 AND hhi.media_item_id = ?
		WHERE true
		RETURNING watched_at
	`, entry.ID, entry.ProfileID, entry.MediaItemID, entry.WatchedAt, entry.WatchedAt, entry.DurationSeconds, entry.Completed, entry.Source, string(identityJSON), entry.ProfileID, entry.MediaItemID).Scan(&entry.WatchedAt); err != nil {
		return entry, fmt.Errorf("adding visible history entry: %w", err)
	}
	return entry, nil
}

func AddHistoryIfMissing(db *sql.DB, entry WatchHistoryEntry) (bool, error) {
	if entry.WatchedAt == "" {
		entry.WatchedAt = nowUTC()
	}
	suppressed, err := historyIsHidden(db, entry.ProfileID, entry.MediaItemID, entry.WatchedAt)
	if err != nil {
		return false, err
	}
	if suppressed {
		return false, nil
	}
	var exists bool
	if err := db.QueryRow(
		`SELECT EXISTS(
			SELECT 1
			FROM watch_history
			WHERE profile_id = ? AND media_item_id = ? AND watched_at = ?
		)`,
		entry.ProfileID,
		entry.MediaItemID,
		entry.WatchedAt,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking history row existence: %w", err)
	}
	if exists {
		return false, nil
	}
	if err := AddHistory(db, entry); err != nil {
		return false, err
	}
	return true, nil
}

// ListHistory returns paginated watch history entries ordered by most recent first.
func ListHistory(db *sql.DB, profileID string, limit, offset int) ([]WatchHistoryEntry, error) {
	query := `
		SELECT h.id, h.profile_id, h.media_item_id, h.watched_at, h.duration_seconds, h.completed, h.source, h.watch_identity
		FROM watch_history h
		WHERE h.profile_id = ?
		  AND NOT EXISTS (
			SELECT 1
			FROM hidden_history_items hhi
			WHERE hhi.profile_id = h.profile_id
			  AND hhi.media_item_id = h.media_item_id
			  AND h.watched_at <= hhi.hidden_before
		  )
		ORDER BY watched_at DESC
		LIMIT ? OFFSET ?
	`
	rows, err := db.Query(query, profileID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing history: %w", err)
	}
	defer rows.Close()

	var results []WatchHistoryEntry
	for rows.Next() {
		var entry WatchHistoryEntry
		var identityJSON string
		if err := rows.Scan(
			&entry.ID, &entry.ProfileID, &entry.MediaItemID,
			&entry.WatchedAt, &entry.DurationSeconds, &entry.Completed, &entry.Source,
			&identityJSON,
		); err != nil {
			return nil, fmt.Errorf("scanning history row: %w", err)
		}
		if identityJSON != "" && identityJSON != "{}" {
			_ = json.Unmarshal([]byte(identityJSON), &entry.Identity)
		}
		results = append(results, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating history rows: %w", err)
	}
	return results, nil
}

func ListCompletedHistory(db *sql.DB, query userstore.CompletedHistoryQuery) ([]WatchHistoryEntry, error) {
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	filters, args := completedHistoryFilterSQL(query.ProfileID, query.MediaItemIDs, query.IncludeSources, query.ExcludeSources)
	args = append(args, limit, query.Offset)
	rows, err := db.Query(`
		SELECT h.id, h.profile_id, h.media_item_id, h.watched_at, h.duration_seconds, h.completed, h.source, h.watch_identity
		FROM watch_history h
		WHERE h.profile_id = ?
		  AND h.completed = 1
		`+filters+completedHistoryVisibleSQL+`
		ORDER BY h.watched_at ASC, h.id ASC
		LIMIT ? OFFSET ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("listing completed history: %w", err)
	}
	defer rows.Close()

	var results []WatchHistoryEntry
	for rows.Next() {
		var entry WatchHistoryEntry
		var identityJSON string
		if err := rows.Scan(
			&entry.ID, &entry.ProfileID, &entry.MediaItemID,
			&entry.WatchedAt, &entry.DurationSeconds, &entry.Completed, &entry.Source,
			&identityJSON,
		); err != nil {
			return nil, fmt.Errorf("scanning completed history row: %w", err)
		}
		if identityJSON != "" && identityJSON != "{}" {
			_ = json.Unmarshal([]byte(identityJSON), &entry.Identity)
		}
		results = append(results, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating completed history rows: %w", err)
	}
	return results, nil
}

func ListCompletedHistoryItems(db *sql.DB, query userstore.CompletedHistoryItemQuery) ([]userstore.CompletedHistoryItem, error) {
	filters, args := completedHistoryFilterSQL(query.ProfileID, query.MediaItemIDs, query.IncludeSources, query.ExcludeSources)
	rows, err := db.Query(`
		SELECT h.media_item_id, MAX(h.watched_at)
		FROM watch_history h
		WHERE h.profile_id = ?
		  AND h.completed = 1
		`+filters+completedHistoryVisibleSQL+`
		GROUP BY h.media_item_id
		ORDER BY h.media_item_id ASC`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("listing completed history items: %w", err)
	}
	defer rows.Close()

	var results []userstore.CompletedHistoryItem
	for rows.Next() {
		var item userstore.CompletedHistoryItem
		if err := rows.Scan(&item.MediaItemID, &item.WatchedAt); err != nil {
			return nil, fmt.Errorf("scanning completed history item: %w", err)
		}
		results = append(results, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating completed history items: %w", err)
	}
	return results, nil
}

const completedHistoryVisibleSQL = `
		  AND NOT EXISTS (
			SELECT 1
			FROM hidden_history_items hhi
			WHERE hhi.profile_id = h.profile_id
			  AND hhi.media_item_id = h.media_item_id
			  AND h.watched_at <= hhi.hidden_before
		  )`

const visibleTimestampSQL = `
			CASE
				WHEN hhi.hidden_before IS NOT NULL AND ? <= hhi.hidden_before
				THEN strftime('%Y-%m-%dT%H:%M:%SZ', hhi.hidden_before, '+1 second')
				ELSE ?
			END`

func completedHistoryFilterSQL(
	profileID string,
	mediaItemIDs []string,
	includeSources []userstore.WatchHistorySource,
	excludeSources []userstore.WatchHistorySource,
) (string, []any) {
	args := []any{profileID}
	var filters strings.Builder
	if len(includeSources) > 0 {
		placeholders := make([]string, 0, len(includeSources))
		for _, source := range includeSources {
			placeholders = append(placeholders, "?")
			args = append(args, string(source))
		}
		filters.WriteString(" AND h.source IN (" + strings.Join(placeholders, ",") + ")")
	}
	if len(excludeSources) > 0 {
		placeholders := make([]string, 0, len(excludeSources))
		for _, source := range excludeSources {
			placeholders = append(placeholders, "?")
			args = append(args, string(source))
		}
		filters.WriteString(" AND h.source NOT IN (" + strings.Join(placeholders, ",") + ")")
	}
	mediaItemIDs = compactText(mediaItemIDs)
	if len(mediaItemIDs) > 0 {
		placeholders := make([]string, 0, len(mediaItemIDs))
		for _, mediaItemID := range mediaItemIDs {
			placeholders = append(placeholders, "?")
			args = append(args, mediaItemID)
		}
		filters.WriteString(" AND h.media_item_id IN (" + strings.Join(placeholders, ",") + ")")
	}
	return filters.String(), args
}

func RemoveHistoryItems(db *sql.DB, profileID string, mediaItemIDs []string, removedAt time.Time) error {
	mediaItemIDs = compactText(mediaItemIDs)
	if len(mediaItemIDs) == 0 {
		return nil
	}
	if removedAt.IsZero() {
		removedAt = time.Now().UTC()
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin remove history items: %w", err)
	}
	defer tx.Rollback()

	removedAtText := removedAt.UTC().Format(time.RFC3339)
	targetValues := make([]string, len(mediaItemIDs))
	watermarkArgs := make([]any, 0, len(mediaItemIDs)+5)
	for i, mediaItemID := range mediaItemIDs {
		targetValues[i] = "(?)"
		watermarkArgs = append(watermarkArgs, mediaItemID)
	}
	watermarkArgs = append(watermarkArgs, removedAtText, removedAtText, profileID, profileID, removedAtText)
	if _, err := tx.Exec(`
		WITH target(media_item_id) AS (
			VALUES `+strings.Join(targetValues, ",")+`
		),
		watermark AS (
			SELECT
				t.media_item_id,
				CASE
					WHEN MAX(h.watched_at) IS NOT NULL AND MAX(h.watched_at) > ?
					THEN MAX(h.watched_at)
					ELSE ?
				END AS hidden_before
			FROM target t
			LEFT JOIN watch_history h
			  ON h.profile_id = ?
			 AND h.media_item_id = t.media_item_id
			GROUP BY t.media_item_id
		)
		INSERT INTO hidden_history_items (profile_id, media_item_id, hidden_before, updated_at)
		SELECT ?, media_item_id, hidden_before, ?
		FROM watermark
		WHERE true
		ON CONFLICT(profile_id, media_item_id) DO UPDATE SET
			hidden_before = CASE
				WHEN excluded.hidden_before > hidden_history_items.hidden_before
				THEN excluded.hidden_before
				ELSE hidden_history_items.hidden_before
			END,
			updated_at = excluded.updated_at
	`, watermarkArgs...); err != nil {
		return fmt.Errorf("upserting hidden history items: %w", err)
	}

	placeholders := make([]string, len(mediaItemIDs))
	args := make([]any, 0, len(mediaItemIDs)+1)
	args = append(args, profileID)
	for i, mediaItemID := range mediaItemIDs {
		placeholders[i] = "?"
		args = append(args, mediaItemID)
	}
	if _, err := tx.Exec(`
		DELETE FROM watch_history
		WHERE profile_id = ?
		  AND media_item_id IN (`+strings.Join(placeholders, ",")+`)
		  AND watched_at <= (
			SELECT hhi.hidden_before
			FROM hidden_history_items hhi
			WHERE hhi.profile_id = watch_history.profile_id
			  AND hhi.media_item_id = watch_history.media_item_id
		  )
	`, args...); err != nil {
		return fmt.Errorf("deleting removed history rows: %w", err)
	}

	progressArgs := make([]any, 0, len(mediaItemIDs)+1)
	progressArgs = append(progressArgs, profileID)
	progressArgs = append(progressArgs, args[1:1+len(mediaItemIDs)]...)
	if _, err := tx.Exec(`
		DELETE FROM watch_progress
		WHERE profile_id = ?
		  AND media_item_id IN (`+strings.Join(placeholders, ",")+`)
	`, progressArgs...); err != nil {
		return fmt.Errorf("deleting removed progress rows: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit remove history items: %w", err)
	}
	return nil
}

func DeleteHistoryBySource(db *sql.DB, profileID string, mediaItemIDs []string, source userstore.WatchHistorySource) error {
	if len(mediaItemIDs) == 0 {
		return nil
	}
	placeholders := make([]string, len(mediaItemIDs))
	args := make([]any, 0, len(mediaItemIDs)+2)
	args = append(args, profileID, source)
	for i, mediaItemID := range mediaItemIDs {
		placeholders[i] = "?"
		args = append(args, mediaItemID)
	}
	_, err := db.Exec(
		`DELETE FROM watch_history
		WHERE profile_id = ? AND source = ? AND media_item_id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("deleting history by source: %w", err)
	}
	return nil
}

func historyIsHidden(db *sql.DB, profileID, mediaItemID, watchedAt string) (bool, error) {
	var exists bool
	if err := db.QueryRow(`
		SELECT EXISTS(
			SELECT 1
			FROM hidden_history_items
			WHERE profile_id = ?
			  AND media_item_id = ?
			  AND hidden_before >= ?
		)
	`, profileID, mediaItemID, watchedAt).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking hidden history item: %w", err)
	}
	return exists, nil
}

func VisibleHistoryTimestamps(db *sql.DB, profileID string, mediaItemIDs []string, at time.Time) (map[string]string, error) {
	mediaItemIDs = compactText(mediaItemIDs)
	result := make(map[string]string, len(mediaItemIDs))
	if len(mediaItemIDs) == 0 {
		return result, nil
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	targetValues := make([]string, len(mediaItemIDs))
	args := make([]any, 0, len(mediaItemIDs)+1)
	for i, mediaItemID := range mediaItemIDs {
		targetValues[i] = "(?)"
		args = append(args, mediaItemID)
	}
	args = append(args, profileID)
	rows, err := db.Query(`
		WITH target(media_item_id) AS (
			VALUES `+strings.Join(targetValues, ",")+`
		)
		SELECT t.media_item_id, hhi.hidden_before
		FROM target t
		LEFT JOIN hidden_history_items hhi
		  ON hhi.media_item_id = t.media_item_id
		 AND hhi.profile_id = ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("listing visible history timestamps: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var mediaItemID string
		var hiddenBefore sql.NullString
		if err := rows.Scan(&mediaItemID, &hiddenBefore); err != nil {
			return nil, fmt.Errorf("scanning visible history timestamp: %w", err)
		}
		result[mediaItemID] = visibleTimestampAfterHiddenString(at, hiddenBefore)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating visible history timestamps: %w", err)
	}
	return result, nil
}

func visibleTimestampAfterHiddenString(at time.Time, hiddenBefore sql.NullString) string {
	timestamp := at.UTC().Format(time.RFC3339)
	if !hiddenBefore.Valid {
		return timestamp
	}
	hiddenAt, err := time.Parse(time.RFC3339, hiddenBefore.String)
	if err != nil {
		return timestamp
	}
	if at.UTC().After(hiddenAt) {
		return timestamp
	}
	return hiddenAt.UTC().Add(time.Second).Format(time.RFC3339)
}

func compactText(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
