package pgstore

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5"
)

func scanOnboardingProgress(row pgx.Row, profileID, tourID string) (*userstore.OnboardingProgress, error) {
	out := &userstore.OnboardingProgress{OnboardingState: userstore.OnboardingState{ProfileID: profileID, TourID: tourID}}
	err := row.Scan(&out.LastStep, &out.CompletedAt, &out.SkippedAt, &out.UpdatedAt, &out.Revision)
	return out, err
}

const onboardingReturning = `last_step,COALESCE(completed_at,''),COALESCE(skipped_at,''),updated_at,revision`

func (s *PostgresUserStore) ReadOnboardingProgress(ctx context.Context, profileID, tourID string) (*userstore.OnboardingProgress, error) {
	out, err := scanOnboardingProgress(s.pool.QueryRow(ctx, `SELECT `+onboardingReturning+` FROM user_profile_onboarding WHERE user_id=$1 AND profile_id=$2 AND tour_id=$3`, s.userID, profileID, tourID), profileID, tourID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	return out, err
}
func (s *PostgresUserStore) SaveOnboardingProgress(ctx context.Context, state userstore.OnboardingState, expected int64) (*userstore.OnboardingProgress, error) {
	var row pgx.Row
	if expected == 0 {
		row = s.pool.QueryRow(ctx, `INSERT INTO user_profile_onboarding(user_id,profile_id,tour_id,last_step,completed_at,skipped_at,updated_at) VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7) ON CONFLICT(user_id,profile_id,tour_id) DO NOTHING RETURNING `+onboardingReturning, s.userID, state.ProfileID, state.TourID, state.LastStep, state.CompletedAt, state.SkippedAt, state.UpdatedAt)
	} else {
		row = s.pool.QueryRow(ctx, `UPDATE user_profile_onboarding SET last_step=$4,completed_at=COALESCE(completed_at,NULLIF($5,'')),skipped_at=COALESCE(skipped_at,NULLIF($6,'')),updated_at=$7 WHERE user_id=$1 AND profile_id=$2 AND tour_id=$3 AND revision=$8 RETURNING `+onboardingReturning, s.userID, state.ProfileID, state.TourID, state.LastStep, state.CompletedAt, state.SkippedAt, state.UpdatedAt, expected)
	}
	out, err := scanOnboardingProgress(row, state.ProfileID, state.TourID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, userstore.ErrOnboardingRevision
	}
	return out, err
}
