package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
)

// maxDashboardLayoutBytes bounds the PUT body. The layout is a short list of
// widget ids and spans; 16 KiB leaves generous headroom while keeping the blob
// small enough that last-write-wins per admin account stays cheap.
const maxDashboardLayoutBytes = 16 << 10

// adminDashboardLayoutResponse is the GET body. Both fields are null when the
// admin has never saved a layout, which the web client reads as "keep the
// local/default layout" rather than as an error.
type adminDashboardLayoutResponse struct {
	Layout    json.RawMessage `json:"layout"`
	UpdatedAt *time.Time      `json:"updated_at"`
}

type adminDashboardLayoutRequest struct {
	Layout json.RawMessage `json:"layout"`
}

// Sentinel validation failures. Their text is the message the client sees, so
// it stays lowercase (staticcheck ST1005) and reads as a sentence fragment.
var (
	errDashboardLayoutInvalidJSON = errors.New("request body must be valid JSON")
	errDashboardLayoutMissing     = errors.New("layout is required")
	errDashboardLayoutNotObject   = errors.New("layout must be a JSON object")
)

// parseDashboardLayoutPayload validates a PUT body and returns the document to
// store. The server treats the layout as opaque past requiring a JSON object:
// widget ids and spans are the web client's vocabulary, and it already
// sanitizes them on load, so validating them here would only add a second
// place to update whenever a widget is added.
func parseDashboardLayoutPayload(body []byte) (json.RawMessage, error) {
	var req adminDashboardLayoutRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, errDashboardLayoutInvalidJSON
	}
	// Unmarshal already checked the syntax of the whole document, so the first
	// non-space byte is enough to tell an object from any other JSON value.
	raw := json.RawMessage(bytes.TrimSpace(req.Layout))
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, errDashboardLayoutMissing
	}
	if raw[0] != '{' {
		return nil, errDashboardLayoutNotObject
	}
	return raw, nil
}

// adminDashboardCapabilitiesResponse advertises the admin dashboard surface a
// server supports. Every field is additive and true on a server that has this
// endpoint at all; they exist so a client can tell "this deployment is older
// than my build" from "this deployment is broken" — a server predating the
// dashboard answers 404 on the aggregates and stores no layout, which is
// otherwise indistinguishable from a transport failure.
//
// Per the v1 rules, new functionality is feature-detected rather than inferred
// from a version. This follows the existing per-subsystem convention
// (/admin/sessions/capabilities, /events/capability).
type adminDashboardCapabilitiesResponse struct {
	// ServerLayouts reports that GET/PUT/DELETE /admin/dashboard/layout store
	// the widget arrangement per admin account server-side.
	ServerLayouts bool `json:"server_layouts"`
	// Timeseries reports that GET /admin/stats/timeseries serves sampled
	// concurrent-stream and egress history.
	Timeseries bool `json:"timeseries"`
	// PlaybackActivity reports that GET /admin/stats/playback-activity serves
	// the rolling playback activity aggregate.
	PlaybackActivity bool `json:"playback_activity"`
	// TopActivity reports that GET /admin/stats/top-activity serves the
	// most-watched-titles and most-active-profiles leaderboards.
	TopActivity bool `json:"top_activity"`
	// Health reports that GET /admin/server/status carries the additive
	// `health` object the dashboard health strip reads.
	Health bool `json:"health"`
	// LogLevelList reports that GET /admin/logs/app accepts a multi-level
	// filter rather than a single level.
	LogLevelList bool `json:"log_level_list"`
	// WatchProviders reports that GET /admin/stats carries the `watch_providers`
	// per-provider breakdown that replaced the Trakt-only
	// `watch_provider_activity` object.
	WatchProviders bool `json:"watch_providers"`
	// DownloadsStats reports that GET /admin/stats/downloads serves the
	// offline-download aggregate and that timeseries points carry the
	// additive `download_egress_kbps` split.
	DownloadsStats bool `json:"downloads_stats"`
}

// HandleGetDashboardCapabilities handles GET /admin/dashboard/capabilities.
func (h *AdminHandler) HandleGetDashboardCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, adminDashboardCapabilitiesResponse{
		ServerLayouts:    true,
		Timeseries:       true,
		PlaybackActivity: true,
		TopActivity:      true,
		Health:           true,
		LogLevelList:     true,
		WatchProviders:   true,
		DownloadsStats:   true,
	})
}

// HandleGetDashboardLayout handles GET /admin/dashboard/layout.
func (h *AdminHandler) HandleGetDashboardLayout(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Database not configured")
		return
	}
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var (
		layout    json.RawMessage
		updatedAt time.Time
	)
	err := h.pool.QueryRow(r.Context(),
		`SELECT layout, updated_at FROM admin_dashboard_layouts WHERE user_id = $1`,
		userID,
	).Scan(&layout, &updatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeJSON(w, http.StatusOK, adminDashboardLayoutResponse{})
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load dashboard layout")
		return
	}

	writeJSON(w, http.StatusOK, adminDashboardLayoutResponse{Layout: layout, UpdatedAt: &updatedAt})
}

// HandlePutDashboardLayout handles PUT /admin/dashboard/layout.
func (h *AdminHandler) HandlePutDashboardLayout(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Database not configured")
		return
	}
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDashboardLayoutBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusBadRequest, "bad_request", "Dashboard layout is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	layout, err := parseDashboardLayoutPayload(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	// Last write wins. The layout is a per-admin blob, so a race between two of
	// the same admin's tabs can only cost the older arrangement; updated_at is
	// returned by GET so a compare-and-set could be layered on later.
	if _, err := h.pool.Exec(r.Context(),
		`INSERT INTO admin_dashboard_layouts (user_id, layout, updated_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (user_id) DO UPDATE SET layout = EXCLUDED.layout, updated_at = now()`,
		userID, []byte(layout),
	); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save dashboard layout")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleDeleteDashboardLayout handles DELETE /admin/dashboard/layout. Deleting
// the row resets the admin to the default layout; it is idempotent.
func (h *AdminHandler) HandleDeleteDashboardLayout(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Database not configured")
		return
	}
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	if _, err := h.pool.Exec(r.Context(),
		`DELETE FROM admin_dashboard_layouts WHERE user_id = $1`, userID,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to reset dashboard layout")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
