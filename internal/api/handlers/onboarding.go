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
