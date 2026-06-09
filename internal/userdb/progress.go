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
	if duration > 0 && position > 0 && position/duration < userstore.MinResumeFraction(thresholds.MinResumePct) {
		return nil
	}
	now := nowUTC()
	// Mirrors the Postgres pgstore UpdateProgress: `completed` is a one-way
	// watched latch; position resets to 0 on completion so a rewatch
	// heartbeat on a completed row re-enters Continue Watching through plain
	// MAX while the watched flag survives.
	query := `
		INSERT INTO watch_progress (profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile_id, media_item_id) DO UPDATE SET
			position_seconds = CASE WHEN excluded.completed = 1 THEN 0
				ELSE MAX(excluded.position_seconds, watch_progress.position_seconds) END,
			duration_seconds = excluded.duration_seconds,
			completed = CASE WHEN excluded.completed = 1
				THEN 1 ELSE watch_progress.completed END,
			updated_at = excluded.updated_at
	`
	completed := false
	if duration > 0 && position/duration > userstore.WatchedFraction(thresholds.WatchedPct) {
		completed = true
		position = 0 // match MarkWatched() — completed rows hold no resume point
	}
	_, err := db.Exec(query, profileID, mediaItemID, position, duration, completed, now)
	if err != nil {
		return fmt.Errorf("updating progress: %w", err)
	}
	return nil
}

// SetProgress bypasses the forward-only guard (for rewatches/explicit seek).
// It unconditionally sets the position to the given value. The completed flag
// stays a one-way watched latch: only ClearProgress/ClearProgressBatch (mark
// unwatched) release it.
func SetProgress(db *sql.DB, profileID, mediaItemID string, position, duration float64, thresholds userstore.ProgressThresholds) error {
	now := nowUTC()
	completed := false
	if duration > 0 && position/duration > userstore.WatchedFraction(thresholds.WatchedPct) {
		completed = true
		position = 0 // match MarkWatched() — completed rows hold no resume point
	}
	query := `
		INSERT INTO watch_progress (profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile_id, media_item_id) DO UPDATE SET
			position_seconds = excluded.position_seconds,
			duration_seconds = excluded.duration_seconds,
			completed = watch_progress.completed OR excluded.completed,
			updated_at = excluded.updated_at
	`
	_, err := db.Exec(query, profileID, mediaItemID, position, duration, completed, now)
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
			updated_at = excluded.updated_at
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
	res, err := db.Exec(`
		INSERT INTO watch_progress (profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile_id, media_item_id) DO UPDATE SET
			position_seconds = excluded.position_seconds,
			duration_seconds = excluded.duration_seconds,
			completed = watch_progress.completed OR excluded.completed,
			updated_at = excluded.updated_at
		WHERE excluded.updated_at > watch_progress.updated_at
	`, profileID, mediaItemID, position, duration, completed, updatedAtText)
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
		VALUES (?, ?, 0, ?, 1, ?)
		ON CONFLICT(profile_id, media_item_id) DO UPDATE SET
			position_seconds = 0,
			duration_seconds = excluded.duration_seconds,
			completed = 1,
			updated_at = excluded.updated_at
	`
	_, err := db.Exec(query, profileID, mediaItemID, duration, now)
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
// single transaction. SQLite has no UNNEST, so each row goes through the same
// MarkWatched UPSERT but inside one BEGIN/COMMIT — still much cheaper than
// per-call autocommit.
func MarkProgressBatch(db *sql.DB, profileID string, mediaItemIDs []string, updatedAt time.Time) error {
	mediaItemIDs = compactText(mediaItemIDs)
	if len(mediaItemIDs) == 0 {
		return nil
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin mark progress batch: %w", err)
	}
	defer tx.Rollback()
	updatedAtText := updatedAt.UTC().Format(time.RFC3339)
	for _, mediaItemID := range mediaItemIDs {
		if _, err := tx.Exec(`
			INSERT INTO watch_progress (profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
			VALUES (?, ?, 0, 0, 1, ?)
			ON CONFLICT(profile_id, media_item_id) DO UPDATE SET
				completed = 1,
				position_seconds = 0,
				updated_at = excluded.updated_at
			WHERE watch_progress.completed != 1
			   OR watch_progress.updated_at < excluded.updated_at
		`, profileID, mediaItemID, updatedAtText); err != nil {
			return fmt.Errorf("mark progress batch row: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit mark progress batch: %w", err)
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
	args := []any{query.ProfileID}
	includeSourceFilter := ""
	if len(query.IncludeSources) > 0 {
		placeholders := make([]string, 0, len(query.IncludeSources))
		for _, source := range query.IncludeSources {
			placeholders = append(placeholders, "?")
			args = append(args, string(source))
		}
		includeSourceFilter = " AND h.source IN (" + strings.Join(placeholders, ",") + ")"
	}
	sourceFilter := ""
	if len(query.ExcludeSources) > 0 {
		placeholders := make([]string, 0, len(query.ExcludeSources))
		for _, source := range query.ExcludeSources {
			placeholders = append(placeholders, "?")
			args = append(args, string(source))
		}
		sourceFilter = " AND h.source NOT IN (" + strings.Join(placeholders, ",") + ")"
	}
	mediaFilter := ""
	if len(query.MediaItemIDs) > 0 {
		placeholders := make([]string, 0, len(query.MediaItemIDs))
		for _, mediaItemID := range query.MediaItemIDs {
			placeholders = append(placeholders, "?")
			args = append(args, mediaItemID)
		}
		mediaFilter = " AND h.media_item_id IN (" + strings.Join(placeholders, ",") + ")"
	}
	args = append(args, limit, query.Offset)
	rows, err := db.Query(`
		SELECT h.id, h.profile_id, h.media_item_id, h.watched_at, h.duration_seconds, h.completed, h.source, h.watch_identity
		FROM watch_history h
		WHERE h.profile_id = ?
		  AND h.completed = 1
		`+includeSourceFilter+sourceFilter+mediaFilter+`
		  AND NOT EXISTS (
			SELECT 1
			FROM hidden_history_items hhi
			WHERE hhi.profile_id = h.profile_id
			  AND hhi.media_item_id = h.media_item_id
			  AND h.watched_at <= hhi.hidden_before
		  )
		ORDER BY h.watched_at ASC
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
	for _, mediaItemID := range mediaItemIDs {
		if _, err := tx.Exec(`
			INSERT INTO hidden_history_items (profile_id, media_item_id, hidden_before, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(profile_id, media_item_id) DO UPDATE SET
				hidden_before = CASE
					WHEN excluded.hidden_before > hidden_history_items.hidden_before
					THEN excluded.hidden_before
					ELSE hidden_history_items.hidden_before
				END,
				updated_at = excluded.updated_at
		`, profileID, mediaItemID, removedAtText, removedAtText); err != nil {
			return fmt.Errorf("upserting hidden history item: %w", err)
		}
	}

	placeholders := make([]string, len(mediaItemIDs))
	args := make([]any, 0, len(mediaItemIDs)+2)
	args = append(args, profileID)
	for i, mediaItemID := range mediaItemIDs {
		placeholders[i] = "?"
		args = append(args, mediaItemID)
	}
	args = append(args, removedAtText)
	if _, err := tx.Exec(`
		DELETE FROM watch_history
		WHERE profile_id = ?
		  AND media_item_id IN (`+strings.Join(placeholders, ",")+`)
		  AND watched_at <= ?
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
