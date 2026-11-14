package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// AdminDashboardLayoutView is one atomic account layout/revision snapshot.
type AdminDashboardLayoutView struct {
	Layout    json.RawMessage
	UpdatedAt *time.Time
	Revision  string
}

// rowQuerier reads one row through a pool or a transaction.
type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readDashboardLayout(ctx context.Context, q rowQuerier, userID int) (AdminDashboardLayoutView, error) {
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
