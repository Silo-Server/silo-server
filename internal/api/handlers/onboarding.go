package handlers

import (
	"github.com/Silo-Server/silo-server/internal/onboarding"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// OnboardingHandler serves the tour manifest and per-profile progress.
type OnboardingHandler struct {
	storeProvider userstore.UserStoreProvider
	gates         onboarding.Gates
}

// NewOnboardingHandler creates a new OnboardingHandler.
func NewOnboardingHandler(provider userstore.UserStoreProvider, gates onboarding.Gates) *OnboardingHandler {
	return &OnboardingHandler{storeProvider: provider, gates: gates}
}

type onboardingStateResponse struct {
	TourID      string `json:"tour_id"`
	LastStep    string `json:"last_step,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	SkippedAt   string `json:"skipped_at,omitempty"`
	// Done is the one bit most clients need: show the tour or not.
	Done bool `json:"done"`
}

type onboardingProgressRequest struct {
	TourID    string `json:"tour_id"`
	LastStep  string `json:"last_step"`
	Completed bool   `json:"completed"`
	Skipped   bool   `json:"skipped"`
}
