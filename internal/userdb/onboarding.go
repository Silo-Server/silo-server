package userdb

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// GetOnboardingState returns one profile's state for one tour, or nil when
// the profile has never touched that tour.
func GetOnboardingState(db *sql.DB, profileID, tourID string) (*userstore.OnboardingState, error) {
	var state userstore.OnboardingState
	var completedAt, skippedAt sql.NullString
	err := db.QueryRow(
		`SELECT profile_id, tour_id, last_step, completed_at, skipped_at, updated_at
		 FROM profile_onboarding WHERE profile_id = ? AND tour_id = ?`,
		profileID, tourID,
	).Scan(&state.ProfileID, &state.TourID, &state.LastStep, &completedAt, &skippedAt, &state.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting onboarding state for profile %q tour %q: %w", profileID, tourID, err)
	}
	state.CompletedAt = completedAt.String
	state.SkippedAt = skippedAt.String
	return &state, nil
}

// UpsertOnboardingState records tour progress. Completed/skipped timestamps
// are monotonic: once set they are never cleared by a later progress write,
// so replaying the tour can't un-complete it for other devices mid-run.
func UpsertOnboardingState(db *sql.DB, state userstore.OnboardingState) error {
	if state.UpdatedAt == "" {
		state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := db.Exec(
		`INSERT INTO profile_onboarding (profile_id, tour_id, last_step, completed_at, skipped_at, updated_at)
		 VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)
		 ON CONFLICT(profile_id, tour_id) DO UPDATE SET
			last_step = excluded.last_step,
			completed_at = COALESCE(profile_onboarding.completed_at, excluded.completed_at),
			skipped_at = COALESCE(profile_onboarding.skipped_at, excluded.skipped_at),
			updated_at = excluded.updated_at`,
		state.ProfileID, state.TourID, state.LastStep, state.CompletedAt, state.SkippedAt, state.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upserting onboarding state for profile %q tour %q: %w", state.ProfileID, state.TourID, err)
	}
	return nil
}
