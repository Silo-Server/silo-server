package auth

import (
	"context"
	"errors"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
)

const apiKeyRateTierStandard = "standard"
const apiKeyRateTierElevated = "elevated"

var ErrAPIKeyRevisionConflict = errors.New("API key configuration changed")
var ErrInvalidAPIKeyTier = errors.New("invalid API key rate tier")
var ErrAPIKeyPreconditionInvalid = errors.New("invalid API key revision precondition")

// APIKeyPrecondition distinguishes an explicit wildcard from an exact revision.
// Missing, negative, or mixed wildcard/exact input is never an unconditional write.
type APIKeyPrecondition struct {
	Revision int64
	Any      bool
}

func (p APIKeyPrecondition) validate() error {
	if p.Any && p.Revision == 0 || !p.Any && p.Revision > 0 {
		return nil
	}
	return ErrAPIKeyPreconditionInvalid
}

type APIKeyRevisionConflict struct{ Current *models.APIKeyMetadata }

func (*APIKeyRevisionConflict) Error() string { return ErrAPIKeyRevisionConflict.Error() }
func (*APIKeyRevisionConflict) Unwrap() error { return ErrAPIKeyRevisionConflict }

type APIKeyPageKey struct {
	CreatedAt time.Time
	ID        int64
}

// Prefixes never contain the whole credential, even for malformed legacy rows.
const apiKeyMetadataColumns = `ak.id,ak.user_id,ak.label,CASE WHEN ak.api_key ~ '^sa_[0-9a-f]{64}$' THEN left(ak.api_key,11) ELSE '' END,ak.rate_tier,COALESCE(ak.scopes,'{}'::text[]),ak.created_at,ak.revision`

func apiKeyMetadataFields(k *models.APIKeyMetadata) []any {
	return []any{&k.ID, &k.UserID, &k.Label, &k.KeyPrefix, &k.RateTier, &k.Scopes, &k.CreatedAt, &k.Revision}
}
func scanAPIKeyMetadata(row interface{ Scan(...any) error }) (*models.APIKeyMetadata, error) {
	k := new(models.APIKeyMetadata)
	if err := row.Scan(apiKeyMetadataFields(k)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAPIKeyNotFound
		}
		return nil, err
	}
	return k, nil
}

func (r *APIKeyRepository) GetMetadataByID(ctx context.Context, id int64) (*models.APIKeyMetadata, error) {
	return scanAPIKeyMetadata(r.pool.QueryRow(ctx, `SELECT `+apiKeyMetadataColumns+` FROM api_keys ak WHERE ak.id=$1`, id))
}

// ListByUser omits the full key in SQL as well as in its return type.
func (r *APIKeyRepository) ListByUser(ctx context.Context, userID int) ([]*models.APIKeyMetadataWithUsage, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+apiKeyMetadataColumns+`,ak.last_used_at FROM api_keys ak WHERE ak.user_id=$1 ORDER BY ak.created_at DESC,ak.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []*models.APIKeyMetadataWithUsage{}
	for rows.Next() {
		k := new(models.APIKeyMetadataWithUsage)
		if err = rows.Scan(append(apiKeyMetadataFields(&k.APIKeyMetadata), &k.LastUsedAt)...); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}
func (r *APIKeyRepository) ListByUserAdmin(ctx context.Context, userID int) ([]*models.APIKeyMetadataWithUsage, error) {
	return r.ListByUser(ctx, userID)
}
func (r *APIKeyRepository) ListAll(ctx context.Context) ([]*models.APIKeyMetadataWithUser, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+apiKeyMetadataColumns+`,ak.last_used_at,u.username FROM api_keys ak JOIN users u ON u.id=ak.user_id ORDER BY ak.created_at DESC,ak.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAPIKeyMetadataList(rows)
}
func scanAPIKeyMetadataList(rows pgx.Rows) ([]*models.APIKeyMetadataWithUser, error) {
	keys := []*models.APIKeyMetadataWithUser{}
	for rows.Next() {
		k := new(models.APIKeyMetadataWithUser)
		if err := rows.Scan(append(apiKeyMetadataFields(&k.APIKeyMetadata), &k.LastUsedAt, &k.Username)...); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}
func (r *APIKeyRepository) ListAllPage(ctx context.Context, after *APIKeyPageKey, limit int) ([]*models.APIKeyMetadataWithUser, bool, error) {
	if limit < 1 {
		limit = 50
	}
	limit = min(limit, 200)
	var stamp *time.Time
	var id int64
	if after != nil {
		stamp = &after.CreatedAt
		id = after.ID
	}
	rows, err := r.pool.Query(ctx, `SELECT `+apiKeyMetadataColumns+`,ak.last_used_at,u.username FROM api_keys ak JOIN users u ON u.id=ak.user_id WHERE ($1::timestamptz IS NULL OR (ak.created_at,ak.id)<($1,$2)) ORDER BY ak.created_at DESC,ak.id DESC LIMIT $3`, stamp, id, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	keys, err := scanAPIKeyMetadataList(rows)
	if err != nil {
		return nil, false, err
	}
	more := len(keys) > limit
	if more {
		keys = keys[:limit]
	}
	return keys, more, nil
}

func lockAPIKeyMetadata(ctx context.Context, tx pgx.Tx, id int64, guard APIKeyPrecondition) (*models.APIKeyMetadata, error) {
	current, err := scanAPIKeyMetadata(tx.QueryRow(ctx, `SELECT `+apiKeyMetadataColumns+` FROM api_keys ak WHERE ak.id=$1 FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	if !guard.Any && guard.Revision != current.Revision {
		return nil, &APIKeyRevisionConflict{Current: current}
	}
	return current, nil
}
func (r *APIKeyRepository) UpdateTierConditional(ctx context.Context, id int64, tier string, guard APIKeyPrecondition) (*models.APIKeyMetadata, error) {
	if tier != apiKeyRateTierStandard && tier != apiKeyRateTierElevated {
		return nil, ErrInvalidAPIKeyTier
	}
	if err := guard.validate(); err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = lockAPIKeyMetadata(ctx, tx, id, guard); err != nil {
		return nil, err
	}
	current, err := scanAPIKeyMetadata(tx.QueryRow(ctx, `UPDATE api_keys ak SET rate_tier=$2 WHERE ak.id=$1 RETURNING `+apiKeyMetadataColumns, id, tier))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return current, nil
}
func (r *APIKeyRepository) DeleteByAdminConditional(ctx context.Context, id int64, guard APIKeyPrecondition) error {
	if err := guard.validate(); err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = lockAPIKeyMetadata(ctx, tx, id, guard); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM api_keys WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListByUserAdminPage is a bounded metadata-only account projection.
func (r *APIKeyRepository) ListByUserAdminPage(ctx context.Context, userID int, after *APIKeyPageKey, limit int) ([]*models.APIKeyMetadataWithUser, bool, error) {
	limit = max(1, min(limit, 200))
	var stamp *time.Time
	var id int64
	if after != nil {
		stamp = &after.CreatedAt
		id = after.ID
	}
	rows, err := r.pool.Query(ctx, `SELECT `+apiKeyMetadataColumns+`,ak.last_used_at,u.username FROM api_keys ak JOIN users u ON u.id=ak.user_id WHERE ak.user_id=$1 AND ($2::timestamptz IS NULL OR (ak.created_at,ak.id)<($2,$3)) ORDER BY ak.created_at DESC,ak.id DESC LIMIT $4`, userID, stamp, id, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	keys, err := scanAPIKeyMetadataList(rows)
	if err != nil {
		return nil, false, err
	}
	more := len(keys) > limit
	if more {
		keys = keys[:limit]
	}
	return keys, more, nil
}
