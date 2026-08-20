package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Sentinel errors for API key operations.
var (
	ErrAPIKeyNotFound = errors.New("api key not found")
)

const apiKeyColumns = `id, user_id, label, api_key, rate_tier, scopes, created_at, last_used_at`

// APIKeyRepository provides CRUD operations for the api_keys table.
type APIKeyRepository struct {
	pool *pgxpool.Pool
}

// NewAPIKeyRepository creates a new APIKeyRepository backed by the given pool.
func NewAPIKeyRepository(pool *pgxpool.Pool) *APIKeyRepository {
	return &APIKeyRepository{pool: pool}
}

// scanAPIKey scans a single row into a *models.APIKey.
func scanAPIKey(row pgx.Row) (*models.APIKey, error) {
	var k models.APIKey
	err := row.Scan(
		&k.ID,
		&k.UserID,
		&k.Label,
		&k.Key,
		&k.RateTier,
		&k.Scopes,
		&k.CreatedAt,
		&k.LastUsedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAPIKeyNotFound
		}
		return nil, fmt.Errorf("scanning api key: %w", err)
	}
	return &k, nil
}

// scanAPIKeys scans multiple rows into a []*models.APIKey slice.
func scanAPIKeys(rows pgx.Rows) ([]*models.APIKey, error) {
	var keys []*models.APIKey
	for rows.Next() {
		var k models.APIKey
		err := rows.Scan(
			&k.ID,
			&k.UserID,
			&k.Label,
			&k.Key,
			&k.RateTier,
			&k.Scopes,
			&k.CreatedAt,
			&k.LastUsedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning api key row: %w", err)
		}
		keys = append(keys, &k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating api key rows: %w", err)
	}
	return keys, nil
}

// generateAPIKey creates a cryptographically random API key with the "sa_" prefix.
func generateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random bytes: %w", err)
	}
	return "sa_" + hex.EncodeToString(b), nil
}

// Create generates a new API key for the given user and returns the full
// record. scopes may be nil or empty for an unscoped key (full access as the
// owning user); callers should validate scopes with NormalizeAPIKeyScopes
// first.
func (r *APIKeyRepository) Create(ctx context.Context, userID int, label string, scopes []string) (*models.APIKey, error) {
	key, err := generateAPIKey()
	if err != nil {
		return nil, err
	}
	if scopes == nil {
		scopes = []string{}
	}

	query := `INSERT INTO api_keys (user_id, label, api_key, scopes)
		VALUES ($1, $2, $3, $4)
		RETURNING ` + apiKeyColumns

	row := r.pool.QueryRow(ctx, query, userID, label, key, scopes)
	return scanAPIKey(row)
}

// ListByUser returns all API keys belonging to the given user, ordered by creation time.
func (r *APIKeyRepository) ListByUser(ctx context.Context, userID int) ([]*models.APIKey, error) {
	query := `SELECT ` + apiKeyColumns + ` FROM api_keys WHERE user_id = $1 ORDER BY created_at DESC`
	rows, err := r.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("listing api keys: %w", err)
	}
	defer rows.Close()
	return scanAPIKeys(rows)
}

// GetByKey looks up an API key by its full key string (including "sa_" prefix).
func (r *APIKeyRepository) GetByKey(ctx context.Context, key string) (*models.APIKey, error) {
	query := `SELECT ` + apiKeyColumns + ` FROM api_keys WHERE api_key = $1`
	return scanAPIKey(r.pool.QueryRow(ctx, query, key))
}

// Delete removes an API key owned by the given user.
func (r *APIKeyRepository) Delete(ctx context.Context, id int64, userID int) error {
	tag, err := r.pool.Exec(ctx, "DELETE FROM api_keys WHERE id = $1 AND user_id = $2", id, userID)
	if err != nil {
		return fmt.Errorf("deleting api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}

// DeleteByAdmin removes any API key by its ID regardless of owner.
func (r *APIKeyRepository) DeleteByAdmin(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, "DELETE FROM api_keys WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("deleting api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}

// ListByUserAdmin returns all API keys for a specific user (admin view).
func (r *APIKeyRepository) ListByUserAdmin(ctx context.Context, userID int) ([]*models.APIKey, error) {
	return r.ListByUser(ctx, userID)
}

// ListAll returns all API keys across all users, ordered by creation time descending.
// Each entry includes the owning user's username.
func (r *APIKeyRepository) ListAll(ctx context.Context) ([]*models.APIKeyWithUser, error) {
	query := `SELECT ak.id, ak.user_id, u.username, ak.label, ak.api_key, ak.rate_tier, ak.scopes, ak.created_at, ak.last_used_at
		FROM api_keys ak
		JOIN users u ON u.id = ak.user_id
		ORDER BY ak.created_at DESC`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing all api keys: %w", err)
	}
	defer rows.Close()

	var keys []*models.APIKeyWithUser
	for rows.Next() {
		var k models.APIKeyWithUser
		err := rows.Scan(
			&k.ID,
			&k.UserID,
			&k.Username,
			&k.Label,
			&k.Key,
			&k.RateTier,
			&k.Scopes,
			&k.CreatedAt,
			&k.LastUsedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning api key with user row: %w", err)
		}
		keys = append(keys, &k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating api key with user rows: %w", err)
	}
	return keys, nil
}

// GetByID looks up an API key by its ID.
func (r *APIKeyRepository) GetByID(ctx context.Context, id int64) (*models.APIKey, error) {
	query := `SELECT ` + apiKeyColumns + ` FROM api_keys WHERE id = $1`
	return scanAPIKey(r.pool.QueryRow(ctx, query, id))
}

// UpdateTier updates the rate tier for an API key.
func (r *APIKeyRepository) UpdateTier(ctx context.Context, id int64, tier string) error {
	result, err := r.pool.Exec(ctx, `UPDATE api_keys SET rate_tier = $1 WHERE id = $2`, tier, id)
	if err != nil {
		return fmt.Errorf("update api key tier: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}

// UpdateLastUsed sets last_used_at to now. Intended to be called asynchronously.
func (r *APIKeyRepository) UpdateLastUsed(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx, "UPDATE api_keys SET last_used_at = NOW() WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("updating api key last_used_at: %w", err)
	}
	return nil
}
