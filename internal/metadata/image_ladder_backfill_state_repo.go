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

// ConfirmBackfilled authoritatively checks the artwork state and records the
// version in one transaction. The singleton row lock coordinates with the
// database trigger installed for rolling upgrades: a late old worker either
// publishes first and is visible to this check, or publishes afterward and
// reopens the version itself.
func (r *ImageLadderBackfillStateRepository) ConfirmBackfilled(ctx context.Context, version int) (bool, error) {
	if r == nil || r.pool == nil {
		return false, nil
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("starting image ladder backfill confirmation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var recordedVersion int
	if err := tx.QueryRow(ctx, `
		SELECT backfilled_version
		FROM image_ladder_backfill_state
		WHERE id = 1
		FOR UPDATE
	`).Scan(&recordedVersion); err != nil {
		return false, fmt.Errorf("locking image ladder backfill state: %w", err)
	}

	var remaining bool
	if err := tx.QueryRow(ctx, `
		WITH all_candidates AS (`+ladderCandidateRowsSQL()+`
		)
		SELECT EXISTS (SELECT 1 FROM all_candidates)
	`).Scan(&remaining); err != nil {
		return false, fmt.Errorf("confirming image ladder backfill remainder: %w", err)
	}
	if remaining {
		if _, err := tx.Exec(ctx, `
			UPDATE image_ladder_backfill_state
			SET last_attempt_at = NULL,
			    updated_at = NOW()
			WHERE id = 1
		`); err != nil {
			return false, fmt.Errorf("reopening image ladder backfill attempt: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("committing image ladder backfill reopen: %w", err)
		}
		return false, nil
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO image_ladder_backfill_state (id, backfilled_version, updated_at)
		VALUES (1, $1, NOW())
		ON CONFLICT (id) DO UPDATE
		SET backfilled_version = GREATEST(image_ladder_backfill_state.backfilled_version, EXCLUDED.backfilled_version),
		    updated_at = NOW()
	`, version)
	if err != nil {
		return false, fmt.Errorf("recording image ladder backfill state: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing image ladder backfill state: %w", err)
	}
	return true, nil
}
