package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
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
