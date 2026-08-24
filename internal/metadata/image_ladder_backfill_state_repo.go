package metadata

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ImageLadderBackfillState is the deployment-wide record of the artwork ladder
// sweep: which version has been proven complete, and when a pass last ran.
type ImageLadderBackfillState struct {
	BackfilledVersion int
	LastAttemptAt     time.Time
}

// ImageLadderBackfillStateRepository reads and writes that record. It is a
// single row; see the migration that creates image_ladder_backfill_state.
type ImageLadderBackfillStateRepository struct {
	pool *pgxpool.Pool
}

func NewImageLadderBackfillStateRepository(pool *pgxpool.Pool) *ImageLadderBackfillStateRepository {
	if pool == nil {
		return nil
	}
	return &ImageLadderBackfillStateRepository{pool: pool}
}

// Get returns the recorded state. A missing row reads as the zero value — no
// ladder backfilled, never attempted — which is the safe answer: the worst case
// is one pass that finds nothing to do.
func (r *ImageLadderBackfillStateRepository) Get(ctx context.Context) (ImageLadderBackfillState, error) {
	if r == nil || r.pool == nil {
		return ImageLadderBackfillState{}, nil
	}
	var (
		state       ImageLadderBackfillState
		lastAttempt *time.Time
	)
	err := r.pool.QueryRow(ctx, `
		SELECT backfilled_version, last_attempt_at
		FROM image_ladder_backfill_state
		WHERE id = 1
	`).Scan(&state.BackfilledVersion, &lastAttempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ImageLadderBackfillState{}, nil
	}
	if err != nil {
		return ImageLadderBackfillState{}, fmt.Errorf("reading image ladder backfill state: %w", err)
	}
	if lastAttempt != nil {
		state.LastAttemptAt = *lastAttempt
	}
	return state, nil
}

// MarkAttempt records that a pass is starting now. It is written before the
// pass rather than after so a crash mid-sweep still paces the next one.
func (r *ImageLadderBackfillStateRepository) MarkAttempt(ctx context.Context) error {
	if r == nil || r.pool == nil {
		return nil
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO image_ladder_backfill_state (id, last_attempt_at, updated_at)
		VALUES (1, NOW(), NOW())
		ON CONFLICT (id) DO UPDATE
		SET last_attempt_at = NOW(),
		    updated_at = NOW()
	`)
	if err != nil {
		return fmt.Errorf("recording image ladder backfill attempt: %w", err)
	}
	return nil
}

// SetBackfilled records that the ladder at this version has been proven fully
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
