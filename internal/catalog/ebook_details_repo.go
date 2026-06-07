package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

type EbookDetailsRepository struct {
	pool *pgxpool.Pool
}

func NewEbookDetailsRepository(pool *pgxpool.Pool) *EbookDetailsRepository {
	return &EbookDetailsRepository{pool: pool}
}

func (r *EbookDetailsRepository) Exists(ctx context.Context, contentID string) (bool, error) {
	if contentID == "" {
		return false, fmt.Errorf("ebook details content_id is required")
	}
	if r == nil || r.pool == nil {
		return false, fmt.Errorf("ebook details repository not configured")
	}
	var found int
	err := r.pool.QueryRow(ctx, `
		SELECT 1
		FROM ebook_details
		WHERE content_id = $1
	`, contentID).Scan(&found)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("check ebook details exists: %w", err)
	}
	return true, nil
}

func (r *EbookDetailsRepository) Upsert(ctx context.Context, details models.EbookDetails) error {
	if details.ContentID == "" {
		return fmt.Errorf("ebook details content_id is required")
	}
	if r == nil || r.pool == nil {
		return fmt.Errorf("ebook details repository not configured")
	}
	if len(details.MetadataJSON) == 0 {
		details.MetadataJSON = []byte("{}")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO ebook_details
		    (content_id, format, isbn, publisher, page_count, series_name, series_index, metadata_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (content_id) DO UPDATE SET
		    format = EXCLUDED.format,
		    isbn = EXCLUDED.isbn,
		    publisher = EXCLUDED.publisher,
		    page_count = EXCLUDED.page_count,
		    series_name = EXCLUDED.series_name,
		    series_index = EXCLUDED.series_index,
		    metadata_json = EXCLUDED.metadata_json,
		    updated_at = now()
	`, details.ContentID, details.Format, details.ISBN, details.Publisher,
		details.PageCount, details.SeriesName, details.SeriesIndex, details.MetadataJSON)
	if err != nil {
		return fmt.Errorf("upsert ebook details: %w", err)
	}
	return nil
}
