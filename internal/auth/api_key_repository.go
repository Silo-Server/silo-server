package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Sentinel errors for API key operations.
var (
	ErrAPIKeyNotFound = errors.New("api key not found")
)

const apiKeyColumns = `id, user_id, label, rate_tier, scopes, COALESCE(profile_id, ''), created_at, last_used_at` //nolint:gosec // SQL column names, not credentials.

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
		&k.RateTier,
		&k.Scopes,
		&k.ProfileID,
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

// ValidTextKey reports whether a client-supplied lookup key (an API key or a
// session token) can be sent to Postgres as a text parameter: valid UTF-8
// with no NUL byte. Postgres rejects any other value with an encoding error
// (SQLSTATE 22021), which a caller would mistake for an unreachable store; a
// key that fails this check cannot match a stored one.
func ValidTextKey(key string) bool {
	return utf8.ValidString(key) && !strings.ContainsRune(key, 0)
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
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning api key creation: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	return insertAPIKey(ctx, tx, userID, "", label, scopes)
}

// CreateAsActor is Create for a key that actorID mints on account userID,
// refused when CheckOwnerTarget refuses actorID acting on that account. Both
// accounts stay share-locked, in id order as ownership transfers lock them,
// until the key is inserted: a promotion or transfer that commits first is
// what the check sees, and a later promotion of the account, or transfer of
// ownership to it, deletes the key with the account's other keys. An unknown account passes the check and fails the
// insert, as it does with Create.
func (r *APIKeyRepository) CreateAsActor(ctx context.Context, actorID, userID int, label string, scopes []string) (*models.APIKey, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning api key creation: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	// The audit row's accounts, an impersonator included, join that id order.
	rows, err := tx.Query(ctx, `SELECT id, role, is_owner FROM users
		WHERE id = ANY($1) ORDER BY id FOR SHARE`, append([]int{actorID, userID}, auditmutation.ActorIDs(ctx)...))
	if err != nil {
		return nil, fmt.Errorf("locking api key accounts: %w", err)
	}
	actor := OwnerActor{ID: actorID}
	var target *models.User
	for rows.Next() {
		var user models.User
		if err := rows.Scan(&user.ID, &user.Role, &user.IsOwner); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning api key account: %w", err)
		}
		if user.ID == actorID {
			actor.IsOwner = user.IsOwner
		}
		if user.ID == userID {
			target = &user
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("locking api key accounts: %w", err)
	}
	if err := CheckOwnerTarget(actor, target); err != nil {
		return nil, err
	}
	if err := requestCredentialLive(ctx, tx); err != nil {
		return nil, err
	}
	return insertAPIKey(ctx, tx, userID, "", label, scopes)
}

// requestCredentialLive refuses a key request whose own login session or API
// key was revoked after the request authenticated. Callers hold the
// requesting account's row locked; revoking an account's sign-ins locks that
// row FOR UPDATE first, so a revocation either committed before this read or
// waits for the caller's commit and then revokes or deletes what it wrote.
// The credential's own row is locked too, so revoking just that session or
// deleting just that key also waits for the new key. A context without claims
// (a trusted internal caller) passes.
func requestCredentialLive(ctx context.Context, tx pgx.Tx) error {
	claims := ClaimsFromContext(ctx)
	var live bool
	var err error
	switch {
	case claims == nil:
		return nil
	case claims.TokenType == TokenTypeAPIKey:
		err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM api_keys WHERE id = $1 FOR KEY SHARE)`, claims.APIKeyID).Scan(&live)
	case claims.SessionID != "":
		err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_sessions WHERE id = $1 AND revoked_at IS NULL FOR SHARE)`, claims.SessionID).Scan(&live)
	default:
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking the request's credential: %w", err)
	}
	if !live {
		return ErrSessionRevoked
	}
	return nil
}

// MaxUserAPIKeys bounds the keys one account may hold once it creates them
// for itself (CreatePersonal). Keys an admin mints are not counted against
// their own creation, only against the next personal one.
const MaxUserAPIKeys = 25

var (
	// ErrUserAPIKeysDisabled refuses a personal key while
	// config.AuthUserAPIKeysSettingKey is off.
	ErrUserAPIKeysDisabled = errors.New("user api keys are turned off on this server")
	// ErrAPIKeyUserScopesRequired refuses a personal key that is unscoped or
	// names a scope outside the user audience.
	ErrAPIKeyUserScopesRequired = errors.New("this api key must carry user scopes only")
	// ErrAPIKeyLimitReached refuses a personal key past MaxUserAPIKeys.
	ErrAPIKeyLimitReached = errors.New("api key limit reached")
	// ErrAPIKeyProfileNotFound refuses a key bound to a profile the account
	// does not have.
	ErrAPIKeyProfileNotFound = errors.New("api key profile not found")
	// ErrAPIKeyActingProfileChanged refuses a personal key when the profile
	// that asked for it is gone or changed its PIN after it was verified.
	ErrAPIKeyActingProfileChanged = errors.New("the profile creating the api key changed")
)

// APIKeyActingProfile is the profile whose PIN proof authorized a personal
// key, at the PIN revision that proof was checked against. The zero value
// names no profile: a request that acted without one.
type APIKeyActingProfile struct {
	ID          string
	PINRevision int64
}

// UserAPIKeysEnabled reads config.AuthUserAPIKeysSettingKey.
func (r *APIKeyRepository) UserAPIKeysEnabled(ctx context.Context) (bool, error) {
	return readBoolSetting(ctx, r.pool, config.AuthUserAPIKeysSettingKey, false)
}

// CreatePersonal mints a key an account creates for itself that is not an
// admin's account key: one bound to profileID, or an account key (empty
// profileID) on an account that is not an admin. Such a key is a profile's
// credential, so it must carry user scopes only, the server must allow user
// keys, and the account stays under MaxUserAPIKeys. The setting and the
// limit are judged in the inserting transaction under the server-settings
// mutation lock, which a setting change also takes: a key is never stored
// after the setting was turned off, and concurrent creations count each
// other's keys. The acting profile is rechecked there too, under its row
// lock: a key skips profile PINs for good, so one must not be stored on a
// PIN proof that a PIN change has since ended. An admin's
// own account key is Create's; an empty profileID on an admin account is
// still held to user scopes here.
func (r *APIKeyRepository) CreatePersonal(ctx context.Context, userID int, acting APIKeyActingProfile, profileID, label string, scopes []string) (*models.APIKey, error) {
	if !UserAPIKeyScopesOnly(scopes) {
		return nil, ErrAPIKeyUserScopesRequired
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning api key creation: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	// Before the account locks, as signup takes it.
	if err := lockServerSettings(ctx, tx); err != nil {
		return nil, err
	}
	// The key and audit rows' accounts, in id order, with the key-share lock
	// their foreign keys take anyway. Nothing stronger: a profile update
	// locks its profile row and then bumps the account's policy revision,
	// and a share lock here would deadlock with it over the acting profile.
	if _, err := tx.Exec(ctx, `SELECT id FROM users WHERE id = ANY($1) ORDER BY id FOR KEY SHARE`,
		append([]int{userID}, auditmutation.ActorIDs(ctx)...)); err != nil {
		return nil, fmt.Errorf("locking api key account: %w", err)
	}
	if err := requestCredentialLive(ctx, tx); err != nil {
		return nil, err
	}
	if acting.ID != "" {
		var revision int64
		err := tx.QueryRow(ctx, `SELECT pin_revision FROM user_profiles WHERE user_id = $1 AND id = $2 FOR SHARE`,
			userID, acting.ID).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && revision != acting.PINRevision) {
			return nil, ErrAPIKeyActingProfileChanged
		}
		if err != nil {
			return nil, fmt.Errorf("checking the acting profile: %w", err)
		}
	}
	enabled, err := readBoolSetting(ctx, tx, config.AuthUserAPIKeysSettingKey, false)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, ErrUserAPIKeysDisabled
	}
	var held int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE user_id = $1`, userID).Scan(&held); err != nil {
		return nil, fmt.Errorf("counting api keys: %w", err)
	}
	if held >= MaxUserAPIKeys {
		return nil, ErrAPIKeyLimitReached
	}
	key, err := insertAPIKey(ctx, tx, userID, profileID, label, scopes)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23503" && pgErr.ConstraintName == "api_keys_profile_fkey" {
		return nil, ErrAPIKeyProfileNotFound
	}
	return key, err
}

// insertAPIKey inserts the key and its audit row in tx and commits tx. A
// non-empty profileID binds the key to that profile of userID.
func insertAPIKey(ctx context.Context, tx pgx.Tx, userID int, profileID, label string, scopes []string) (*models.APIKey, error) {
	key, err := generateAPIKey()
	if err != nil {
		return nil, err
	}
	if scopes == nil {
		scopes = []string{}
	}

	query := `INSERT INTO api_keys (user_id, label, key_hash, key_prefix, scopes, profile_id)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''))
		RETURNING ` + apiKeyColumns

	created, err := scanAPIKey(tx.QueryRow(ctx, query, userID, label, HashLinkToken(key), key[:11], scopes, profileID))
	if err != nil {
		return nil, err
	}
	audit, err := recordAPIKeyMutation(ctx, tx, nil, &models.APIKeyMetadata{ID: created.ID, UserID: created.UserID,
		Label: created.Label, RateTier: created.RateTier, Scopes: created.Scopes, ProfileID: created.ProfileID})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing api key creation: %w", err)
	}
	auditmutation.CommitMutation(ctx, audit)
	// The bearer is disclosed once. No ordinary row projection carries it.
	created.Key = key
	return created, nil
}

// GetByKey looks up an API key by its full key string (including "sa_" prefix).
// A key Postgres cannot take as text matches no key: ErrAPIKeyNotFound, not
// the query error that would read as a store outage.
func (r *APIKeyRepository) GetByKey(ctx context.Context, key string) (*models.APIKey, error) {
	if !ValidTextKey(key) {
		return nil, ErrAPIKeyNotFound
	}
	// These bearers contain 256 random bits. The indexed SHA-256 digest is
	// both the lookup key and verifier, as for password-reset and invitation
	// link tokens. Do not tighten the public format here: legacy credentials
	// remain valid.
	query := `SELECT ` + apiKeyColumns + ` FROM api_keys WHERE key_hash = $1`
	return scanAPIKey(r.pool.QueryRow(ctx, query, HashLinkToken(key)))
}

// Delete removes an API key owned by the given user.
func (r *APIKeyRepository) Delete(ctx context.Context, id int64, userID int) error {
	return r.DeleteForProfile(ctx, id, userID, "")
}

// DeleteForProfile is Delete narrowed, when profileID is set, to a key bound
// to that profile; any other key answers ErrAPIKeyNotFound.
func (r *APIKeyRepository) DeleteForProfile(ctx context.Context, id int64, userID int, profileID string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning api key deletion: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	// The audit row's account locks come before the key row lock, the order
	// an account deletion takes them in.
	if err := auditmutation.LockActors(ctx, tx); err != nil {
		return err
	}
	deleted, err := scanAPIKeyMetadata(tx.QueryRow(ctx, `DELETE FROM api_keys ak WHERE ak.id = $1 AND ak.user_id = $2 AND ($3 = '' OR ak.profile_id = $3) RETURNING `+apiKeyMetadataColumns, id, userID, profileID))
	if errors.Is(err, ErrAPIKeyNotFound) {
		return ErrAPIKeyNotFound
	}
	if err != nil {
		return fmt.Errorf("deleting api key: %w", err)
	}
	audit, err := recordAPIKeyMutation(ctx, tx, deleted, nil)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing api key deletion: %w", err)
	}
	auditmutation.CommitMutation(ctx, audit)
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
