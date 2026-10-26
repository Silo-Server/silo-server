package requests

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
)

// Who may request, and on what terms, resolves in layers: the account's own
// limits, then its access group's, then the server-wide settings. Blocking is
// not a limit: an account is blocked by the requests switch on the account or
// its access group (access.EffectiveUserPolicy.RequestsAllowed), or by
// requests being off server-wide. The older "blocked" limit and approval
// modes on an account are still honored when written, but no editor offers
// them.

// GroupLimit is an access group's request approval and quota.
type GroupLimit struct {
	Revision     int64
	GroupID      int64
	LimitMode    LimitMode
	MaxRequests  *int
	WindowDays   *int
	ApprovalMode ApprovalMode
	UpdatedAt    time.Time
}

// GroupLimitStore reads and writes access groups' request limits. The
// PostgreSQL repository implements it.
type GroupLimitStore interface {
	GetGroupLimit(ctx context.Context, groupID int64) (*GroupLimit, error)
	UpsertGroupLimitConditional(ctx context.Context, in GroupLimit, expected int64) (*GroupLimit, error)
	GroupExists(ctx context.Context, groupID int64) (bool, error)
}

func (r *Repository) GroupExists(ctx context.Context, groupID int64) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access_groups WHERE id=$1)`, groupID).Scan(&exists)
	return exists, err
}

func (r *Repository) GetGroupLimit(ctx context.Context, groupID int64) (*GroupLimit, error) {
	row, err := scanGroupLimit(r.pool.QueryRow(ctx, `
		SELECT group_id, limit_mode, max_requests, window_days, approval_mode, updated_at, revision
		FROM request_group_limits WHERE group_id = $1`, groupID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get request group limit: %w", err)
	}
	return row, nil
}

// UpsertGroupLimitConditional saves a group's limits when its revision still
// matches expected (zero for a group with no saved limits, -1 to overwrite).
func (r *Repository) UpsertGroupLimitConditional(ctx context.Context, in GroupLimit, expected int64) (*GroupLimit, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var group int64
	if err = tx.QueryRow(ctx, `SELECT id FROM access_groups WHERE id=$1 FOR KEY SHARE`, in.GroupID).Scan(&group); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = lockRevision(ctx, tx, `SELECT revision FROM request_group_limits WHERE group_id=$1 FOR UPDATE`, []any{in.GroupID}, expected, true); err != nil {
		return nil, err
	}
	out, err := scanGroupLimit(tx.QueryRow(ctx, `
		INSERT INTO request_group_limits (group_id, limit_mode, max_requests, window_days, approval_mode, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (group_id) DO UPDATE SET
			limit_mode = EXCLUDED.limit_mode,
			max_requests = EXCLUDED.max_requests,
			window_days = EXCLUDED.window_days,
			approval_mode = EXCLUDED.approval_mode,
			updated_at = now()
		WHERE $6::bigint = -1 OR request_group_limits.revision = $6
		RETURNING group_id, limit_mode, max_requests, window_days, approval_mode, updated_at, revision`,
		in.GroupID, in.LimitMode, in.MaxRequests, in.WindowDays, in.ApprovalMode, expected))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrStaleRevision
	}
	if err != nil {
		return nil, fmt.Errorf("upsert request group limit: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func scanGroupLimit(row pgx.Row) (*GroupLimit, error) {
	var out GroupLimit
	var max, window sql.NullInt64
	if err := row.Scan(&out.GroupID, &out.LimitMode, &max, &window, &out.ApprovalMode, &out.UpdatedAt, &out.Revision); err != nil {
		return nil, err
	}
	if max.Valid {
		v := int(max.Int64)
		out.MaxRequests = &v
	}
	if window.Valid {
		v := int(window.Int64)
		out.WindowDays = &v
	}
	return &out, nil
}

func (s *Service) groupLimitStore() (GroupLimitStore, error) {
	store, ok := s.store.(GroupLimitStore)
	if !ok {
		return nil, fmt.Errorf("request store does not support group limits")
	}
	return store, nil
}

// GetGroupLimit returns an access group's request limits; a group with none
// saved inherits everything (revision zero).
func (s *Service) GetGroupLimit(ctx context.Context, v Viewer, groupID int64) (*GroupLimit, error) {
	if !v.IsAdmin {
		return nil, ErrForbidden
	}
	store, err := s.groupLimitStore()
	if err != nil {
		return nil, err
	}
	exists, err := store.GroupExists(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	limit, err := store.GetGroupLimit(ctx, groupID)
	if err != nil || limit != nil {
		return limit, err
	}
	return &GroupLimit{GroupID: groupID, LimitMode: LimitModeInherit, ApprovalMode: ApprovalModeInherit}, nil
}

// UpsertGroupLimitConditional saves an access group's request limits.
func (s *Service) UpsertGroupLimitConditional(ctx context.Context, v Viewer, in GroupLimit, expected int64) (*GroupLimit, error) {
	if !v.IsAdmin {
		return nil, ErrForbidden
	}
	in, err := normalizeGroupLimit(in)
	if err != nil {
		return nil, err
	}
	store, err := s.groupLimitStore()
	if err != nil {
		return nil, err
	}
	return store.UpsertGroupLimitConditional(ctx, in, expected)
}

func normalizeGroupLimit(in GroupLimit) (GroupLimit, error) {
	if in.GroupID <= 0 {
		return GroupLimit{}, fmt.Errorf("%w: invalid access group id", ErrInvalidInput)
	}
	switch in.LimitMode {
	case "", LimitModeInherit, LimitModeUnlimited:
		if in.LimitMode == "" {
			in.LimitMode = LimitModeInherit
		}
		in.MaxRequests, in.WindowDays = nil, nil
	case LimitModeCustom:
		if in.MaxRequests == nil || in.WindowDays == nil || *in.MaxRequests < 0 || *in.WindowDays <= 0 {
			return GroupLimit{}, &ValidationError{FieldErrors: map[string]string{
				"max_requests": "A custom limit needs a number of requests (0 or more) and a window of at least one day.",
			}}
		}
	default:
		return GroupLimit{}, fmt.Errorf("%w: invalid limit mode", ErrInvalidInput)
	}
	switch in.ApprovalMode {
	case "":
		in.ApprovalMode = ApprovalModeInherit
	case ApprovalModeInherit, ApprovalModeManual, ApprovalModeAuto:
	default:
		return GroupLimit{}, fmt.Errorf("%w: invalid approval mode", ErrInvalidInput)
	}
	return in, nil
}

// requestAccess is what the account and its access group say about
// requesting: whether the account may request at all, and the group's
// limits when the group applies.
type requestAccess struct {
	allowed bool
	group   *GroupLimit
}

// viewerRequestAccess resolves an account's requests switch and its access
// group's limits. Without a user repository (some tests) it resolves to an
// allowed account with no group; creating a request still enforces the
// switch through ensureViewerRequestsAllowed, which requires one.
func (s *Service) viewerRequestAccess(ctx context.Context, userID int) (requestAccess, error) {
	if s.users == nil {
		return requestAccess{allowed: true}, nil
	}
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return requestAccess{}, err
	}
	if user == nil {
		return requestAccess{}, nil
	}
	effective, err := access.EffectivePolicyForUser(ctx, user, s.groupProvider)
	if err != nil {
		return requestAccess{}, err
	}
	out := requestAccess{allowed: effective.RequestsAllowed}
	if access.GroupApplies(user) {
		if store, ok := s.store.(GroupLimitStore); ok {
			if out.group, err = store.GetGroupLimit(ctx, *user.AccessGroupID); err != nil {
				return requestAccess{}, err
			}
		}
	}
	return out, nil
}
