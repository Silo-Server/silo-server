package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// KeyBinding is the provider binding of a device key, or of one about to be
// enrolled: the identity of the session that enrolled it, and whether that
// session came from a provider at all.
type KeyBinding struct {
	IdentityID    *int64
	ProviderBound bool
}

func bindingOfDeviceKey(key *models.AppDeviceKey) KeyBinding {
	return KeyBinding{IdentityID: key.IdentityID, ProviderBound: key.ProviderBound}
}

// accountHasIdentity reports whether the account has an identity with this
// ID. The account is always compared: a number that names another account's
// identity is not this account's.
func accountHasIdentity(ctx context.Context, db rowQuerier, userID int, identityID int64) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plugin_auth_identities WHERE id = $1 AND user_id = $2)`,
		identityID, userID).Scan(&exists)
	return exists, err
}

// identityAnchor is when the provider last vouched for the person behind an
// identity: its last interactive sign-in or ACTIVE re-check answer, or its
// linking until there is one (docs/architecture/external-sign-in.md,
// "Provider re-check"). A key session's provider_since is this, so a key
// sign-in never restarts the age of a chain the provider can't re-check.
func identityAnchor(ctx context.Context, db rowQuerier, userID int, identityID int64) (time.Time, error) {
	var anchor time.Time
	err := db.QueryRow(ctx, `SELECT COALESCE(last_authenticated_at, linked_at) FROM plugin_auth_identities
		WHERE id = $1 AND user_id = $2`, identityID, userID).Scan(&anchor)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, errKeyIdentityGone
	}
	if err != nil {
		// As the session check reports a store it couldn't ask: the route
		// answers 503 with Retry-After, and the key is not judged.
		return time.Time{}, sessionCheckUnavailable("reading provider identity", err)
	}
	return anchor, nil
}

// keyProviderExpiry is the furthest a session the key opens may run, decided
// as Refresh decides it (providerExpiry) for a session whose provider chain
// starts at the identity's anchor: the provider is re-checked when due, and
// a chain it can't re-check ends refresh_token_expiry after the anchor. It
// runs before the sign-in's transaction, because a re-check takes the
// account lock in a transaction of its own.
//
// A chain past its end refuses the sign-in with ErrProviderSignInRequired:
// the person signs in through the provider again, which moves the anchor, and
// the key works again. A refusal on record is ErrNotPermitted, as an OAuth
// sign-in answers it. An outage under the fail-closed policy is
// ErrProviderUnavailable, and a store failure ErrSessionCheckUnavailable, as
// at refresh. A key that is not provider-bound runs the ordinary
// refresh_token_expiry. A provider-bound key that names no identity is
// refused with ErrProviderSignInRequired. One whose identity is gone from
// the account (unlinked, or its plugin uninstalled) answers
// errKeyIdentityGone: a key sign-in carries on to its transaction, which
// revokes the key under the account lock.
func (s *Service) keyProviderExpiry(ctx context.Context, userID int, binding KeyBinding) (time.Time, error) {
	now := time.Now()
	if !binding.ProviderBound {
		return now.Add(s.jwt.RefreshExpiry()), nil
	}
	if binding.IdentityID == nil {
		return time.Time{}, ErrProviderSignInRequired
	}
	anchor, err := identityAnchor(ctx, s.sessions.pool, userID, *binding.IdentityID)
	if err != nil {
		return time.Time{}, err
	}
	expiry, _, err := s.providerExpiry(ctx, &models.AuthSession{
		UserID: userID, IdentityID: binding.IdentityID, ProviderSince: &anchor, CreatedAt: now,
	}, now)
	switch {
	case errors.Is(err, errProviderChainEnded):
		return time.Time{}, ErrProviderSignInRequired
	case errors.Is(err, ErrSessionRevoked):
		return time.Time{}, ErrNotPermitted
	case errors.Is(err, errAnswerNotApplied):
		return time.Time{}, fmt.Errorf("%w: %w", ErrProviderCheckIncomplete, err)
	}
	return expiry, err
}

// keySignInPolicy decides whether a key may open a login session now, and
// answers the provider identity and provider_since the session continues
// (nil for none). It runs after the account row lock and the settings lock
// (lockChain) and before any key or session row is locked, from a binding
// read without a lock: the caller then locks the row and checks its binding
// is still the one decided here. That keeps the lock order every key path
// shares: the account, the settings lock, the installation, the identity,
// the person key, the device keys, the sessions. A plugin uninstall takes the
// installation, its identities and the sessions whose identity it clears,
// and never a key row or the settings lock.
//
// A provider-bound key inherits the provider checks of the session that
// enrolled it. Its identity is locked and checked as an OAuth sign-in checks
// it (the installation enabled, no refusal on record, and a network
// identity's primary provider not refusing the account), and the session is
// opened under it from the identity's anchor (identityAnchor), read under
// that lock. keyProviderExpiry has already applied the re-check and the age
// limit.
//
// A key that is not provider-bound is a local credential. While the server
// has local password sign-in off it works only for a break-glass account,
// exactly as a password would, and it is refused while a network provider
// refuses the account (localPasswordPolicy). The settings lock lockChain
// holds serializes it with a change of auth.local_password_login, as a
// password sign-in's does.
func keySignInPolicy(ctx context.Context, tx pgx.Tx, user *models.User, binding KeyBinding) (*int64, *time.Time, error) {
	if binding.ProviderBound {
		return lockKeyIdentity(ctx, tx, user, binding.IdentityID)
	}
	return nil, nil, localPasswordPolicy(ctx, tx, user)
}

// lockKeyIdentity locks and checks a provider-bound key's identity and
// answers it with its anchor, read under the lock.
func lockKeyIdentity(ctx context.Context, tx pgx.Tx, user *models.User, identityID *int64) (*int64, *time.Time, error) {
	if identityID == nil {
		return nil, nil, ErrProviderSignInRequired
	}
	installationID, err := lockOAuthIdentity(ctx, tx, user.ID, *identityID)
	if errors.Is(err, ErrNotPermitted) {
		// No identity of this account has that ID: unlinked, its plugin
		// uninstalled, or a number that names another account's identity.
		// Either way the key can never sign in again.
		exists, qerr := accountHasIdentity(ctx, tx, user.ID, *identityID)
		if qerr != nil {
			return nil, nil, sessionCheckUnavailable("reading provider identity", qerr)
		}
		if !exists {
			return nil, nil, errKeyIdentityGone
		}
	}
	if err != nil {
		return nil, nil, err
	}
	authority, err := primaryAuthorityOf(ctx, tx, user.ID, installationID)
	if err != nil {
		return nil, nil, err
	}
	if authority.refused {
		return nil, nil, ErrNotPermitted
	}
	anchor, err := identityAnchor(ctx, tx, user.ID, *identityID)
	if err != nil {
		return nil, nil, err
	}
	id := *identityID
	return &id, &anchor, nil
}

// errKeyIdentityGone is a provider-bound key whose identity no longer exists
// on its account. It is ErrNotPermitted to any caller that doesn't look for
// it; a key sign-in revokes the key and answers it as the revoked key it now
// is.
var errKeyIdentityGone = fmt.Errorf("%w: the key's provider identity was deleted", ErrNotPermitted)

// sessionIdentityGone answers a login session whose provider identity was
// deleted while it enrolled or replaced a key as the provider sign-in it now
// needs. The provider did not refuse the person, so it is not ErrNotPermitted.
func sessionIdentityGone(err error) error {
	if errors.Is(err, errKeyIdentityGone) {
		return ErrProviderSignInRequired
	}
	return err
}

// KeySession describes the login session a device key opens.
type KeySession struct {
	IP string
	// Expiry is keyProviderExpiry's answer for the key.
	Expiry time.Time
	// IdentityID and ProviderSince are keySignInPolicy's answer, decided
	// before the key row was locked.
	IdentityID    *int64
	ProviderSince *time.Time
}

// openKeySessionTx opens the login session of an active device key and
// mints its token pair. tx holds, in order, the account row lock (user is
// what it read), the provider locks keySignInPolicy took for in, and the
// key's row lock, so a revocation of the key either ends this session or
// happens after it is refused. The session is an ordinary account session
// that records the key (device_key_id), so revoking the key ends it.
func (s *Service) openKeySessionTx(ctx context.Context, tx pgx.Tx, user *models.User, key *models.AppDeviceKey, in KeySession) (*TokenPair, error) {
	if key.RevokedAt != nil || key.Status != models.DeviceKeyStatusActive {
		return nil, ErrKeySignatureInvalid
	}
	identityID, providerSince := in.IdentityID, in.ProviderSince
	// keyProviderExpiry decided before the request waited on the account
	// lock; a limit that passed meanwhile ends the sign-in, rather than
	// opening a session that is already over.
	if !in.Expiry.IsZero() && !in.Expiry.After(time.Now()) {
		return nil, ErrProviderSignInRequired
	}
	expiresAt := time.Now().Add(s.jwt.RefreshExpiry())
	if !in.Expiry.IsZero() && in.Expiry.Before(expiresAt) {
		expiresAt = in.Expiry
	}
	sessionID := uuid.New().String()
	keyID := key.ID
	session := models.AuthSession{
		ID:             sessionID,
		UserID:         user.ID,
		DeviceName:     key.Name,
		DevicePlatform: key.Platform,
		IPAddress:      in.IP,
		ExpiresAt:      expiresAt,
		IdentityID:     identityID,
		ProviderSince:  providerSince,
		DeviceKeyID:    &keyID,
		OpenedVia:      models.SessionOpenedAppKey,
	}
	if err := s.sessions.createWithQuerier(ctx, tx, session); err != nil {
		return nil, fmt.Errorf("creating key session: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_device_keys SET last_used_at = clock_timestamp() WHERE id = $1`, key.ID); err != nil {
		return nil, fmt.Errorf("recording device key use: %w", err)
	}
	pair, err := s.generateTokenPair(Claims{
		UserID:                 user.ID,
		Role:                   user.Role,
		SessionID:              sessionID,
		PasswordChangeRequired: user.PasswordChangeRequired,
	})
	if err != nil {
		return nil, err
	}
	pair.User = user
	return pair, nil
}
