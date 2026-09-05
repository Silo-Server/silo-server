package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
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

// OnboardingStateView is a profile's progress through the current tour.
type OnboardingStateView struct {
	TourID      string
	LastStep    string
	CompletedAt string
	SkippedAt   string
	Done        bool
}

// OnboardingProgressInput is one progress report as the transport received it.
type OnboardingProgressInput struct {
	TourID    string
	LastStep  string
	Completed bool
	Skipped   bool
}

// Flow resolves the tour for one surface. v1 GET /onboarding/flow and v2
// getOnboardingFlow both call it; an unknown surface is an *APIError naming
// the member.
func (h *OnboardingHandler) Flow(ctx context.Context, userID int, profileID, surface string) (onboarding.Flow, error) {
	surface = strings.ToLower(strings.TrimSpace(surface))
	switch surface {
	case onboarding.SurfaceWeb, onboarding.SurfacePhone, onboarding.SurfaceTV:
	case "":
		surface = onboarding.SurfaceWeb
	default:
		return onboarding.Flow{}, &APIError{Status: http.StatusBadRequest, Code: policyErrorBadRequest, Message: "surface must be web, phone, or tv", Field: "surface"}
	}

	isChild := false
	if store, err := h.storeProvider.ForUser(ctx, userID); err == nil {
		if profile, err := store.GetProfile(ctx, profileID); err == nil && profile != nil {
			isChild = profile.IsChild
		}
	}
	return onboarding.FlowFor(ctx, h.gates, surface, isChild), nil
}

// State reads a profile's progress. v1 GET /onboarding/state and v2
// getOnboardingState both call it.
func (h *OnboardingHandler) State(ctx context.Context, userID int, profileID string) (OnboardingStateView, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return OnboardingStateView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	state, err := store.GetOnboardingState(ctx, profileID, onboarding.TourID)
	if err != nil {
		return OnboardingStateView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to read onboarding state")
	}
	view := OnboardingStateView{TourID: onboarding.TourID}
	if state != nil {
		view.LastStep = state.LastStep
		view.CompletedAt = state.CompletedAt
		view.SkippedAt = state.SkippedAt
		view.Done = state.CompletedAt != "" || state.SkippedAt != ""
	}
	return view, nil
}

// RecordProgress writes a profile's progress through the current tour. v1
// POST /onboarding/progress and v2 recordOnboardingProgress both call it. A
// tour_id other than the current tour is a 409 conflict, so a stale client
// cannot corrupt a future tour's state.
func (h *OnboardingHandler) RecordProgress(ctx context.Context, userID int, profileID string, in OnboardingProgressInput) error {
	if in.TourID != "" && in.TourID != onboarding.TourID {
		return apiError(http.StatusConflict, "tour_mismatch", "This tour is no longer current")
	}
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	state := userstore.OnboardingState{
		ProfileID: profileID,
		TourID:    onboarding.TourID,
		LastStep:  in.LastStep,
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if in.Completed {
		state.CompletedAt = now
	}
	if in.Skipped {
		state.SkippedAt = now
	}
	if err := store.UpsertOnboardingState(ctx, state); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to save onboarding progress")
	}
	return nil
}

// HandleGetFlow handles GET /onboarding/flow?surface=web|phone|tv.
func (h *OnboardingHandler) HandleGetFlow(w http.ResponseWriter, r *http.Request) {
	profileID, ok := activeProfileIDFromRequest(w, r)
	if !ok {
		return
	}
	flow, err := h.Flow(r.Context(), apimw.GetUserID(r.Context()), profileID, r.URL.Query().Get("surface"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, flow)
}

// HandleGetState handles GET /onboarding/state.
func (h *OnboardingHandler) HandleGetState(w http.ResponseWriter, r *http.Request) {
	profileID, ok := activeProfileIDFromRequest(w, r)
	if !ok {
		return
	}
	state, err := h.State(r.Context(), apimw.GetUserID(r.Context()), profileID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, onboardingStateResponse(state))
}

// HandlePostProgress handles POST /onboarding/progress.
func (h *OnboardingHandler) HandlePostProgress(w http.ResponseWriter, r *http.Request) {
	profileID, ok := activeProfileIDFromRequest(w, r)
	if !ok {
		return
	}
	var req onboardingProgressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if err := h.RecordProgress(r.Context(), apimw.GetUserID(r.Context()), profileID, OnboardingProgressInput(req)); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
