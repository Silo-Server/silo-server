package handlers

import (
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

// HandleGetFlow handles GET /onboarding/flow?surface=web|phone|tv.
func (h *OnboardingHandler) HandleGetFlow(w http.ResponseWriter, r *http.Request) {
	profileID, ok := activeProfileIDFromRequest(w, r)
	if !ok {
		return
	}
	userID := apimw.GetUserID(r.Context())

	surface := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("surface")))
	switch surface {
	case onboarding.SurfaceWeb, onboarding.SurfacePhone, onboarding.SurfaceTV:
	case "":
		surface = onboarding.SurfaceWeb
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "surface must be web, phone, or tv")
		return
	}

	isChild := false
	if store, err := h.storeProvider.ForUser(r.Context(), userID); err == nil {
		if profile, err := store.GetProfile(r.Context(), profileID); err == nil && profile != nil {
			isChild = profile.IsChild
		}
	}

	writeJSON(w, http.StatusOK, onboarding.FlowFor(r.Context(), h.gates, surface, isChild))
}

// HandleGetState handles GET /onboarding/state.
func (h *OnboardingHandler) HandleGetState(w http.ResponseWriter, r *http.Request) {
	profileID, ok := activeProfileIDFromRequest(w, r)
	if !ok {
		return
	}
	userID := apimw.GetUserID(r.Context())

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	state, err := store.GetOnboardingState(r.Context(), profileID, onboarding.TourID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read onboarding state")
		return
	}

	resp := onboardingStateResponse{TourID: onboarding.TourID}
	if state != nil {
		resp.LastStep = state.LastStep
		resp.CompletedAt = state.CompletedAt
		resp.SkippedAt = state.SkippedAt
		resp.Done = state.CompletedAt != "" || state.SkippedAt != ""
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandlePostProgress handles POST /onboarding/progress.
func (h *OnboardingHandler) HandlePostProgress(w http.ResponseWriter, r *http.Request) {
	profileID, ok := activeProfileIDFromRequest(w, r)
	if !ok {
		return
	}
	userID := apimw.GetUserID(r.Context())

	var req onboardingProgressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	// tour_id is accepted for forward compatibility but only the current
	// tour is writable — a stale client can't corrupt a future tour's state.
	if req.TourID != "" && req.TourID != onboarding.TourID {
		writeError(w, http.StatusConflict, "tour_mismatch", "This tour is no longer current")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	state := userstore.OnboardingState{
		ProfileID: profileID,
		TourID:    onboarding.TourID,
		LastStep:  req.LastStep,
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if req.Completed {
		state.CompletedAt = now
	}
	if req.Skipped {
		state.SkippedAt = now
	}
	if err := store.UpsertOnboardingState(r.Context(), state); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save onboarding progress")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
