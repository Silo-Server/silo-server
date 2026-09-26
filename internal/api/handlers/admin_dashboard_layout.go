package handlers

import (
	"bytes"
	"context"
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

	if _, err := h.writeAdminDashboardLayout(r.Context(), userID, layout, nil); err != nil {
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

	if _, err := h.writeAdminDashboardLayout(r.Context(), userID, nil, nil); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to reset dashboard layout")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AdminDashboardLayoutView is one atomic account layout/revision snapshot.
type AdminDashboardLayoutView struct {
	Layout    json.RawMessage
	UpdatedAt *time.Time
	Revision  string
}

type dashboardLayoutQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readDashboardLayout(ctx context.Context, q dashboardLayoutQuery, userID int) (AdminDashboardLayoutView, error) {
	var view AdminDashboardLayoutView
	err := q.QueryRow(ctx, `SELECT l.layout, l.updated_at, COALESCE(r.revision::text, 'initial')
      FROM users u LEFT JOIN admin_dashboard_layouts l ON l.user_id = u.id
      LEFT JOIN admin_dashboard_layout_revisions r ON r.user_id = u.id WHERE u.id = $1`, userID).Scan(&view.Layout, &view.UpdatedAt, &view.Revision)
	return view, err
}
func (h *AdminHandler) ReadAdminDashboardLayout(ctx context.Context, userID int) (AdminDashboardLayoutView, error) {
	if h == nil || h.pool == nil {
		return AdminDashboardLayoutView{}, &APIError{Status: http.StatusServiceUnavailable, Message: "Dashboard layout storage unavailable"}
	}
	if userID <= 0 {
		return AdminDashboardLayoutView{}, &APIError{Status: http.StatusUnauthorized, Message: "Authentication required"}
	}
	return readDashboardLayout(ctx, h.pool, userID)
}

// Every known bridge/v2 writer locks the account before touching either table.
// This also serializes the absent-row case without global locks or gap locking.
func (h *AdminHandler) writeAdminDashboardLayout(ctx context.Context, userID int, layout json.RawMessage, guard func(AdminDashboardLayoutView) error) (AdminDashboardLayoutView, error) {
	if h == nil || h.pool == nil {
		return AdminDashboardLayoutView{}, &APIError{Status: http.StatusServiceUnavailable, Message: "Dashboard layout storage unavailable"}
	}
	if userID <= 0 {
		return AdminDashboardLayoutView{}, &APIError{Status: http.StatusUnauthorized, Message: "Authentication required"}
	}
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return AdminDashboardLayoutView{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var lockedID int
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id = $1 FOR NO KEY UPDATE`, userID).Scan(&lockedID); err != nil {
		return AdminDashboardLayoutView{}, err
	}
	current, err := readDashboardLayout(ctx, tx, userID)
	if err != nil {
		return AdminDashboardLayoutView{}, err
	}
	if guard != nil {
		if err = guard(current); err != nil {
			return AdminDashboardLayoutView{}, err
		}
	}
	if layout == nil {
		_, err = tx.Exec(ctx, `DELETE FROM admin_dashboard_layouts WHERE user_id = $1`, userID)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO admin_dashboard_layouts (user_id,layout,updated_at) VALUES ($1,$2,clock_timestamp())
          ON CONFLICT (user_id) DO UPDATE SET layout = EXCLUDED.layout, updated_at = clock_timestamp()`, userID, []byte(layout))
	}
	if err != nil {
		return AdminDashboardLayoutView{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO admin_dashboard_layout_revisions (user_id) VALUES ($1)
      ON CONFLICT (user_id) DO UPDATE SET revision = gen_random_uuid()`, userID); err != nil {
		return AdminDashboardLayoutView{}, err
	}
	committed, err := readDashboardLayout(ctx, tx, userID)
	if err != nil {
		return AdminDashboardLayoutView{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AdminDashboardLayoutView{}, err
	}
	return committed, nil
}
func (h *AdminHandler) ResetAdminDashboardLayout(ctx context.Context, userID int) error {
	_, err := h.writeAdminDashboardLayout(ctx, userID, nil, nil)
	return err
}
func (h *AdminHandler) SaveAdminDashboardLayout(ctx context.Context, userID int, layout json.RawMessage, guard func(AdminDashboardLayoutView) error) (AdminDashboardLayoutView, error) {
	if guard == nil {
		return AdminDashboardLayoutView{}, errors.New("dashboard layout precondition required")
	}
	return h.writeAdminDashboardLayout(ctx, userID, layout, guard)
}
