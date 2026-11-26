package auth

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/clientip"
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

// SessionMayIssueCredential reports whether the claims' session may be
// handed a personal API key or a linked sign-in (SessionMayIssueCredential).
func (a *AppKeySignIn) SessionMayIssueCredential(ctx context.Context, claims *Claims) error {
	return SessionMayIssueCredential(ctx, a.pool, claims)
}

// ListOwnKeys lists the keys of the claims' account, for a session the
// person opened themselves (ActionReadKeys).
func (a *AppKeySignIn) ListOwnKeys(ctx context.Context, claims *Claims) (OwnAppKeys, error) {
	if err := SessionAllows(ctx, a.pool, claims, ActionReadKeys); err != nil {
		return OwnAppKeys{}, err
	}
	keys, err := a.keys.ListAccountKeys(ctx, claims.UserID)
	if err != nil {
		return OwnAppKeys{}, err
	}
	out := OwnAppKeys{Person: keys.Person}
	for _, key := range keys.Devices {
		// A denied key, and a waiting key past its lifetime (sign-in answers
		// it key_denied), are left out. Either can still be revoked by ID.
		expired := key.Status == models.DeviceKeyStatusPending && time.Since(key.CreatedAt) >= appkeys.PendingKeyLifetime
		if key.Status != models.DeviceKeyStatusDenied && !expired {
			out.Devices = append(out.Devices, key)
		}
	}
	// A grant's person key is always the account's own: the reference to
	// it includes the account.
	if err := a.pool.QueryRow(ctx, `SELECT COALESCE(username, '') FROM users WHERE id = $1`, claims.UserID).Scan(&out.AllowedBy); err != nil {
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
		return NotOwnSessionError(claims)
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
// with itself changes nothing. acting is the main profile the route checked,
// checked again under the account lock. The new key records the request's
// client address (clientip), the one a key sign-in's session records.
func (a *AppKeySignIn) ReplacePersonKey(ctx context.Context, claims *Claims, key appkeys.PublicKey, acting ActingProfile) error {
	if err := SessionAllows(ctx, a.pool, claims, ActionManageKeys); err != nil {
		return err
	}
	unlocked, err := sessionChainUnlocked(ctx, a.pool, claims.SessionID, claims.UserID)
	if err != nil {
		return err
	}
	var audit *auditmutation.Entry
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockEnabledUser(ctx, tx, claims.UserID)
		if err != nil {
			return err
		}
		if err := recheckActingProfile(ctx, tx, user.ID, acting); err != nil {
			return err
		}
		// The identity the new key records is locked before the session
		// row, in the order every key path takes.
		if unlocked.binding().ProviderBound {
			// A provider session whose identity is gone (identity_id NULL,
			// provider_since set) is refused as enroll refuses it: a person
			// key bound to no identity could never vouch for a device.
			if _, _, err := lockKeyIdentity(ctx, tx, user, unlocked.identityID); err != nil {
				return sessionIdentityGone(err)
			}
		}
		current, err := livePersonKeyTx(ctx, tx, user.ID)
		if err != nil {
			return err
		}
		before := map[string]any{}
		var denied int64
		if current != nil {
			if current.KeyID == key.KeyID() {
				return nil
			}
			if denied, err = retirePersonKeyTx(ctx, tx, user.ID, current.ID); err != nil {
				return err
			}
			before[auditFieldPersonKey] = current.KeyID
		}
		binding := unlocked.binding()
		if _, err := insertPersonKeyTx(ctx, tx, NewPersonKey{
			UserID: user.ID, PublicKey: key, IdentityID: binding.IdentityID,
			ProviderBound: binding.ProviderBound, EnrolledVia: models.AppKeyEnrolledSession, IP: clientip.FromContext(ctx),
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
		changes := auditmutation.Changes(before, map[string]any{auditFieldPersonKey: key.KeyID(), auditFieldSessionOpenedVia: chain.openedVia})
		if denied > 0 {
			changes = append(changes, auditmutation.Created(auditFieldDeniedDeviceKeys, denied)...)
		}
		audit, err = auditmutation.RecordMutation(ctx, tx, auditmutation.ActionPersonKeyReplaced, auditmutation.TargetUser,
			strconv.Itoa(user.ID), http.StatusOK, changes)
		return err
	})
	if err != nil {
		return err
	}
	auditmutation.CommitMutation(ctx, audit)
	return nil
}

// retirePersonKeyTx revokes a person key that is being replaced. Replacing
// the key is how the person cuts off a copy of it, and a device still
// waiting for approval may be the copy's work: it is refused with the key
// that vouched for it. Devices already approved keep their keys. tx holds
// the account row lock and the key's row lock. It returns how many waiting
// devices it denied.
func retirePersonKeyTx(ctx context.Context, tx pgx.Tx, userID int, id string) (int64, error) {
	if _, err := tx.Exec(ctx, `UPDATE auth_person_keys SET revoked_at = NOW() WHERE id = $1`, id); err != nil {
		return 0, fmt.Errorf("revoking person key: %w", err)
	}
	return denyIntroducedWaitingKeysTx(ctx, tx, userID, id)
}
