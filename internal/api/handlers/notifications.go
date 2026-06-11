package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// profileResolver is the minimal interface NotificationsHandler needs from the
// userstore provider. Defined on the handler side so tests can stub it without
// importing the full provider.
type profileResolver interface {
	ForUser(ctx context.Context, userID int) (userstore.UserStore, error)
}

// NotificationsHandler handles inbox, preferences, and announcement endpoints.
type NotificationsHandler struct {
	svc      *notifications.Service
	profiles profileResolver // nil-tolerant: ChildSafe=false when absent
}

// NewNotificationsHandler creates a new NotificationsHandler.
// provider may be nil; when nil, ChildSafe defaults to false on all requests.
func NewNotificationsHandler(svc *notifications.Service, provider ...profileResolver) *NotificationsHandler {
	h := &NotificationsHandler{svc: svc}
	if len(provider) > 0 {
		h.profiles = provider[0]
	}
	return h
}

// childSafe resolves the active profile and returns Profile.IsChild.
// Fails closed: an unresolvable profile is treated as a child so restricted
// categories are never leaked on errors. Logs at debug level only — never fails
// the request. Nil resolver / missing ids keep returning false (unauthenticated
// paths never reach here; that's the wiring-absent case).
func (h *NotificationsHandler) childSafe(r *http.Request, userID int, profileID string) bool {
	if h.profiles == nil || userID == 0 || profileID == "" {
		return false
	}
	store, err := h.profiles.ForUser(r.Context(), userID)
	if err != nil {
		slog.Debug("notifications: childSafe: ForUser failed", "user_id", userID, "error", err)
		return true // fail closed: treat unknown as child
	}
	p, err := store.GetProfile(r.Context(), profileID)
	if err != nil || p == nil {
		slog.Debug("notifications: childSafe: GetProfile failed", "profile_id", profileID, "error", err)
		return true // fail closed: treat unknown as child
	}
	return p.IsChild
}

// ---------------------------------------------------------------------------
// User-scoped inbox endpoints
// ---------------------------------------------------------------------------

// HandleList handles GET /notifications.
// Query params: unread=1, category, cursor (int64), limit (int, default 50, max 100).
// Response: {"items":[...],"next_cursor":<int64|null>}
func (h *NotificationsHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	q := r.URL.Query()

	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	// keep in sync with the clamp in internal/notifications/store.go List
	if limit > 100 {
		limit = 100
	}

	f := notifications.ListFilter{
		UserID:    userID,
		ProfileID: profileID,
		Limit:     limit,
		ChildSafe: h.childSafe(r, userID, profileID),
	}
	if q.Get("unread") == "1" {
		f.UnreadOnly = true
	}
	if cat := q.Get("category"); cat != "" {
		f.Category = notifications.Category(cat)
	}
	if v := q.Get("cursor"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.Cursor = n
		}
	}

	items, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list notifications")
		return
	}

	var nextCursor *int64
	if len(items) == limit && limit > 0 {
		last := items[len(items)-1].ID
		nextCursor = &last
	}

	writeJSON(w, http.StatusOK, struct {
		Items      []*notifications.Notification `json:"items"`
		NextCursor *int64                        `json:"next_cursor"`
	}{
		Items:      items,
		NextCursor: nextCursor,
	})
}

// HandleUnreadCount handles GET /notifications/unread-count.
// Response: {"count": n}
func (h *NotificationsHandler) HandleUnreadCount(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	count, err := h.svc.UnreadCount(r.Context(), userID, profileID, h.childSafe(r, userID, profileID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get unread count")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Count int `json:"count"`
	}{Count: count})
}

// markReadRequest is the request body for POST /notifications/read.
type markReadRequest struct {
	IDs []int64 `json:"ids"`
	All bool    `json:"all"`
}

// HandleMarkRead handles POST /notifications/read.
// Body: {"ids":[...]} OR {"all":true}. 400 when neither present.
func (h *NotificationsHandler) HandleMarkRead(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	var req markReadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if !req.All && len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Provide ids or all:true")
		return
	}

	if req.All {
		if err := h.svc.MarkAllRead(r.Context(), userID, profileID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to mark all read")
			return
		}
	} else {
		if err := h.svc.MarkRead(r.Context(), userID, profileID, req.IDs); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to mark notifications read")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleDismiss handles POST /notifications/{id}/dismiss.
// 400 for invalid id; 404 on ErrNotFound; 204 on success.
func (h *NotificationsHandler) HandleDismiss(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	rawID := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid notification id")
		return
	}

	if err := h.svc.Dismiss(r.Context(), userID, profileID, id); err != nil {
		if errors.Is(err, notifications.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Notification not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to dismiss notification")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleGetPreferences handles GET /notifications/preferences.
// Response: {"preferences":[{category,enabled},...]}
func (h *NotificationsHandler) HandleGetPreferences(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())

	prefs, err := h.svc.GetPreferences(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get preferences")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Preferences []notifications.Preference `json:"preferences"`
	}{Preferences: prefs})
}

// putPreferencesRequest is the request body for PUT /notifications/preferences.
type putPreferencesRequest struct {
	Preferences []notifications.Preference `json:"preferences"`
}

// HandlePutPreferences handles PUT /notifications/preferences.
// 400 on empty or invalid category; 204 on success.
func (h *NotificationsHandler) HandlePutPreferences(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())

	var req putPreferencesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if len(req.Preferences) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "preferences must not be empty")
		return
	}

	if err := h.svc.SetPreferences(r.Context(), userID, req.Preferences); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Admin announcement endpoints
// ---------------------------------------------------------------------------

// HandleListAnnouncements handles GET /admin/announcements.
// Response: {"items":[...]}
func (h *NotificationsHandler) HandleListAnnouncements(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListAnnouncements(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list announcements")
		return
	}
	if items == nil {
		items = []*notifications.Announcement{}
	}
	writeJSON(w, http.StatusOK, struct {
		Items []*notifications.Announcement `json:"items"`
	}{Items: items})
}

// HandleCreateAnnouncement handles POST /admin/announcements.
// Decodes an Announcement, sets CreatedBy from claims, publishes; 400 on
// validation error; 201 with the announcement on success.
func (h *NotificationsHandler) HandleCreateAnnouncement(w http.ResponseWriter, r *http.Request) {
	claims := apimw.GetClaims(r.Context())

	// Announcements fan out one row per (user, profile); cap the request body so
	// a large title/body cannot be amplified into the whole server's inboxes.
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)

	var a notifications.Announcement
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if claims != nil {
		userID := claims.UserID
		a.CreatedBy = &userID
	}

	if err := h.svc.PublishAnnouncement(r.Context(), &a); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, &a)
}

// HandleDeleteAnnouncement handles DELETE /admin/announcements/{id}.
// 404 on ErrNotFound; 204 on success.
func (h *NotificationsHandler) HandleDeleteAnnouncement(w http.ResponseWriter, r *http.Request) {
	rawID := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid announcement id")
		return
	}

	if err := h.svc.DeleteAnnouncement(r.Context(), id); err != nil {
		if errors.Is(err, notifications.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Announcement not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete announcement")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
