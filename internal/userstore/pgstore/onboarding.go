package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (s *PostgresUserStore) GetOnboardingState(ctx context.Context, profileID, tourID string) (*userstore.OnboardingState, error) {
	var state userstore.OnboardingState
	var completedAt, skippedAt sql.NullString
	err := s.pool.QueryRow(ctx,
		`SELECT profile_id, tour_id, last_step, completed_at, skipped_at, updated_at
		 FROM user_profile_onboarding
		 WHERE user_id = $1 AND profile_id = $2 AND tour_id = $3`,
		s.userID, profileID, tourID,
	).Scan(&state.ProfileID, &state.TourID, &state.LastStep, &completedAt, &skippedAt, &state.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting onboarding state for profile %q tour %q: %w", profileID, tourID, err)
	}
	state.CompletedAt = completedAt.String
	state.SkippedAt = skippedAt.String
	return &state, nil
}

func (s *PostgresUserStore) UpsertOnboardingState(ctx context.Context, state userstore.OnboardingState) error {
	if state.UpdatedAt == "" {
		state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	// Completed/skipped are monotonic: a later progress write never clears
	// them (same rule as the SQLite backend).
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_profile_onboarding (user_id, profile_id, tour_id, last_step, completed_at, skipped_at, updated_at)
		 VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7)
		 ON CONFLICT(user_id, profile_id, tour_id) DO UPDATE SET
			last_step = excluded.last_step,
			completed_at = COALESCE(user_profile_onboarding.completed_at, excluded.completed_at),
			skipped_at = COALESCE(user_profile_onboarding.skipped_at, excluded.skipped_at),
			updated_at = excluded.updated_at`,
		s.userID, state.ProfileID, state.TourID, state.LastStep, state.CompletedAt, state.SkippedAt, state.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upserting onboarding state for profile %q tour %q: %w", state.ProfileID, state.TourID, err)
	}
	return nil
}
