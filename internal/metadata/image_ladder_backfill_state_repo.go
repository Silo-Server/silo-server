package metadata

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ImageLadderBackfillStateRepository reads and writes the deployment-wide record
// of which artwork variant ladder has been backfilled. It is a single row; see
// the migration that creates image_ladder_backfill_state.
type ImageLadderBackfillStateRepository struct {
	pool *pgxpool.Pool
}

func NewImageLadderBackfillStateRepository(pool *pgxpool.Pool) *ImageLadderBackfillStateRepository {
	if pool == nil {
		return nil
	}
	return &ImageLadderBackfillStateRepository{pool: pool}
}

// Get returns the ladder version this deployment has finished backfilling. A
// missing row reads as 0 — no ladder backfilled — which is the safe answer: the
// worst case is one pass that finds nothing to do.
func (r *ImageLadderBackfillStateRepository) Get(ctx context.Context) (int, error) {
	if r == nil || r.pool == nil {
		return 0, nil
	}
	var version int
	err := r.pool.QueryRow(ctx, `
		SELECT backfilled_version FROM image_ladder_backfill_state WHERE id = 1
	`).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading image ladder backfill state: %w", err)
	}
	return version, nil
}

// SetBackfilled records that the ladder at this version has been fully
// backfilled. It only ever moves the version forward, so a stale worker that
// finishes an older pass late cannot un-record a newer one.
func (r *ImageLadderBackfillStateRepository) SetBackfilled(ctx context.Context, version int) error {
	if r == nil || r.pool == nil {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO image_ladder_backfill_state (id, backfilled_version, updated_at)
		VALUES (1, $1, NOW())
		ON CONFLICT (id) DO UPDATE
		SET backfilled_version = GREATEST(image_ladder_backfill_state.backfilled_version, EXCLUDED.backfilled_version),
		    updated_at = NOW()
	`, version)
	if err != nil {
		return fmt.Errorf("recording image ladder backfill state: %w", err)
	}
	return nil
}
