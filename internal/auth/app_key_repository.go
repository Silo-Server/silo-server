package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Sentinel errors for app key storage (docs/architecture/app-keys.md).
var (
	// ErrAppKeyNotFound is returned for a key that does not exist, or not on
	// the account named.
	ErrAppKeyNotFound = errors.New("app key not found")
	// ErrAppKeyAlreadyEnrolled is returned when the public key is already
	// stored on this server, as a person key or a device key, revoked or not.
	// A public key is enrolled once per server.
	ErrAppKeyAlreadyEnrolled = errors.New("app key already enrolled on this server")
	// ErrPersonKeyExists is returned when the account already holds a person
	// key that is not revoked.
	ErrPersonKeyExists = errors.New("account already has a person key")
	// ErrAppKeyRevoked is returned when a revoked person key is named to
	// vouch for a new device key.
	ErrAppKeyRevoked = errors.New("app key revoked")
	// ErrAppKeyInvalid is returned for a key whose kind, status or way of
	// enrolling is not one its table takes.
	ErrAppKeyInvalid = errors.New("invalid app key")
)

const personKeyColumns = `id::text, user_id, public_key, key_id, identity_id, provider_bound, enrolled_via, COALESCE(host(enrolled_ip_address), ''), created_at, revoked_at`

const deviceKeyColumns = `id::text, user_id, public_key, key_id, kind, name, platform, status, enrolled_via, person_key_id::text, identity_id, provider_bound, COALESCE(host(enrolled_ip_address), ''), created_at, last_used_at, revoked_at, last_attempt_at`

// Unique constraints Postgres names after the columns in the migration.
const (
	personKeyPublicKeyUnique = "auth_person_keys_public_key_key"
	personKeyKeyIDUnique     = "auth_person_keys_key_id_key"
	personKeyLiveUnique      = "auth_person_keys_one_live_per_user"
	deviceKeyPublicKeyUnique = "auth_device_keys_public_key_key"
	deviceKeyKeyIDUnique     = "auth_device_keys_key_id_key"
)

// AppKeyRepository stores app keys: an account's person key and its device
// keys. Every key path takes its locks in one order: the account row
// (lockUser), the plugin installation and provider identity of a
// provider-bound key (keySignInPolicy), the person key, the device key rows,
// then the login sessions those keys opened. Key rows are written only here,
// in application transactions that hold the account lock: a key's
// identity_id has no foreign key, so deleting an identity (the cascade from
// uninstalling a plugin, which takes no account lock) never writes one.
type AppKeyRepository struct {
	pool *pgxpool.Pool
}

// NewAppKeyRepository creates an AppKeyRepository backed by the given pool.
func NewAppKeyRepository(pool *pgxpool.Pool) *AppKeyRepository {
	return &AppKeyRepository{pool: pool}
}

// AppKeySignInEnabled reads config.AuthAppKeySignInSettingKey live, so a
// change on any node applies at once.
func (r *AppKeyRepository) AppKeySignInEnabled(ctx context.Context) (bool, error) {
	return appKeySignInAllowed(ctx, r.pool)
}

func appKeySignInAllowed(ctx context.Context, db rowQuerier) (bool, error) {
	return readBoolSetting(ctx, db, config.AuthAppKeySignInSettingKey, true)
}

// NewPersonKey is a person key to enroll.
type NewPersonKey struct {
	UserID    int
	PublicKey appkeys.PublicKey
	// IdentityID is the provider identity of the enrolling session, nil for
	// a local one or one whose identity is gone.
	IdentityID *int64
	// ProviderBound marks a key enrolled from a provider session. It is set
	// whenever IdentityID is, and stays set for a session whose identity row
	// was deleted.
	ProviderBound bool
	EnrolledVia   string
	// IP is the client address of the enrolling request, the one the login
	// session it opens records (KeySession.IP); empty when unknown.
	IP string
}

// NewDeviceKey is a device key to enroll.
type NewDeviceKey struct {
	UserID    int
	PublicKey appkeys.PublicKey
	Kind      string
	Name      string
	Platform  string
	// Status defaults to models.DeviceKeyStatusActive.
	Status      string
	EnrolledVia string
	PersonKeyID *string
	// IdentityID and ProviderBound are as on NewPersonKey: an introduced key
	// takes its person key's.
	IdentityID    *int64
	ProviderBound bool
	// IP is as on NewPersonKey.
	IP string
}

func scanPersonKey(row pgx.Row) (*models.AppPersonKey, error) {
	var k models.AppPersonKey
	err := row.Scan(&k.ID, &k.UserID, &k.PublicKey, &k.KeyID, &k.IdentityID, &k.ProviderBound, &k.EnrolledVia, &k.EnrolledIP, &k.CreatedAt, &k.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAppKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning person key: %w", err)
	}
	return &k, nil
}

func scanDeviceKey(row pgx.Row) (*models.AppDeviceKey, error) {
	var k models.AppDeviceKey
	err := row.Scan(&k.ID, &k.UserID, &k.PublicKey, &k.KeyID, &k.Kind, &k.Name, &k.Platform, &k.Status, &k.EnrolledVia,
		&k.PersonKeyID, &k.IdentityID, &k.ProviderBound, &k.EnrolledIP, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt, &k.LastAttemptAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAppKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning device key: %w", err)
	}
	return &k, nil
}

// inAccountTx runs fn in a transaction holding the account row lock.
func (r *AppKeyRepository) inAccountTx(ctx context.Context, userID int, fn func(pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning app key transaction: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	if _, err := lockUser(ctx, tx, userID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// InsertPersonKey enrolls a person key on its own.
func (r *AppKeyRepository) InsertPersonKey(ctx context.Context, k NewPersonKey) (*models.AppPersonKey, error) {
	var out *models.AppPersonKey
	err := r.inAccountTx(ctx, k.UserID, func(tx pgx.Tx) error {
		var err error
		out, err = insertPersonKeyTx(ctx, tx, k)
		return err
	})
	return out, err
}

// InsertDeviceKey enrolls a device key on its own.
func (r *AppKeyRepository) InsertDeviceKey(ctx context.Context, k NewDeviceKey) (*models.AppDeviceKey, error) {
	var out *models.AppDeviceKey
	err := r.inAccountTx(ctx, k.UserID, func(tx pgx.Tx) error {
		var err error
		out, err = insertDeviceKeyTx(ctx, tx, k)
		return err
	})
	return out, err
}

// claimPublicKey serializes enrollments of one public key across both key
// tables and refuses a key already stored in either. Each table's unique
// constraints cover only that table; the transaction-scoped lock on the key
// ID makes the check across them race-free.
func claimPublicKey(ctx context.Context, tx pgx.Tx, key appkeys.PublicKey) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('auth_app_key:' || $1, 0))`, key.KeyID()); err != nil {
		return fmt.Errorf("locking app key: %w", err)
	}
	var taken bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_person_keys WHERE public_key = $1 OR key_id = $2)
		OR EXISTS (SELECT 1 FROM auth_device_keys WHERE public_key = $1 OR key_id = $2)`, key.Bytes(), key.KeyID()).Scan(&taken)
	if err != nil {
		return fmt.Errorf("checking app key: %w", err)
	}
	if taken {
		return ErrAppKeyAlreadyEnrolled
	}
	return nil
}

// insertPersonKeyTx enrolls a person key in tx, which holds the account row
// lock (lockUser).
func insertPersonKeyTx(ctx context.Context, tx pgx.Tx, k NewPersonKey) (*models.AppPersonKey, error) {
	if k.EnrolledVia != models.AppKeyEnrolledSession && k.EnrolledVia != models.AppKeyEnrolledAppCode {
		return nil, ErrAppKeyInvalid
	}
	if err := claimPublicKey(ctx, tx, k.PublicKey); err != nil {
		return nil, err
	}
	row, err := scanPersonKey(tx.QueryRow(ctx, `INSERT INTO auth_person_keys
		(id, user_id, public_key, key_id, identity_id, provider_bound, enrolled_via, enrolled_ip_address)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::inet)
		RETURNING `+personKeyColumns,
		uuid.NewString(), k.UserID, k.PublicKey.Bytes(), k.PublicKey.KeyID(), k.IdentityID, k.ProviderBound || k.IdentityID != nil, k.EnrolledVia, enrolledAddress(k.IP)))
	return row, appKeyInsertError(err)
}

// enrolledAddress is the address a key row records for the enrolling
// request's ip: none for the unspecified address (0.0.0.0 or ::), which the
// client-address resolver answers for a client it could not tell, so the
// lists show none rather than a made-up address. A login session's row keeps
// what it is given; this is the one place a key's address differs from it.
func enrolledAddress(ip string) string {
	if parsed := net.ParseIP(ip); parsed == nil || parsed.IsUnspecified() {
		return ""
	}
	return ip
}

// validDeviceKey reports whether k's kind, status and way of enrolling are
// ones auth_device_keys takes.
func validDeviceKey(k NewDeviceKey, status string) bool {
	kind := k.Kind == models.DeviceKeyKindDevice || k.Kind == models.DeviceKeyKindApp
	state := status == models.DeviceKeyStatusActive || status == models.DeviceKeyStatusPending || status == models.DeviceKeyStatusDenied
	switch k.EnrolledVia {
	case models.AppKeyEnrolledIntroduce, models.AppKeyEnrolledSession, models.AppKeyEnrolledAppCode, models.AppKeyEnrolledGrant:
		return kind && state
	}
	return false
}

// lockPersonKey locks one of the account's person keys in tx, which already
// holds the account row lock. A key of another account is not found.
func lockPersonKey(ctx context.Context, tx pgx.Tx, userID int, id string) (*models.AppPersonKey, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrAppKeyNotFound
	}
	return scanPersonKey(tx.QueryRow(ctx, `SELECT `+personKeyColumns+` FROM auth_person_keys
		WHERE id = $1 AND user_id = $2 FOR UPDATE`, id, userID))
}

// insertDeviceKeyTx enrolls a device key in tx, which holds the account row
// lock (lockUser). A PersonKeyID must name a person key of the same account
// that is not revoked; its row is locked here, before any device key row.
func insertDeviceKeyTx(ctx context.Context, tx pgx.Tx, k NewDeviceKey) (*models.AppDeviceKey, error) {
	status := k.Status
	if status == "" {
		status = models.DeviceKeyStatusActive
	}
	if !validDeviceKey(k, status) {
		return nil, ErrAppKeyInvalid
	}
	if k.PersonKeyID != nil {
		person, err := lockPersonKey(ctx, tx, k.UserID, *k.PersonKeyID)
		if err != nil {
			return nil, err
		}
		if person.RevokedAt != nil {
			return nil, ErrAppKeyRevoked
		}
	}
	if err := claimPublicKey(ctx, tx, k.PublicKey); err != nil {
		return nil, err
	}
	row, err := scanDeviceKey(tx.QueryRow(ctx, `INSERT INTO auth_device_keys
		(id, user_id, public_key, key_id, kind, name, platform, status, enrolled_via, person_key_id, identity_id, provider_bound, enrolled_ip_address)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::uuid, $11, $12, NULLIF($13, '')::inet)
		RETURNING `+deviceKeyColumns,
		uuid.NewString(), k.UserID, k.PublicKey.Bytes(), k.PublicKey.KeyID(), k.Kind, k.Name, k.Platform, status,
		k.EnrolledVia, k.PersonKeyID, k.IdentityID, k.ProviderBound || k.IdentityID != nil, enrolledAddress(k.IP)))
	return row, appKeyInsertError(err)
}

// appKeyInsertError maps the unique constraints to their sentinels. The
// claim check finds a duplicate first; these cover a row the check could
// not see.
func appKeyInsertError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return err
	}
	switch pgErr.ConstraintName {
	case personKeyPublicKeyUnique, personKeyKeyIDUnique, deviceKeyPublicKeyUnique, deviceKeyKeyIDUnique:
		return ErrAppKeyAlreadyEnrolled
	case personKeyLiveUnique:
		return ErrPersonKeyExists
	}
	return err
}

// PersonKeyByKeyID finds a person key by its key ID, revoked or not.
func (r *AppKeyRepository) PersonKeyByKeyID(ctx context.Context, keyID string) (*models.AppPersonKey, error) {
	if !ValidTextKey(keyID) {
		return nil, ErrAppKeyNotFound
	}
	return scanPersonKey(r.pool.QueryRow(ctx, `SELECT `+personKeyColumns+` FROM auth_person_keys WHERE key_id = $1`, keyID))
}

// LivePersonKey finds the account's person key that is not revoked, leaving
// out a provider-bound key whose identity is gone from the account, as the
// key list does.
func (r *AppKeyRepository) LivePersonKey(ctx context.Context, userID int) (*models.AppPersonKey, error) {
	return scanPersonKey(r.pool.QueryRow(ctx, `SELECT `+personKeyColumns+` FROM auth_person_keys k
		WHERE user_id = $1 AND revoked_at IS NULL AND `+identityLive, userID))
}

// UnrevokedPersonKey finds the account's person key that is not revoked,
// whether or not its identity still exists: a caller that goes on to use the
// key revokes one whose identity is gone.
func (r *AppKeyRepository) UnrevokedPersonKey(ctx context.Context, userID int) (*models.AppPersonKey, error) {
	return scanPersonKey(r.pool.QueryRow(ctx, `SELECT `+personKeyColumns+` FROM auth_person_keys
		WHERE user_id = $1 AND revoked_at IS NULL`, userID))
}

// DeviceKeyByKeyID finds a device key by its key ID, revoked or not.
func (r *AppKeyRepository) DeviceKeyByKeyID(ctx context.Context, keyID string) (*models.AppDeviceKey, error) {
	if !ValidTextKey(keyID) {
		return nil, ErrAppKeyNotFound
	}
	return scanDeviceKey(r.pool.QueryRow(ctx, `SELECT `+deviceKeyColumns+` FROM auth_device_keys WHERE key_id = $1`, keyID))
}

// AccountKeys is an account's keys that are not revoked.
type AccountKeys struct {
	// Person is nil when the account has no live person key.
	Person  *models.AppPersonKey
	Devices []*models.AppDeviceKey
}

// identityLive holds for a key row k that is not provider-bound, or whose
// identity still exists on its own account. A provider-bound key whose
// identity is gone is dead (revoked at its next use) and is never listed.
const identityLive = `(NOT k.provider_bound OR k.identity_id IS NULL OR EXISTS (
	SELECT 1 FROM plugin_auth_identities i WHERE i.id = k.identity_id AND i.user_id = k.user_id))`

// ListAccountKeys returns the account's live keys, device keys newest first.
func (r *AppKeyRepository) ListAccountKeys(ctx context.Context, userID int) (AccountKeys, error) {
	var out AccountKeys
	person, err := r.LivePersonKey(ctx, userID)
	switch {
	case err == nil:
		out.Person = person
	case !errors.Is(err, ErrAppKeyNotFound):
		return AccountKeys{}, err
	}
	rows, err := r.pool.Query(ctx, `SELECT `+deviceKeyColumns+` FROM auth_device_keys k
		WHERE user_id = $1 AND revoked_at IS NULL AND `+identityLive+` ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return AccountKeys{}, fmt.Errorf("listing device keys: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		key, err := scanDeviceKey(rows)
		if err != nil {
			return AccountKeys{}, err
		}
		out.Devices = append(out.Devices, key)
	}
	if err := rows.Err(); err != nil {
		return AccountKeys{}, fmt.Errorf("listing device keys: %w", err)
	}
	return out, nil
}

// lockDeviceKey locks one of the account's device keys in tx, which already
// holds the account row lock. Revocation and opening a session by key both
// take it, so neither can slip past the other.
func lockDeviceKey(ctx context.Context, tx pgx.Tx, userID int, id string) (*models.AppDeviceKey, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrAppKeyNotFound
	}
	return scanDeviceKey(tx.QueryRow(ctx, `SELECT `+deviceKeyColumns+` FROM auth_device_keys
		WHERE id = $1 AND user_id = $2 FOR UPDATE`, id, userID))
}

// RevokeDeviceKey revokes one of the account's device keys and ends every
// login session it opened, in one transaction. Revoking a revoked key
// succeeds and changes nothing.
func (r *AppKeyRepository) RevokeDeviceKey(ctx context.Context, userID int, id string) error {
	return r.inAccountTx(ctx, userID, func(tx pgx.Tx) error {
		if _, err := lockDeviceKey(ctx, tx, userID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE auth_device_keys SET revoked_at = COALESCE(revoked_at, NOW()) WHERE id = $1`, id); err != nil {
			return fmt.Errorf("revoking device key: %w", err)
		}
		return endDeviceKeySessions(ctx, tx, userID, []string{id})
	})
}

// revokeDeviceKeyTx revokes one of the account's device keys in tx, which
// holds the account row lock, and ends the sessions it opened: the key row is
// locked before the sessions, the order every key path takes.
func revokeDeviceKeyTx(ctx context.Context, tx pgx.Tx, userID int, id string) error {
	if _, err := lockDeviceKey(ctx, tx, userID, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_device_keys SET revoked_at = COALESCE(revoked_at, NOW()) WHERE id = $1`, id); err != nil {
		return fmt.Errorf("revoking device key: %w", err)
	}
	return endDeviceKeySessions(ctx, tx, userID, []string{id})
}

// revokePersonKeyTx revokes one of the account's person keys in tx, which
// holds the account row lock.
func revokePersonKeyTx(ctx context.Context, tx pgx.Tx, userID int, id string) error {
	if _, err := tx.Exec(ctx, `UPDATE auth_person_keys SET revoked_at = COALESCE(revoked_at, NOW())
		WHERE id = $1 AND user_id = $2`, id, userID); err != nil {
		return fmt.Errorf("revoking person key: %w", err)
	}
	return nil
}

// revokeIdentityKeysTx revokes the account's keys bound to identityID and
// ends the sessions its device keys opened, in tx, which holds the account
// row lock, before the identity is deleted (deleteIdentity): key rows, then
// sessions. The account is compared as well as the identity.
func revokeIdentityKeysTx(ctx context.Context, tx sessionExecQuerier, userID int, identityID int64) error {
	if _, err := tx.Exec(ctx, `UPDATE auth_person_keys SET revoked_at = NOW()
		WHERE user_id = $1 AND identity_id = $2 AND revoked_at IS NULL`, userID, identityID); err != nil {
		return fmt.Errorf("revoking the identity's person key: %w", err)
	}
	if _, err := tx.Exec(ctx, `WITH revoked AS (
			UPDATE auth_device_keys SET revoked_at = NOW()
			WHERE user_id = $1 AND identity_id = $2 AND revoked_at IS NULL RETURNING id
		)
		UPDATE auth_sessions SET revoked_at = COALESCE(revoked_at, NOW())
		WHERE device_key_id IN (SELECT id FROM revoked) AND revoked_at IS NULL`, userID, identityID); err != nil {
		return fmt.Errorf("revoking the identity's device keys: %w", err)
	}
	return nil
}

// RevokePersonKey revokes the account's live person key. Device keys it
// vouched for keep working; it can no longer vouch for new ones.
func (r *AppKeyRepository) RevokePersonKey(ctx context.Context, userID int) error {
	return r.inAccountTx(ctx, userID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE auth_person_keys SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
		if err != nil {
			return fmt.Errorf("revoking person key: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrAppKeyNotFound
		}
		return nil
	})
}

// RevokeAccountKeys revokes every key the account holds and ends the login
// sessions its device keys opened.
func (r *AppKeyRepository) RevokeAccountKeys(ctx context.Context, userID int) error {
	return r.inAccountTx(ctx, userID, func(tx pgx.Tx) error {
		return revokeAccountKeysTx(ctx, tx, userID)
	})
}

// revokeAccountKeysTx is RevokeAccountKeys in tx, which holds the account
// row lock. The credential sweeps of account changes (a provider refusing
// the person, an email match taking the account over, a promotion to admin
// or Owner) call it in their own transaction, so it needs only Exec. The
// device keys' rows are locked by their UPDATE before the sessions they
// opened, the documented order. Those sessions carry the identity their key
// was bound to, so the account's identities are taken first, the order a
// plugin uninstall takes them (lockIdentitiesBeforeSessions).
func revokeAccountKeysTx(ctx context.Context, tx sessionExecQuerier, userID int) error {
	if err := lockIdentitiesBeforeSessions(ctx, tx, userID, false); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_person_keys SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
		return fmt.Errorf("revoking person keys: %w", err)
	}
	if _, err := tx.Exec(ctx, `WITH revoked AS (
			UPDATE auth_device_keys SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL RETURNING id
		)
		UPDATE auth_sessions SET revoked_at = COALESCE(revoked_at, NOW())
		WHERE device_key_id IN (SELECT id FROM revoked) AND revoked_at IS NULL`, userID); err != nil {
		return fmt.Errorf("revoking device keys: %w", err)
	}
	return nil
}

// endDeviceKeySessions ends the live login sessions userID's device keys
// opened, in tx, which holds the account row lock and has already locked the
// keys' rows. A key can have opened several sessions, each carrying the
// identity the key was bound to, so the account's identities are locked
// before those sessions, the order a plugin uninstall takes them
// (lockIdentitiesBeforeSessions). The order is account, key rows,
// identities, sessions; revoking every key of the account takes the
// identities before the key rows instead. Both are safe: every writer of
// key rows holds the account row lock, and an uninstall never writes key
// rows (a key's identity_id has no foreign key to the identity).
func endDeviceKeySessions(ctx context.Context, tx pgx.Tx, userID int, ids []string) error {
	if err := lockIdentitiesBeforeSessions(ctx, tx, userID, false); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at = COALESCE(revoked_at, NOW())
		WHERE device_key_id = ANY($1::uuid[]) AND revoked_at IS NULL`, ids); err != nil {
		return fmt.Errorf("ending device key sessions: %w", err)
	}
	return nil
}

// bindAccountKeysToIdentity brings the account's live keys that are not
// provider-bound under identityID, when linking makes the provider answer for
// the account (linkIdentityTx). From then on the provider rule applies to them
// like any provider-bound key. tx holds the account row lock and has just
// inserted the identity.
func bindAccountKeysToIdentity(ctx context.Context, tx sessionExecQuerier, userID int, identityID int64) error {
	if _, err := tx.Exec(ctx, `UPDATE auth_person_keys SET identity_id = $2, provider_bound = true
		WHERE user_id = $1 AND revoked_at IS NULL AND NOT provider_bound`, userID, identityID); err != nil {
		return fmt.Errorf("binding person key to the identity: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_device_keys SET identity_id = $2, provider_bound = true
		WHERE user_id = $1 AND revoked_at IS NULL AND NOT provider_bound`, userID, identityID); err != nil {
		return fmt.Errorf("binding device keys to the identity: %w", err)
	}
	return nil
}

// deleteAccountKeysTx deletes every key of an account being deleted, in tx,
// which holds the account row lock: the device keys first, which ends the
// sessions they opened by cascade (key rows before sessions, as everywhere),
// then the person keys, before the account's other sessions are revoked.
func deleteAccountKeysTx(ctx context.Context, tx sessionExecQuerier, userID int) error {
	if _, err := tx.Exec(ctx, `DELETE FROM auth_device_keys WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("deleting device keys: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM auth_person_keys WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("deleting person keys: %w", err)
	}
	return nil
}
