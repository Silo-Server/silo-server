package catalog

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ItemBackdropRepository stores the backdrops an item shows after its primary
// one (media_items.backdrop_*), in media_item_backdrops.
type ItemBackdropRepository struct {
	pool *pgxpool.Pool
}

func NewItemBackdropRepository(pool *pgxpool.Pool) *ItemBackdropRepository {
	return &ItemBackdropRepository{pool: pool}
}

// ListByContentIDs returns each item's extra backdrops in ordinal order. Items
// without any are absent from the map.
func (r *ItemBackdropRepository) ListByContentIDs(ctx context.Context, contentIDs []string) (map[string][]models.ItemBackdrop, error) {
	if r == nil || r.pool == nil || len(contentIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, id, ordinal, path, source_path, thumbhash
		FROM media_item_backdrops
		WHERE content_id = ANY($1)
		ORDER BY content_id, ordinal`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("listing item backdrops: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]models.ItemBackdrop)
	for rows.Next() {
		var contentID string
		var b models.ItemBackdrop
		if err := rows.Scan(&contentID, &b.ID, &b.Ordinal, &b.Path, &b.SourcePath, &b.Thumbhash); err != nil {
			return nil, fmt.Errorf("scanning item backdrop: %w", err)
		}
		out[contentID] = append(out[contentID], b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing item backdrops: %w", err)
	}
	return out, nil
}

// Replace makes backdrops the item's extra backdrops, numbered from ordinal 1
// in slice order, and returns the stored rows. A row keeps its ID while its
// ordinal survives, so a cache job that targets it stays valid across
// refreshes; rows past the new count are deleted. The cached path and
// thumbhash of a source already stored, at any ordinal, come from its stored
// row: only its cache job and reconcile write those, and the caller's copy may
// predate the job's write. The caller's copy is used for a new source.
func (r *ItemBackdropRepository) Replace(ctx context.Context, contentID string, backdrops []models.ItemBackdrop) ([]models.ItemBackdrop, error) {
	if r == nil || r.pool == nil {
		return nil, nil
	}
	ordinals := make([]int32, len(backdrops))
	paths := make([]string, len(backdrops))
	sources := make([]string, len(backdrops))
	thumbhashes := make([]string, len(backdrops))
	for i, b := range backdrops {
		ordinals[i] = int32(i + 1)
		paths[i] = b.Path
		sources[i] = b.SourcePath
		thumbhashes[i] = b.Thumbhash
	}

	var stored []models.ItemBackdrop
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		// Lock the item's rows first: a cache job writing one of them
		// commits before the upsert reads the stored cached copies, and none
		// can write until this commits.
		if _, err := tx.Exec(ctx, `
			SELECT 1 FROM media_item_backdrops
			WHERE content_id = $1
			ORDER BY id
			FOR UPDATE`, contentID); err != nil {
			return fmt.Errorf("locking item backdrops: %w", err)
		}
		// Upsert before trimming, so a source moving down from a trimmed
		// ordinal still finds its stored row.
		if len(backdrops) > 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO media_item_backdrops (content_id, ordinal, path, source_path, thumbhash)
				SELECT $1, t.ordinal, COALESCE(cur.path, t.path), t.source_path, COALESCE(cur.thumbhash, t.thumbhash)
				FROM unnest($2::int[], $3::text[], $4::text[], $5::text[]) AS t(ordinal, path, source_path, thumbhash)
				LEFT JOIN LATERAL (
					SELECT b.path, b.thumbhash
					FROM media_item_backdrops b
					WHERE b.content_id = $1 AND b.source_path = t.source_path
					ORDER BY b.ordinal
					LIMIT 1
				) cur ON true
				ON CONFLICT (content_id, ordinal) DO UPDATE SET
					path = EXCLUDED.path,
					source_path = EXCLUDED.source_path,
					thumbhash = EXCLUDED.thumbhash
				WHERE media_item_backdrops.source_path IS DISTINCT FROM EXCLUDED.source_path`,
				contentID, ordinals, paths, sources, thumbhashes); err != nil {
				return fmt.Errorf("writing item backdrops: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM media_item_backdrops
			WHERE content_id = $1 AND ordinal > $2`, contentID, len(backdrops)); err != nil {
			return fmt.Errorf("trimming item backdrops: %w", err)
		}
		rows, err := tx.Query(ctx, `
			SELECT id, ordinal, path, source_path, thumbhash
			FROM media_item_backdrops
			WHERE content_id = $1
			ORDER BY ordinal`, contentID)
		if err != nil {
			return fmt.Errorf("reading item backdrops: %w", err)
		}
		stored, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (models.ItemBackdrop, error) {
			var b models.ItemBackdrop
			err := row.Scan(&b.ID, &b.Ordinal, &b.Path, &b.SourcePath, &b.Thumbhash)
			return b, err
		})
		if err != nil {
			return fmt.Errorf("reading item backdrops: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

// UpdateArtworkIfSourceMatches stores the cached copy of one backdrop row, but
// only while the row still points at the source the cache job read.
func (r *ItemBackdropRepository) UpdateArtworkIfSourceMatches(ctx context.Context, id int64, sourcePath, cachedPath, thumbhash string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE media_item_backdrops
		SET path = $3,
			thumbhash = $4
		WHERE id = $1
		  AND source_path = $2`, id, sourcePath, cachedPath, thumbhash)
	if err != nil {
		return false, fmt.Errorf("updating item backdrop cached artwork: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
