package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ProgressLibraryLookup resolves which progress items belong to a library.
type ProgressLibraryLookup interface {
	GetItemsInFolder(ctx context.Context, contentIDs []string, folderID int) (map[string]bool, error)
}

// ProgressHandler handles watch progress and sync endpoints.
type ProgressHandler struct {
	storeProvider           userstore.UserStoreProvider
	LibraryLookup           ProgressLibraryLookup
	SettingsRepo            PlaybackSettingsReader
	EventsHub               *evt.Hub
	profileStaler           ProfileStaler
	profileRefreshRequester ProfileRefreshRequester
}

// NewProgressHandler creates a new ProgressHandler.
func NewProgressHandler(provider userstore.UserStoreProvider) *ProgressHandler {
	return &ProgressHandler{storeProvider: provider}
}

// SetProfileStaler configures an optional staleness trigger for taste profiles.
func (h *ProgressHandler) SetProfileStaler(ps ProfileStaler) {
	h.profileStaler = ps
}

// SetProfileRefreshRequester configures an optional background refresh queue for taste profiles.
func (h *ProgressHandler) SetProfileRefreshRequester(requester ProfileRefreshRequester) {
	h.profileRefreshRequester = requester
}

// --- Request/Response types ---

type progressEntryResponse struct {
	MediaItemID     string  `json:"media_item_id"`
	PositionSeconds float64 `json:"position_seconds"`
	DurationSeconds float64 `json:"duration_seconds"`
	Completed       bool    `json:"completed"`
	UpdatedAt       string  `json:"updated_at"`
}

type progressListResponse struct {
	Progress []progressEntryResponse `json:"progress"`
}

type syncProgressItem struct {
	MediaItemID    string  `json:"media_item_id"`
	Position       float64 `json:"position"`
	Duration       float64 `json:"duration"`
	ForceOverwrite bool    `json:"force_overwrite"`
}

type syncProgressRequest struct {
	Items []syncProgressItem `json:"items"`
}

type syncProgressResultItem struct {
	MediaItemID string `json:"media_item_id"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}

type syncProgressResponse struct {
	Results []syncProgressResultItem `json:"results"`
}

// --- Handler methods ---

// HandleListProgress handles GET /progress?status=in_progress&limit=20&offset=0.
func (h *ProgressHandler) HandleListProgress(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	status := r.URL.Query().Get("status")
	limit, offset := parsePagination(r)
	libraryID, err := parseLibraryIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library_id")
		return
	}

	entries, err := store.ListProgress(r.Context(), profileID, status, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list progress")
		return
	}
	if libraryID > 0 {
		if h.LibraryLookup == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to apply library filter")
			return
		}
		entries, err = filterProgressEntriesByLibrary(r.Context(), entries, libraryID, h.LibraryLookup)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to apply library filter")
			return
		}
	}

	resp := progressListResponse{
		Progress: make([]progressEntryResponse, 0, len(entries)),
	}
	for _, e := range entries {
		resp.Progress = append(resp.Progress, progressEntryResponse{
			MediaItemID:     e.MediaItemID,
			PositionSeconds: e.PositionSeconds,
			DurationSeconds: e.DurationSeconds,
			Completed:       e.Completed,
			UpdatedAt:       e.UpdatedAt,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

func parseLibraryIDParam(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("library_id")
	if raw == "" {
		return 0, nil
	}

	libraryID, err := strconv.Atoi(raw)
	if err != nil || libraryID <= 0 {
		return 0, strconv.ErrSyntax
	}

	return libraryID, nil
}

func filterProgressEntriesByLibrary(
	ctx context.Context,
	entries []userstore.WatchProgress,
	libraryID int,
	lookup ProgressLibraryLookup,
) ([]userstore.WatchProgress, error) {
	if len(entries) == 0 {
		return entries, nil
	}

	contentIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		contentIDs = append(contentIDs, entry.MediaItemID)
	}

	allowed, err := lookup.GetItemsInFolder(ctx, contentIDs, libraryID)
	if err != nil {
		return nil, err
	}

	filtered := make([]userstore.WatchProgress, 0, len(entries))
	for _, entry := range entries {
		if allowed[entry.MediaItemID] {
			filtered = append(filtered, entry)
		}
	}

	return filtered, nil
}

// HandleSyncProgress handles POST /sync/progress.
// It accepts a batch of progress updates and returns per-item results.
func (h *ProgressHandler) HandleSyncProgress(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	var req syncProgressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if len(req.Items) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "At least one progress item is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	var thresholds userstore.ProgressThresholds
	if h.SettingsRepo != nil {
		if v, _ := h.SettingsRepo.Get(r.Context(), "playback.watched_threshold"); v != "" {
			if pct, err := strconv.Atoi(v); err == nil && pct > 0 {
				thresholds.WatchedPct = pct
			}
		}
		if v, _ := h.SettingsRepo.Get(r.Context(), "playback.min_resume_threshold"); v != "" {
			if pct, err := strconv.Atoi(v); err == nil && pct > 0 {
				thresholds.MinResumePct = pct
			}
		}
	}

	results := make([]syncProgressResultItem, 0, len(req.Items))
	hadSuccessfulUpdate := false

	for _, item := range req.Items {
		result := syncProgressResultItem{
			MediaItemID: item.MediaItemID,
		}

		if item.MediaItemID == "" {
			result.Status = "error"
			result.Error = "media_item_id is required"
			results = append(results, result)
			continue
		}

		var updateErr error
		if item.ForceOverwrite {
			updateErr = store.SetProgress(r.Context(), profileID, item.MediaItemID, item.Position, item.Duration, thresholds)
		} else {
			updateErr = store.UpdateProgress(r.Context(), profileID, item.MediaItemID, item.Position, item.Duration, thresholds)
		}

		if updateErr != nil {
			result.Status = "error"
			result.Error = "failed to update progress"
		} else {
			result.Status = "ok"
			hadSuccessfulUpdate = true
		}

		results = append(results, result)
	}

	if hadSuccessfulUpdate {
		triggerProfileRefresh(r.Context(), h.profileStaler, h.profileRefreshRequester, userID, profileID)
		for _, item := range req.Items {
			if item.MediaItemID == "" {
				continue
			}
			publishUserStateEvent(
				r.Context(),
				h.EventsHub,
				userID,
				profileID,
				item.MediaItemID,
				"",
				"progress",
				userStateEventState{},
			)
		}
	}

	writeJSON(w, http.StatusOK, syncProgressResponse{Results: results})
}
