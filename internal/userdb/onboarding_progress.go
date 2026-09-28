package userdb

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

const onboardingRevisionSchema = `
CREATE TRIGGER IF NOT EXISTS onboarding_revision AFTER UPDATE OF last_step,completed_at,skipped_at,updated_at ON profile_onboarding
BEGIN
 UPDATE profile_onboarding SET revision=OLD.revision+1 WHERE profile_id=NEW.profile_id AND tour_id=NEW.tour_id;
END;
`

func readOnboardingProgress(row *sql.Row, profileID, tourID string) (*userstore.OnboardingProgress, error) {
	out := &userstore.OnboardingProgress{OnboardingState: userstore.OnboardingState{ProfileID: profileID, TourID: tourID}}
	err := row.Scan(&out.LastStep, &out.CompletedAt, &out.SkippedAt, &out.UpdatedAt, &out.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	return out, err
}

const onboardingProgressSelect = `SELECT last_step,COALESCE(completed_at,''),COALESCE(skipped_at,''),updated_at,revision FROM profile_onboarding WHERE profile_id=? AND tour_id=?`

func (s *SQLiteUserStore) ReadOnboardingProgress(ctx context.Context, profileID, tourID string) (*userstore.OnboardingProgress, error) {
	return readOnboardingProgress(s.db.QueryRowContext(ctx, onboardingProgressSelect, profileID, tourID), profileID, tourID)
}
func (s *SQLiteUserStore) SaveOnboardingProgress(ctx context.Context, state userstore.OnboardingState, expected int64) (*userstore.OnboardingProgress, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var result sql.Result
	if expected == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO profile_onboarding(profile_id,tour_id,last_step,completed_at,skipped_at,updated_at) VALUES(?,?,?,NULLIF(?,''),NULLIF(?,''),?) ON CONFLICT(profile_id,tour_id) DO NOTHING`, state.ProfileID, state.TourID, state.LastStep, state.CompletedAt, state.SkippedAt, state.UpdatedAt)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE profile_onboarding SET last_step=?,completed_at=COALESCE(completed_at,NULLIF(?,'')),skipped_at=COALESCE(skipped_at,NULLIF(?,'')),updated_at=? WHERE profile_id=? AND tour_id=? AND revision=?`, state.LastStep, state.CompletedAt, state.SkippedAt, state.UpdatedAt, state.ProfileID, state.TourID, expected)
	}
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, userstore.ErrOnboardingRevision
	}
	out, err := readOnboardingProgress(tx.QueryRowContext(ctx, onboardingProgressSelect, state.ProfileID, state.TourID), state.ProfileID, state.TourID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
