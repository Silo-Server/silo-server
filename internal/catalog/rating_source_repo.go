package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// RatingSourceRepository persists per-source ratings (IMDb, Metacritic,
// Letterboxd, ...) in media_item_rating_sources, one row per item and source.
type RatingSourceRepository struct {
	pool *pgxpool.Pool
}

// NewRatingSourceRepository creates a rating source repository backed by the
// given pool.
func NewRatingSourceRepository(pool *pgxpool.Pool) *RatingSourceRepository {
	return &RatingSourceRepository{pool: pool}
}

// Upsert stores the item's rating sources. With replace false, a source that
// already has a row keeps it (the fill-empty merge); with replace true, the
// incoming row overwrites it (replace-unlocked). Sources absent from the input
// are never removed.
func (r *RatingSourceRepository) Upsert(ctx context.Context, contentID string, sources []models.ItemRatingSource, replace bool) error {
	contentID = strings.TrimSpace(contentID)
	if contentID == "" {
		return fmt.Errorf("content_id is required")
	}
	return upsertRatingSources(ctx, r.pool, contentID, ratingSourceColumnsOf(sources), replace)
}

// Replace makes sources the item's complete set of rating sources: it writes
// each one as Upsert with replace does, and deletes every other source the item
// has. An empty set deletes them all.
func (r *RatingSourceRepository) Replace(ctx context.Context, contentID string, sources []models.ItemRatingSource) error {
	contentID = strings.TrimSpace(contentID)
	if contentID == "" {
		return fmt.Errorf("content_id is required")
	}
	columns := ratingSourceColumnsOf(sources)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin replace rating sources transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		DELETE FROM media_item_rating_sources
		WHERE content_id = $1 AND source <> ALL($2::text[])`,
		contentID, columns.names); err != nil {
		return fmt.Errorf("delete unreported rating sources: %w", err)
	}
	if err := upsertRatingSources(ctx, tx, contentID, columns, true); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ratingSourceColumns holds rating sources as the parallel arrays the upsert
// statement unnests, one entry per source.
type ratingSourceColumns struct {
	names     []string
	scores    []float64
	votes     []*int64
	providers []string
}

// ratingSourceColumnsOf splits sources into columns, keeping the first row for
// a repeated source. names is never nil, so an empty set binds as an empty
// array rather than NULL.
func ratingSourceColumnsOf(sources []models.ItemRatingSource) ratingSourceColumns {
	columns := ratingSourceColumns{
		names:     make([]string, 0, len(sources)),
		scores:    make([]float64, 0, len(sources)),
		votes:     make([]*int64, 0, len(sources)),
		providers: make([]string, 0, len(sources)),
	}
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		// ON CONFLICT cannot touch the same row twice in one statement.
		if _, dup := seen[source.Source]; dup {
			continue
		}
		seen[source.Source] = struct{}{}
		columns.names = append(columns.names, source.Source)
		columns.scores = append(columns.scores, source.Score)
		columns.votes = append(columns.votes, source.Votes)
		columns.providers = append(columns.providers, source.Provider)
	}
	return columns
}

func upsertRatingSources(ctx context.Context, db itemExecer, contentID string, columns ratingSourceColumns, replace bool) error {
	if len(columns.names) == 0 {
		return nil
	}
	conflict := `DO NOTHING`
	if replace {
		// Skip unchanged rows so updated_at records when a rating last changed.
		conflict = `DO UPDATE SET
			score = EXCLUDED.score,
			votes = EXCLUDED.votes,
			provider = EXCLUDED.provider,
			updated_at = now()
		WHERE (media_item_rating_sources.score, media_item_rating_sources.votes, media_item_rating_sources.provider)
			IS DISTINCT FROM (EXCLUDED.score, EXCLUDED.votes, EXCLUDED.provider)`
	}
	_, err := db.Exec(ctx, `
		INSERT INTO media_item_rating_sources (content_id, source, score, votes, provider)
		SELECT $1, s.source, s.score, s.votes, s.provider
		FROM unnest($2::text[], $3::double precision[], $4::bigint[], $5::text[]) AS s(source, score, votes, provider)
		ON CONFLICT (content_id, source) `+conflict,
		contentID, columns.names, columns.scores, columns.votes, columns.providers)
	if err != nil {
		return fmt.Errorf("upsert rating sources: %w", err)
	}
	return nil
}

// GetByContentID returns the item's rating sources in display order.
func (r *RatingSourceRepository) GetByContentID(ctx context.Context, contentID string) ([]models.ItemRatingSource, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, source, score, votes, provider
		FROM media_item_rating_sources
		WHERE content_id = $1`, contentID)
	if err != nil {
		return nil, fmt.Errorf("query rating sources: %w", err)
	}
	defer rows.Close()
	sources, err := scanItemRatingSources(rows)
	if err != nil {
		return nil, err
	}
	sortItemRatingSources(sources)
	return sources, nil
}

// ListByContentIDs returns rating sources for a batch of items, keyed by
// content_id, each in display order. Items without sources are absent.
func (r *RatingSourceRepository) ListByContentIDs(ctx context.Context, contentIDs []string) (map[string][]models.ItemRatingSource, error) {
	if len(contentIDs) == 0 {
		return map[string][]models.ItemRatingSource{}, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, source, score, votes, provider
		FROM media_item_rating_sources
		WHERE content_id = ANY($1)`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("query rating sources batch: %w", err)
	}
	defer rows.Close()
	sources, err := scanItemRatingSources(rows)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]models.ItemRatingSource, len(contentIDs))
	for _, source := range sources {
		result[source.ContentID] = append(result[source.ContentID], source)
	}
	for _, group := range result {
		sortItemRatingSources(group)
	}
	return result, nil
}

func scanItemRatingSources(rows pgx.Rows) ([]models.ItemRatingSource, error) {
	var sources []models.ItemRatingSource
	for rows.Next() {
		var source models.ItemRatingSource
		if err := rows.Scan(&source.ContentID, &source.Source, &source.Score, &source.Votes, &source.Provider); err != nil {
			return nil, fmt.Errorf("scan rating source: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rating sources: %w", err)
	}
	return sources, nil
}

// sortItemRatingSources puts sources in display order. The source name breaks
// ties between names the vocabulary no longer lists, so output is stable.
func sortItemRatingSources(sources []models.ItemRatingSource) {
	slices.SortFunc(sources, func(a, b models.ItemRatingSource) int {
		if rank := models.RatingSourceRank(a.Source) - models.RatingSourceRank(b.Source); rank != 0 {
			return rank
		}
		return strings.Compare(a.Source, b.Source)
	})
}
