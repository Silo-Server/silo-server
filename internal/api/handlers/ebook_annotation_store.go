package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const ebookAnnotationColumns = `id,user_id,profile_id,content_id,kind,COALESCE(cfi_range,''),COALESCE(location,''),selected_text,note,style,color,metadata,created_at,updated_at`

func (s *PGEbookReaderAnnotationStore) ListPage(ctx context.Context, userID int, profileID, contentID string, after *EbookAnnotationPosition, limit int) ([]EbookReaderAnnotation, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("ebook annotation store is not configured")
	}
	if limit < 1 || limit > 51 {
		return nil, fmt.Errorf("ebook annotation page limit must be 1 to 51")
	}
	var at *time.Time
	id := ""
	if after != nil {
		at = &after.UpdatedAt
		id = after.ID
	}
	rows, err := s.pool.Query(ctx, `SELECT `+ebookAnnotationColumns+` FROM ebook_reader_annotations
 WHERE user_id=$1 AND profile_id=$2 AND content_id=$3 AND ($4::timestamptz IS NULL OR (updated_at,id)<($4,$5))
 ORDER BY updated_at DESC,id DESC LIMIT $6`, userID, profileID, contentID, at, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]EbookReaderAnnotation, 0, limit)
	for rows.Next() {
		row, err := scanEbookReaderAnnotation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, row)
	}
	return items, rows.Err()
}

func insertReaderAnnotation(ctx context.Context, exec func(context.Context, string, ...any) (pgconn.CommandTag, error), annotation EbookReaderAnnotation, replay bool) (bool, error) {
	query := `INSERT INTO ebook_reader_annotations
 (id,user_id,profile_id,content_id,kind,cfi_range,location,selected_text,note,style,color,metadata,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),$8,$9,$10,$11,$12::jsonb,$13,$14)`
	if replay {
		query += ` ON CONFLICT(id) DO NOTHING`
	}
	result, err := exec(ctx, query, annotation.ID, annotation.UserID, annotation.ProfileID, annotation.ContentID, annotation.Kind, annotation.CFIRange, annotation.Location, annotation.SelectedText, annotation.Note, annotation.Style, annotation.Color, annotation.Metadata, annotation.CreatedAt, annotation.UpdatedAt)
	return result.RowsAffected() == 1, err
}

func (s *PGEbookReaderAnnotationStore) CreateOrGet(ctx context.Context, annotation EbookReaderAnnotation) (*EbookReaderAnnotation, bool, error) {
	if s == nil || s.pool == nil {
		return nil, false, fmt.Errorf("ebook annotation store is not configured")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	created, err := insertReaderAnnotation(ctx, tx.Exec, annotation, true)
	if err != nil {
		return nil, false, err
	}
	current, err := scanEbookReaderAnnotation(tx.QueryRow(ctx, `SELECT `+ebookAnnotationColumns+` FROM ebook_reader_annotations
 WHERE id=$1 AND user_id=$2 AND profile_id=$3 AND content_id=$4 FOR UPDATE`, annotation.ID, annotation.UserID, annotation.ProfileID, annotation.ContentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, &APIError{Status: 409, Code: "conflict", Message: "Annotation identity is unavailable"}
	}
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &current, created, nil
}

func (s *PGEbookReaderAnnotationStore) DeleteGuarded(ctx context.Context, userID int, profileID, contentID, id string, guard EbookAnnotationGuard) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("ebook annotation store is not configured")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanEbookReaderAnnotation(tx.QueryRow(ctx, `SELECT `+ebookAnnotationColumns+` FROM ebook_reader_annotations
 WHERE id=$1 AND user_id=$2 AND profile_id=$3 AND content_id=$4 FOR UPDATE`, id, userID, profileID, contentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrEbookAnnotationNotFound
	}
	if err != nil {
		return err
	}
	if err := guard(current); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ebook_reader_annotations WHERE id=$1 AND user_id=$2 AND profile_id=$3 AND content_id=$4`, id, userID, profileID, contentID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
