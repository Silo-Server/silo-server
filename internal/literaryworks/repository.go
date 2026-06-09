package literaryworks

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrWorkNotFound = errors.New("literary work not found")

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

type CreateWorkParams struct {
	WorkID           string
	CanonicalTitle   string
	SortTitle        string
	NormalizedTitle  string
	PrimaryAuthorKey string
	Description      string
	Publisher        string
	Genres           []string
}

type LinkItemParams struct {
	ContentID  string
	FormatType string
	LinkSource string
	Confidence float64
}

func (r *Repository) CreateWork(ctx context.Context, p CreateWorkParams) (*Work, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("literary works repository requires a database pool")
	}
	if p.Genres == nil {
		p.Genres = []string{}
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO literary_works (
			work_id, canonical_title, sort_title, normalized_title,
			primary_author_key, description, publisher, genres
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (work_id) DO UPDATE SET
			canonical_title = EXCLUDED.canonical_title,
			sort_title = EXCLUDED.sort_title,
			normalized_title = EXCLUDED.normalized_title,
			primary_author_key = EXCLUDED.primary_author_key,
			description = EXCLUDED.description,
			publisher = EXCLUDED.publisher,
			genres = EXCLUDED.genres,
			updated_at = NOW()
		RETURNING work_id, canonical_title, COALESCE(sort_title, ''), normalized_title,
			primary_author_key, COALESCE(primary_cover_content_id, ''),
			COALESCE(description, ''), published_date, COALESCE(publisher, ''),
			genres, created_at, updated_at
	`, p.WorkID, p.CanonicalTitle, p.SortTitle, p.NormalizedTitle, p.PrimaryAuthorKey, p.Description, p.Publisher, p.Genres)
	return scanWork(row)
}

func (r *Repository) GetWork(ctx context.Context, workID string) (*Work, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("literary works repository requires a database pool")
	}
	row := r.pool.QueryRow(ctx, `
		SELECT work_id, canonical_title, COALESCE(sort_title, ''), normalized_title,
			primary_author_key, COALESCE(primary_cover_content_id, ''),
			COALESCE(description, ''), published_date, COALESCE(publisher, ''),
			genres, created_at, updated_at
		FROM literary_works
		WHERE work_id = $1
	`, workID)
	work, err := scanWork(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkNotFound
	}
	return work, err
}

func (r *Repository) LinkItems(ctx context.Context, workID string, items []LinkItemParams) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("literary works repository requires a database pool")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, item := range items {
		if item.Confidence == 0 {
			item.Confidence = 1
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO literary_work_items (work_id, content_id, format_type, link_source, confidence, confirmed_at)
			VALUES ($1,$2,$3,$4,$5, CASE WHEN $4 = 'manual' THEN NOW() ELSE NULL END)
			ON CONFLICT (content_id) DO UPDATE SET
				work_id = EXCLUDED.work_id,
				format_type = EXCLUDED.format_type,
				link_source = EXCLUDED.link_source,
				confidence = EXCLUDED.confidence,
				confirmed_at = EXCLUDED.confirmed_at,
				ignored_at = NULL,
				updated_at = NOW()
		`, workID, item.ContentID, item.FormatType, item.LinkSource, item.Confidence)
		if err != nil {
			return fmt.Errorf("linking %s to work %s: %w", item.ContentID, workID, err)
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) UnlinkItem(ctx context.Context, workID, contentID string) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("literary works repository requires a database pool")
	}
	_, err := r.pool.Exec(ctx, `
		DELETE FROM literary_work_items
		WHERE work_id = $1 AND content_id = $2
	`, workID, contentID)
	return err
}

func (r *Repository) RecordDecision(ctx context.Context, sourceContentID, targetContentID, decision string, userID int) error {
	if r == nil || r.pool == nil {
		return fmt.Errorf("literary works repository requires a database pool")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO literary_work_match_decisions (source_content_id, target_content_id, decision, created_by)
		VALUES ($1, $2, $3, NULLIF($4, 0))
		ON CONFLICT (source_content_id, target_content_id) DO UPDATE SET
			decision = EXCLUDED.decision,
			created_by = EXCLUDED.created_by,
			updated_at = NOW()
	`, sourceContentID, targetContentID, decision, userID)
	return err
}

func (r *Repository) GetSummaryForContentID(ctx context.Context, contentID string) (*WorkSummary, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("literary works repository requires a database pool")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT lw.work_id, lw.canonical_title, lwi.format_type, lwi.content_id,
		       COALESCE(MIN(mil.media_folder_id), 0)::int
		FROM literary_work_items anchor
		JOIN literary_works lw ON lw.work_id = anchor.work_id
		JOIN literary_work_items lwi ON lwi.work_id = lw.work_id
		LEFT JOIN media_item_libraries mil ON mil.content_id = lwi.content_id
		WHERE anchor.content_id = $1
		GROUP BY lw.work_id, lw.canonical_title, lwi.format_type, lwi.content_id
		ORDER BY lwi.format_type, lwi.content_id
	`, contentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var summary *WorkSummary
	for rows.Next() {
		var format WorkFormatSummary
		var workID, title string
		if err := rows.Scan(&workID, &title, &format.Type, &format.ContentID, &format.LibraryID); err != nil {
			return nil, err
		}
		if summary == nil {
			summary = &WorkSummary{WorkID: workID, Title: title}
		}
		summary.Formats = append(summary.Formats, format)
	}
	return summary, rows.Err()
}

func scanWork(row pgx.Row) (*Work, error) {
	var w Work
	if err := row.Scan(
		&w.WorkID,
		&w.CanonicalTitle,
		&w.SortTitle,
		&w.NormalizedTitle,
		&w.PrimaryAuthorKey,
		&w.PrimaryCoverContentID,
		&w.Description,
		&w.PublishedDate,
		&w.Publisher,
		&w.Genres,
		&w.CreatedAt,
		&w.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &w, nil
}
