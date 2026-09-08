package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/onboarding"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (h *OnboardingHandler) ReadOnboardingFlow(ctx context.Context, userID int, profileID, surface string) (onboarding.Flow, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return onboarding.Flow{}, err
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil {
		return onboarding.Flow{}, err
	}
	if profile == nil {
		return onboarding.Flow{}, apiError(http.StatusNotFound, "not_found", "Profile not found")
	}
	return onboarding.FlowFor(ctx, h.gates, surface, profile.IsChild), nil
}
func (h *OnboardingHandler) progressStore(ctx context.Context, userID int) (userstore.OnboardingProgressStore, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	progress, ok := userstore.Capability[userstore.OnboardingProgressStore](store)
	if !ok {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Onboarding progress is unavailable")
	}
	return progress, nil
}
func (h *OnboardingHandler) ReadOnboardingProgress(ctx context.Context, userID int, profileID string) (*userstore.OnboardingProgress, error) {
	store, err := h.progressStore(ctx, userID)
	if err != nil {
		return nil, err
	}
	return store.ReadOnboardingProgress(ctx, profileID, onboarding.TourID)
}
func (h *OnboardingHandler) SaveOnboardingProgress(ctx context.Context, userID int, profileID, lastStep string, completed, skipped bool, expected int64) (*userstore.OnboardingProgress, error) {
	store, err := h.progressStore(ctx, userID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	state := userstore.OnboardingState{ProfileID: profileID, TourID: onboarding.TourID, LastStep: lastStep, UpdatedAt: now}
	if completed {
		state.CompletedAt = now
	}
	if skipped {
		state.SkippedAt = now
	}
	return store.SaveOnboardingProgress(ctx, state, expected)
}
