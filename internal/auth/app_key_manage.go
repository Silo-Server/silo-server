package auth

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/models"
)

// OwnAppKeys is the key list a person sees for their own account.
type OwnAppKeys struct {
	// Person is the live person key, or nil.
	Person *models.AppPersonKey
	// Devices are the live device keys that are active or waiting, newest
	// first. A denied key no longer appears.
	Devices []*models.AppDeviceKey
	// AllowedBy is the account name shown beside a key a grant enrolled:
	// the person whose person key signed the grant.
	AllowedBy string
}

// ListOwnKeys lists the keys of the claims' account, for the account's own
// login session.
func (a *AppKeySignIn) ListOwnKeys(ctx context.Context, claims *Claims) (OwnAppKeys, error) {
	if !claims.IsOwnLoginSession() {
		return OwnAppKeys{}, ErrSessionNotAllowed
	}
	keys, err := a.keys.ListAccountKeys(ctx, claims.UserID)
	if err != nil {
		return OwnAppKeys{}, err
	}
	out := OwnAppKeys{Person: keys.Person}
	for _, key := range keys.Devices {
		if key.Status != models.DeviceKeyStatusDenied {
			out.Devices = append(out.Devices, key)
		}
	}
	// A grant's person key is always the account's own: the reference to
	// it includes the account.
	if err := a.pool.QueryRow(ctx, `SELECT username FROM users WHERE id = $1`, claims.UserID).Scan(&out.AllowedBy); err != nil {
		return OwnAppKeys{}, fmt.Errorf("reading account name: %w", err)
	}
	return out, nil
}

// RevokeOwnDeviceKey revokes one of the account's device keys from the
// account's own login session, ending every session the key opened. A
// device may revoke its own key, which ends the session asking. A session
// from a grant-enrolled app key may revoke only that key; a session opened
// by device sign-in may revoke none.
func (a *AppKeySignIn) RevokeOwnDeviceKey(ctx context.Context, claims *Claims, id string) error {
	if !claims.IsOwnLoginSession() {
		return ErrSessionNotAllowed
	}
	origin, err := ReadSessionOrigin(ctx, a.pool, claims)
	if err != nil {
		return err
	}
	if origin.GrantKey {
		if id != origin.DeviceKeyID {
			return ErrSessionNotAllowed
		}
	} else if !origin.Allows(ActionManageKeys) {
		return ErrSessionNotAllowed
	}
	return a.keys.RevokeDeviceKey(ctx, claims.UserID, id)
}

// ReplacePersonKey makes key the account's person key, from the account's
// own login session, and revokes the one it replaces. Device keys already
// enrolled keep working; new devices need the new key. Replacing the key
// with itself changes nothing.
func (a *AppKeySignIn) ReplacePersonKey(ctx context.Context, claims *Claims, key appkeys.PublicKey) error {
	if err := SessionAllows(ctx, a.pool, claims, ActionManageKeys); err != nil {
		return err
	}
	unlocked, err := sessionChainUnlocked(ctx, a.pool, claims.SessionID, claims.UserID)
	if err != nil {
		return err
	}
	return a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockEnabledUser(ctx, tx, claims.UserID)
		if err != nil {
			return err
		}
		// The identity the new key records is locked before the session
		// row, in the order every key path takes.
		if unlocked.identityID != nil {
			if _, _, err := lockKeyIdentity(ctx, tx, user, unlocked.identityID); err != nil {
				return err
			}
		}
		current, err := livePersonKeyTx(ctx, tx, user.ID)
		if err != nil {
			return err
		}
		if current != nil {
			if current.KeyID == key.KeyID() {
				return nil
			}
			if _, err := tx.Exec(ctx, `UPDATE auth_person_keys SET revoked_at = NOW() WHERE id = $1`, current.ID); err != nil {
				return fmt.Errorf("revoking person key: %w", err)
			}
		}
		binding := unlocked.binding()
		if _, err := insertPersonKeyTx(ctx, tx, NewPersonKey{
			UserID: user.ID, PublicKey: key, IdentityID: binding.IdentityID,
			ProviderBound: binding.ProviderBound, EnrolledVia: models.AppKeyEnrolledSession,
		}); err != nil {
			return err
		}
		// The session row last, after the key rows; a session that moved
		// under another identity or ended meanwhile rolls everything back.
		chain, err := sessionProviderChainOf(ctx, tx, claims.SessionID, user.ID)
		if err != nil {
			return err
		}
		if !chain.binding().same(binding) {
			return ErrSessionRevoked
		}
		return nil
	})
}
