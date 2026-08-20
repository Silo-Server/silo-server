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

func (s *ABSEbookProgressStore) UpsertEbookProgress(ctx context.Context, p abs.EbookProgress) (*abs.EbookProgress, error) {
	uid, err := strconv.Atoi(p.UserID)
	if err != nil {
		return nil, fmt.Errorf("invalid user id: %w", err)
	}
	const query = `INSERT INTO ebook_reader_progress (user_id, profile_id, content_id, file_id, location, progress, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (user_id, profile_id, content_id) DO UPDATE SET
			file_id = CASE
				WHEN NOT $8 AND ebook_reader_progress.progress >= $7 AND EXCLUDED.progress < $7
				THEN ebook_reader_progress.file_id
				ELSE EXCLUDED.file_id
			END,
			location = CASE
				WHEN NOT $8 AND ebook_reader_progress.progress >= $7 AND EXCLUDED.progress < $7
				THEN ebook_reader_progress.location
				ELSE EXCLUDED.location
			END,
			progress = CASE
				WHEN NOT $8 AND ebook_reader_progress.progress >= $7 AND EXCLUDED.progress < $7
				THEN ebook_reader_progress.progress
				ELSE EXCLUDED.progress
			END,
			updated_at = now()
		RETURNING file_id, location, progress, updated_at`
	committed := p
	err = s.Pool.QueryRow(ctx, query, uid, p.ProfileID, p.ContentID, p.FileID, p.Location, p.Progress, models.EbookFinishedProgressThreshold, p.AllowFinishedRegression).
		Scan(&committed.FileID, &committed.Location, &committed.Progress, &committed.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("upsert ebook progress: %w", err)
	}
	return &committed, nil
}

func (s *ABSEbookProgressStore) SetEbookHidden(ctx context.Context, userID, profileID, contentID string, hide bool) error {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return fmt.Errorf("invalid user id: %w", err)
	}
	if !hide {
		// A new progress timestamp is the canonical reappearance signal: history
		// watermarks remain intact, while activity after hidden_before counts again.
		_, err = s.Pool.Exec(ctx, `UPDATE ebook_reader_progress SET updated_at = now()
			WHERE user_id = $1 AND profile_id = $2 AND content_id = $3`, uid, profileID, contentID)
		if err != nil {
			return fmt.Errorf("readd ebook progress: %w", err)
		}
		return nil
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO user_history_hidden_items
			(user_id, profile_id, media_item_id, hidden_before, updated_at)
		VALUES ($1, $2, $3, GREATEST(now(), COALESCE((
			SELECT updated_at FROM ebook_reader_progress
			WHERE user_id = $1 AND profile_id = $2 AND content_id = $3
		), now())), now())
		ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE SET
			hidden_before = GREATEST(user_history_hidden_items.hidden_before, EXCLUDED.hidden_before),
			updated_at = EXCLUDED.updated_at`, uid, profileID, contentID)
	if err != nil {
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
