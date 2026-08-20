package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ABSEbookProgressStore bridges ABS ebook state to the canonical
// ebook_reader_progress table used by Silo's native reader and recommendations.
type ABSEbookProgressStore struct{ Pool *pgxpool.Pool }

var _ abs.EbookProgressStore = (*ABSEbookProgressStore)(nil)

func (s *ABSEbookProgressStore) GetEbookProgress(ctx context.Context, userID, profileID, contentID string) (*abs.EbookProgress, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user id: %w", err)
	}
	p := abs.EbookProgress{UserID: userID, ProfileID: profileID, ContentID: contentID}
	err = s.Pool.QueryRow(ctx, `SELECT file_id, location, progress, updated_at FROM ebook_reader_progress WHERE user_id = $1 AND profile_id = $2 AND content_id = $3`, uid, profileID, contentID).Scan(&p.FileID, &p.Location, &p.Progress, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ebook progress: %w", err)
	}
	return &p, nil
}

func (s *ABSEbookProgressStore) ListEbookProgress(ctx context.Context, userID, profileID string, limit int) ([]abs.EbookProgress, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user id: %w", err)
	}
	rows, err := s.Pool.Query(ctx, `SELECT content_id, file_id, location, progress, updated_at
		FROM ebook_reader_progress
		WHERE user_id = $1 AND profile_id = $2
		  AND NOT EXISTS (
			SELECT 1 FROM user_history_hidden_items hhi
			WHERE hhi.user_id = ebook_reader_progress.user_id
			  AND hhi.profile_id = ebook_reader_progress.profile_id
			  AND hhi.media_item_id = ebook_reader_progress.content_id
			  AND ebook_reader_progress.updated_at <= hhi.hidden_before
		  )
		ORDER BY updated_at DESC LIMIT $3`, uid, profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("list ebook progress: %w", err)
	}
	defer rows.Close()
	out := make([]abs.EbookProgress, 0)
	for rows.Next() {
		p := abs.EbookProgress{UserID: userID, ProfileID: profileID}
		if err := rows.Scan(&p.ContentID, &p.FileID, &p.Location, &p.Progress, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan ebook progress: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ebook progress: %w", err)
	}
	return out, nil
}

// ebookFinishedRegressionGuard is true when a routine write would drag a
// stored *finished* row back below the finished threshold ($7). Such writes are
// stale autosaves from a second device, so every column keeps its stored value.
// An explicit un-finish sets $8 and passes the guard.
const ebookFinishedRegressionGuard = `NOT $8::boolean
			AND ebook_reader_progress.progress >= $7::double precision
			AND COALESCE($6::double precision, ebook_reader_progress.progress) < $7::double precision`

// ebookProgressMergeSet merges a patch into the stored row inside the write
// statement, so no read-modify-write round trip is needed and two devices
// cannot clobber each other's fields. Unset patch columns ($4 file, $5
// location, $6 progress) COALESCE to what is already stored.
//
// $9 carries Audiobookshelf's isFinished:false rule: reset a stored *finished*
// row to 0, leave an unfinished row where it is. An explicit ebookProgress in
// the same body wins, which the caller expresses by leaving $9 false.
const ebookProgressMergeSet = `
			file_id = CASE
				WHEN ` + ebookFinishedRegressionGuard + `
				THEN ebook_reader_progress.file_id
				ELSE COALESCE($4::integer, ebook_reader_progress.file_id)
			END,
			location = CASE
				WHEN ` + ebookFinishedRegressionGuard + `
				THEN ebook_reader_progress.location
				ELSE COALESCE($5::text, ebook_reader_progress.location)
			END,
			progress = CASE
				WHEN ` + ebookFinishedRegressionGuard + `
				THEN ebook_reader_progress.progress
				WHEN $9::boolean AND ebook_reader_progress.progress >= $7::double precision
				THEN 0
				ELSE COALESCE($6::double precision, ebook_reader_progress.progress)
			END,
			updated_at = now()`

// mergeEbookProgressQuery is the hot path: one statement for every write to an
// item the profile has already opened.
const mergeEbookProgressQuery = `UPDATE ebook_reader_progress SET` + ebookProgressMergeSet + `
		WHERE user_id = $1 AND profile_id = $2 AND content_id = $3
		RETURNING file_id, location, progress, updated_at`

// insertEbookProgressQuery creates the first row for an item. ON CONFLICT
// repeats the merge so a device that raced us to the insert is merged into
// rather than overwritten.
const insertEbookProgressQuery = `INSERT INTO ebook_reader_progress
			(user_id, profile_id, content_id, file_id, location, progress, updated_at)
		VALUES ($1, $2, $3, $4::integer, COALESCE($5::text, ''), COALESCE($6::double precision, 0), now())
		ON CONFLICT (user_id, profile_id, content_id) DO UPDATE SET` + ebookProgressMergeSet + `
		RETURNING file_id, location, progress, updated_at`

func (s *ABSEbookProgressStore) UpsertEbookProgress(ctx context.Context, patch abs.EbookProgressPatch) (*abs.EbookProgress, error) {
	uid, err := strconv.Atoi(patch.UserID)
	if err != nil {
		return nil, fmt.Errorf("invalid user id: %w", err)
	}
	args := []any{
		uid, patch.ProfileID, patch.ContentID,
		patch.FileID, patch.Location, patch.Progress,
		models.EbookFinishedProgressThreshold, patch.AllowFinishedRegression, patch.ResetWhenFinished,
	}
	committed := abs.EbookProgress{UserID: patch.UserID, ProfileID: patch.ProfileID, ContentID: patch.ContentID}
	err = s.Pool.QueryRow(ctx, mergeEbookProgressQuery, args...).
		Scan(&committed.FileID, &committed.Location, &committed.Progress, &committed.UpdatedAt)
	if err == nil {
		return &committed, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("merge ebook progress: %w", err)
	}
	// First write for this item. file_id is a NOT NULL reference to
	// media_files, so it cannot be filled in afterwards — the caller resolves
	// the item's ebook file under its own access filter and retries.
	if patch.FileID == nil {
		return nil, abs.ErrEbookProgressFileRequired
	}
	err = s.Pool.QueryRow(ctx, insertEbookProgressQuery, args...).
		Scan(&committed.FileID, &committed.Location, &committed.Progress, &committed.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert ebook progress: %w", err)
	}
	return &committed, nil
}

// hideEbookProgressQuery records the history watermark that hides an item from
// Continue Reading: reading activity at or before hidden_before stops counting.
const hideEbookProgressQuery = `INSERT INTO user_history_hidden_items
			(user_id, profile_id, media_item_id, hidden_before, updated_at)
		VALUES ($1, $2, $3, GREATEST(now(), COALESCE((
			SELECT updated_at FROM ebook_reader_progress
			WHERE user_id = $1 AND profile_id = $2 AND content_id = $3
		), now())), now())
		ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE SET
			hidden_before = GREATEST(user_history_hidden_items.hidden_before, EXCLUDED.hidden_before),
			updated_at = EXCLUDED.updated_at`

// unhideEbookProgressQuery is the exact inverse: dropping the watermark row
// makes the NOT EXISTS filters in Continue Reading and the ABS shelf pass
// again. It deliberately does not touch ebook_reader_progress.updated_at, which
// is those rails' ORDER BY and the lastUpdate reported to clients — restoring a
// book must not reorder the shelf or fake fresh reading activity.
const unhideEbookProgressQuery = `DELETE FROM user_history_hidden_items
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`

func (s *ABSEbookProgressStore) SetEbookHidden(ctx context.Context, userID, profileID, contentID string, hide bool) error {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return fmt.Errorf("invalid user id: %w", err)
	}
	if !hide {
		if _, err = s.Pool.Exec(ctx, unhideEbookProgressQuery, uid, profileID, contentID); err != nil {
			return fmt.Errorf("readd ebook progress: %w", err)
		}
		return nil
	}
	if _, err = s.Pool.Exec(ctx, hideEbookProgressQuery, uid, profileID, contentID); err != nil {
		return fmt.Errorf("hide ebook progress: %w", err)
	}
	return nil
}

func (s *ABSEbookProgressStore) DeleteEbookProgress(ctx context.Context, userID, profileID, contentID string) error {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return fmt.Errorf("invalid user id: %w", err)
	}
	_, err = s.Pool.Exec(ctx, `DELETE FROM ebook_reader_progress WHERE user_id = $1 AND profile_id = $2 AND content_id = $3`, uid, profileID, contentID)
	if err != nil {
		return fmt.Errorf("delete ebook progress: %w", err)
	}
	return nil
}
