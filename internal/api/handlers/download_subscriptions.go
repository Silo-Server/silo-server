package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
)

// subscriptionRequest is the JSON body for POST /downloads/subscriptions.
type subscriptionRequest struct {
	SeriesID        string `json:"series_id"`
	Mode            string `json:"mode"` // all | future | latest_season | specific_seasons
	SeasonNumbers   []int  `json:"season_numbers,omitempty"`
	DeleteWatched   bool   `json:"delete_watched,omitempty"`
	MaxStorageBytes int64  `json:"max_storage_bytes,omitempty"`
}

// patchSubscriptionRequest is the JSON body for PATCH /downloads/subscriptions/{id}.
// Pointer fields distinguish "absent" from a zero value so a partial update only
// touches the fields the client sent.
type patchSubscriptionRequest struct {
	Mode            *string `json:"mode,omitempty"`
	SeasonNumbers   *[]int  `json:"season_numbers,omitempty"`
	DeleteWatched   *bool   `json:"delete_watched,omitempty"`
	MaxStorageBytes *int64  `json:"max_storage_bytes,omitempty"`
	Active          *bool   `json:"active,omitempty"`
}

// subscriptionResponse is one subscription in API responses.
type subscriptionResponse struct {
	ID              string `json:"id"`
	SeriesID        string `json:"series_id"`
	Mode            string `json:"mode"`
	SeasonNumbers   []int  `json:"season_numbers,omitempty"`
	TargetSeason    *int   `json:"target_season,omitempty"`
	DeleteWatched   bool   `json:"delete_watched"`
	MaxStorageBytes int64  `json:"max_storage_bytes"`
	Active          bool   `json:"active"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

type subscriptionsListResponse struct {
	Subscriptions []subscriptionResponse `json:"subscriptions"`
}

// subscriptionResultResponse wraps a created/updated subscription plus the count
// of in-scope episodes registered as managed downloads.
type subscriptionResultResponse struct {
	Subscription subscriptionResponse `json:"subscription"`
	Registered   int                  `json:"registered"`
}

// subscriptionSyncResponse reports how many newly in-scope episodes a sync
// registered across the calling device's monitors.
type subscriptionSyncResponse struct {
	Registered int `json:"registered"`
}

func toSubscriptionResponse(s *downloads.Subscription) subscriptionResponse {
	return subscriptionResponse{
		ID:              s.ID,
		SeriesID:        s.SeriesID,
		Mode:            s.Mode,
		SeasonNumbers:   s.SeasonNumbers,
		TargetSeason:    s.TargetSeason,
		DeleteWatched:   s.DeleteWatched,
		MaxStorageBytes: s.MaxStorageBytes,
		Active:          s.Active,
		CreatedAt:       s.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:       s.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func toSubscriptionResultResponse(result *downloads.SubscriptionResult) subscriptionResultResponse {
	return subscriptionResultResponse{
		Subscription: toSubscriptionResponse(result.Subscription),
		Registered:   result.Registered,
	}
}

// HandleCreateSubscription handles POST /downloads/subscriptions (managed-only).
func (h *DownloadHandler) HandleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	userID, profileID, deviceID, deviceName, devicePlatform, ok := h.requireManaged(w, r)
	if !ok {
		return
	}
	var req subscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.SeriesID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "series_id is required")
		return
	}

	result, err := h.svc.CreateSubscription(r.Context(), userID, downloads.SubscriptionRequest{
		SeriesID:        req.SeriesID,
		Mode:            req.Mode,
		SeasonNumbers:   req.SeasonNumbers,
		DeleteWatched:   req.DeleteWatched,
		MaxStorageBytes: req.MaxStorageBytes,
		ProfileID:       profileID,
		DeviceID:        deviceID,
		DeviceName:      deviceName,
		DevicePlatform:  devicePlatform,
	}, requestAccessFilter(r))
	if err != nil {
		h.writeSubscriptionError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toSubscriptionResultResponse(result))
}

// HandleListSubscriptions handles GET /downloads/subscriptions (managed-only).
func (h *DownloadHandler) HandleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	userID, profileID, deviceID, _, _, ok := h.requireManaged(w, r)
	if !ok {
		return
	}
	subs, err := h.svc.ListSubscriptions(r.Context(), userID, profileID, deviceID)
	if err != nil {
		h.writeSubscriptionError(w, err)
		return
	}
	responses := make([]subscriptionResponse, 0, len(subs))
	for _, s := range subs {
		responses = append(responses, toSubscriptionResponse(s))
	}
	writeJSON(w, http.StatusOK, subscriptionsListResponse{Subscriptions: responses})
}

// HandleGetSubscription handles GET /downloads/subscriptions/{id} (managed-only).
func (h *DownloadHandler) HandleGetSubscription(w http.ResponseWriter, r *http.Request) {
	userID, profileID, deviceID, _, _, ok := h.requireManaged(w, r)
	if !ok {
		return
	}
	sub, err := h.svc.GetSubscription(r.Context(), userID, profileID, deviceID, chi.URLParam(r, "id"))
	if err != nil {
		h.writeSubscriptionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubscriptionResponse(sub))
}

// HandlePatchSubscription handles PATCH /downloads/subscriptions/{id} (managed-only).
func (h *DownloadHandler) HandlePatchSubscription(w http.ResponseWriter, r *http.Request) {
	userID, profileID, deviceID, _, _, ok := h.requireManaged(w, r)
	if !ok {
		return
	}
	var req patchSubscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	result, err := h.svc.UpdateSubscription(r.Context(), userID, profileID, deviceID, chi.URLParam(r, "id"), downloads.SubscriptionPatch{
		Mode:            req.Mode,
		SeasonNumbers:   req.SeasonNumbers,
		DeleteWatched:   req.DeleteWatched,
		MaxStorageBytes: req.MaxStorageBytes,
		Active:          req.Active,
	}, requestAccessFilter(r))
	if err != nil {
		h.writeSubscriptionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubscriptionResultResponse(result))
}

// HandleDeleteSubscription handles DELETE /downloads/subscriptions/{id} (managed-only).
func (h *DownloadHandler) HandleDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	userID, profileID, deviceID, _, _, ok := h.requireManaged(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteSubscription(r.Context(), userID, profileID, deviceID, chi.URLParam(r, "id")); err != nil {
		h.writeSubscriptionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleSyncSubscriptions handles POST /downloads/subscriptions/sync (managed-only):
// the client triggers it on open / background refresh to register newly available
// episodes for its monitors, then pulls the files on its own schedule.
func (h *DownloadHandler) HandleSyncSubscriptions(w http.ResponseWriter, r *http.Request) {
	userID, profileID, deviceID, _, _, ok := h.requireManaged(w, r)
	if !ok {
		return
	}
	registered, err := h.svc.SyncSubscriptions(r.Context(), userID, profileID, deviceID, requestAccessFilter(r))
	if err != nil {
		h.writeSubscriptionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, subscriptionSyncResponse{Registered: registered})
}

// writeSubscriptionError maps series-monitoring errors to HTTP responses.
func (h *DownloadHandler) writeSubscriptionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, downloads.ErrSubscriptionNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Subscription not found")
	case errors.Is(err, downloads.ErrSubscriptionsUnavailable):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Series monitoring is not available")
	case errors.Is(err, downloads.ErrInvalidSubscriptionMode):
		writeError(w, http.StatusBadRequest, "invalid_mode", "Unknown monitor mode")
	case errors.Is(err, downloads.ErrSeasonsRequired):
		writeError(w, http.StatusBadRequest, "seasons_required", "season_numbers is required for specific_seasons mode")
	case errors.Is(err, downloads.ErrInvalidSeasonNumbers):
		writeError(w, http.StatusBadRequest, "invalid_season_numbers", "season_numbers values must be between 0 and 9999")
	case errors.Is(err, downloads.ErrNotSeries):
		writeError(w, http.StatusBadRequest, "not_series", "content_id is not a series")
	case errors.Is(err, downloads.ErrProfileRequired):
		writeError(w, http.StatusBadRequest, "profile_required", "A profile and device are required for series monitoring")
	case errors.Is(err, downloads.ErrFeatureDisabled), errors.Is(err, downloads.ErrDownloadNotAllowed):
		writeError(w, http.StatusForbidden, "forbidden", "You are not allowed to download")
	case errors.Is(err, catalog.ErrItemNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Series not found")
	default:
		slog.Error("download subscription operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to process subscription")
	}
}
