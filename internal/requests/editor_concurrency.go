package requests

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ErrStaleRevision means the editor's representation was replaced or deleted.
var ErrStaleRevision = errors.New("request editor revision changed")

// ConditionalStore is implemented by the PostgreSQL repository. Negative one
// explicitly overwrites the current row; zero identifies a missing default row.
// Positive revisions identify one persisted generation, including after recreate.
type ConditionalStore interface {
	UpdateSettingsConditional(context.Context, Settings, int64) (Settings, error)
	UpsertUserLimitConditional(context.Context, UserLimit, int64) (*UserLimit, error)
	UpdateIntegrationConditional(context.Context, Integration, int64) (*Integration, error)
	DeleteIntegrationConditional(context.Context, string, int64) error
	UserExists(context.Context, int) (bool, error)
}

func (r *Repository) UserExists(ctx context.Context, id int) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, id).Scan(&exists)
	return exists, err
}

// lockRevision holds the existing row through mutation. A missing default is
// guarded separately by the upsert conflict predicate, since absent rows cannot
// be locked. Legacy writers use the same row locks through ordinary SQL writes.
func lockRevision(ctx context.Context, tx pgx.Tx, query string, args []any, expected int64, allowMissing bool) error {
	var revision int64
	err := tx.QueryRow(ctx, query, args...).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		if allowMissing && (expected == 0 || expected == -1) {
			return nil
		}
		if expected == -1 {
			return ErrNotFound
		}
		return ErrStaleRevision
	}
	if err != nil {
		return err
	}
	if expected != -1 && expected != revision {
		return ErrStaleRevision
	}
	return nil
}

func (r *Repository) UpdateSettingsConditional(ctx context.Context, in Settings, expected int64) (Settings, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Settings{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockRevision(ctx, tx, `SELECT revision FROM request_settings WHERE id=true FOR UPDATE`, nil, expected, true); err != nil {
		return Settings{}, err
	}
	out, err := r.updateSettings(ctx, tx, in, expected)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, ErrStaleRevision
	}
	if err != nil {
		return Settings{}, err
	}
	return out, tx.Commit(ctx)
}
func (r *Repository) UpsertUserLimitConditional(ctx context.Context, in UserLimit, expected int64) (*UserLimit, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var user int
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR KEY SHARE`, in.UserID).Scan(&user); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = lockRevision(ctx, tx, `SELECT revision FROM request_user_limits WHERE user_id=$1 FOR UPDATE`, []any{in.UserID}, expected, true); err != nil {
		return nil, err
	}
	out, err := r.upsertUserLimit(ctx, tx, in, expected)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrStaleRevision
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
func (r *Repository) UpdateIntegrationConditional(ctx context.Context, in Integration, expected int64) (*Integration, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockRevision(ctx, tx, `SELECT revision FROM request_integrations WHERE id=$1 FOR UPDATE`, []any{in.ID}, expected, false); err != nil {
		return nil, err
	}
	out, err := r.updateIntegration(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
func (r *Repository) DeleteIntegrationConditional(ctx context.Context, id string, expected int64) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockRevision(ctx, tx, `SELECT revision FROM request_integrations WHERE id=$1 FOR UPDATE`, []any{id}, expected, false); err != nil {
		return err
	}
	if err = r.deleteIntegration(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) conditionalStore() (ConditionalStore, error) {
	store, ok := s.store.(ConditionalStore)
	if !ok {
		return nil, fmt.Errorf("request store does not support conditional edits")
	}
	return store, nil
}
func (s *Service) UpdateSettingsConditional(ctx context.Context, v Viewer, in Settings, expected int64) (Settings, error) {
	if !v.IsAdmin {
		return Settings{}, ErrForbidden
	}
	if in.GlobalMaxRequests < 0 || in.GlobalWindowDays <= 0 {
		return Settings{}, fmt.Errorf("%w: invalid request settings", ErrInvalidInput)
	}
	store, err := s.conditionalStore()
	if err != nil {
		return Settings{}, err
	}
	return store.UpdateSettingsConditional(ctx, in, expected)
}
func (s *Service) UpsertUserLimitConditional(ctx context.Context, v Viewer, in UserLimit, expected int64) (*UserLimit, error) {
	if !v.IsAdmin {
		return nil, ErrForbidden
	}
	in, err := normalizeUserLimit(in)
	if err != nil {
		return nil, err
	}
	store, err := s.conditionalStore()
	if err != nil {
		return nil, err
	}
	return store.UpsertUserLimitConditional(ctx, in, expected)
}
func (s *Service) GetIntegration(ctx context.Context, v Viewer, id string) (*Integration, error) {
	if !v.IsAdmin {
		return nil, ErrForbidden
	}
	return s.store.GetIntegration(ctx, strings.TrimSpace(id))
}
func (s *Service) UpdateIntegrationConditional(ctx context.Context, v Viewer, in Integration, expected int64) (*Integration, error) {
	if !v.IsAdmin {
		return nil, ErrForbidden
	}
	in.ID = strings.TrimSpace(in.ID)
	if in.ID == "" {
		return nil, fmt.Errorf("%w: integration id required", ErrInvalidInput)
	}
	store, err := s.conditionalStore()
	if err != nil {
		return nil, err
	}
	current, err := s.store.GetIntegration(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if expected != -1 && current.Revision != expected {
		return nil, ErrStaleRevision
	}
	if err = validateInstance(&in); err != nil {
		return nil, err
	}
	if err = s.validateViaPlugin(ctx, in); err != nil {
		return nil, err
	}
	return store.UpdateIntegrationConditional(ctx, in, expected)
}
func (s *Service) DeleteIntegrationConditional(ctx context.Context, v Viewer, id string, expected int64) error {
	if !v.IsAdmin {
		return ErrForbidden
	}
	store, err := s.conditionalStore()
	if err != nil {
		return err
	}
	return store.DeleteIntegrationConditional(ctx, strings.TrimSpace(id), expected)
}
