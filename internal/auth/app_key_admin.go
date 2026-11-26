package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Audit fields of the administrator key operations.
const (
	auditFieldPersonKey        = "person_key_kid"
	auditFieldDeniedDeviceKeys = "denied_device_keys"
)

// AdminPersonKey is an account's person key as an administrator sees it.
type AdminPersonKey struct {
	*models.AppPersonKey
	// IdentityGone marks a provider-bound key whose identity no longer
	// exists on the account: it can't vouch for a device again.
	IdentityGone bool
}

// AdminDeviceKey is one of an account's device keys as an administrator
// sees it.
type AdminDeviceKey struct {
	*models.AppDeviceKey
	// IdentityGone marks a provider-bound key whose identity no longer
	// exists on the account: it can't sign in again and is revoked at its
	// next try. The person's own list leaves such a key out.
	IdentityGone bool
}

// AdminAccountKeys is the key list an administrator sees for an account.
type AdminAccountKeys struct {
	// Person is the account's unrevoked person key, or nil.
	Person *AdminPersonKey
	// Devices are the unrevoked device keys that are active or waiting
	// inside their lifetime, dead ones included, newest first.
	Devices []AdminDeviceKey
	// AllowedBy is the account name shown beside a key a grant enrolled
	// (OwnAppKeys.AllowedBy).
	AllowedBy string
}

// identityGoneSQL is the negation of identityLive for a key row k.
const identityGoneSQL = `NOT ` + identityLive

// AdminListKeys lists an account's keys for an administrator: what the
// person's own list shows, plus keys whose provider identity is gone, marked
// as such. ErrNotFound when the account does not exist.
func (a *AppKeySignIn) AdminListKeys(ctx context.Context, userID int) (AdminAccountKeys, error) {
	var out AdminAccountKeys
	err := a.pool.QueryRow(ctx, `SELECT COALESCE(username, '') FROM users WHERE id = $1`, userID).Scan(&out.AllowedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminAccountKeys{}, ErrNotFound
	}
	if err != nil {
		return AdminAccountKeys{}, fmt.Errorf("reading account: %w", err)
	}
	var gone bool
	person, err := scanPersonKey(rowWithFlag{a.pool.QueryRow(ctx, `SELECT `+personKeyColumns+`, `+identityGoneSQL+` FROM auth_person_keys k
		WHERE user_id = $1 AND revoked_at IS NULL`, userID), &gone})
	switch {
	case err == nil:
		out.Person = &AdminPersonKey{AppPersonKey: person, IdentityGone: gone}
	case !errors.Is(err, ErrAppKeyNotFound):
		return AdminAccountKeys{}, err
	}
	// A denied key and a waiting key past its lifetime are left out, as
	// from the person's own list.
	rows, err := a.pool.Query(ctx, `SELECT `+deviceKeyColumns+`, `+identityGoneSQL+` FROM auth_device_keys k
		WHERE user_id = $1 AND revoked_at IS NULL
			AND (status = $2 OR (status = $3 AND created_at > clock_timestamp() - make_interval(secs => $4)))
		ORDER BY created_at DESC, id DESC`,
		userID, models.DeviceKeyStatusActive, models.DeviceKeyStatusPending, appkeys.PendingKeyLifetime.Seconds())
	if err != nil {
		return AdminAccountKeys{}, fmt.Errorf("listing device keys: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var dead bool
		key, err := scanDeviceKey(rowWithFlag{rows, &dead})
		if err != nil {
			return AdminAccountKeys{}, err
		}
		out.Devices = append(out.Devices, AdminDeviceKey{AppDeviceKey: key, IdentityGone: dead})
	}
	if err := rows.Err(); err != nil {
		return AdminAccountKeys{}, fmt.Errorf("listing device keys: %w", err)
	}
	return out, nil
}

// rowWithFlag scans a key row followed by one boolean column into flag.
type rowWithFlag struct {
	row  pgx.Row
	flag *bool
}

func (r rowWithFlag) Scan(dest ...any) error {
	return r.row.Scan(append(dest, r.flag)...)
}

// AdminRevokeDeviceKey revokes one of userID's device keys as actorID, an
// enabled administrator, under the Owner rules (lockAdminActorAndTarget),
// and ends every session the key opened. Revoking a revoked key changes
// nothing and records nothing.
func (a *AppKeySignIn) AdminRevokeDeviceKey(ctx context.Context, actorID, userID int, id string) error {
	return a.adminKeyTx(ctx, actorID, userID, func(tx pgx.Tx) (*auditmutation.Entry, error) {
		key, err := lockDeviceKey(ctx, tx, userID, id)
		if err != nil {
			return nil, err
		}
		if key.RevokedAt != nil {
			return nil, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE auth_device_keys SET revoked_at = NOW() WHERE id = $1`, key.ID); err != nil {
			return nil, fmt.Errorf("revoking device key: %w", err)
		}
		if err := endDeviceKeySessions(ctx, tx, userID, []string{key.ID}); err != nil {
			return nil, err
		}
		return auditmutation.RecordMutation(ctx, tx, auditmutation.ActionDeviceKeyRevoked, auditmutation.TargetUser,
			strconv.Itoa(userID), http.StatusNoContent, auditmutation.Removed(auditFieldDeviceKey, key.ID))
	})
}

// AdminRevokePersonKey revokes userID's person key as actorID, under the
// Owner rules. Devices already enrolled keep their keys and sessions; no new
// device can be introduced with it, and the devices it introduced that are
// still waiting for approval are denied, as replacing the key denies them.
// An account with no unrevoked person key changes nothing and records
// nothing, so a repeat succeeds; a missing account is ErrNotFound.
func (a *AppKeySignIn) AdminRevokePersonKey(ctx context.Context, actorID, userID int) error {
	return a.adminKeyTx(ctx, actorID, userID, func(tx pgx.Tx) (*auditmutation.Entry, error) {
		person, err := scanPersonKey(tx.QueryRow(ctx, `SELECT `+personKeyColumns+` FROM auth_person_keys
			WHERE user_id = $1 AND revoked_at IS NULL FOR UPDATE`, userID))
		if errors.Is(err, ErrAppKeyNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if err := revokePersonKeyTx(ctx, tx, userID, person.ID); err != nil {
			return nil, err
		}
		denied, err := denyIntroducedWaitingKeysTx(ctx, tx, userID, person.ID)
		if err != nil {
			return nil, err
		}
		changes := auditmutation.Removed(auditFieldPersonKey, person.KeyID)
		if denied > 0 {
			changes = append(changes, auditmutation.Created(auditFieldDeniedDeviceKeys, denied)...)
		}
		return auditmutation.RecordMutation(ctx, tx, auditmutation.ActionPersonKeyRevoked, auditmutation.TargetUser,
			strconv.Itoa(userID), http.StatusNoContent, changes)
	})
}

// AdminDenyPendingKey refuses one of userID's waiting device keys as
// actorID, under the Owner rules. It answers as DenyPendingKey does. No
// administrator route approves a waiting key: only the person does, from
// one of their own sessions.
func (a *AppKeySignIn) AdminDenyPendingKey(ctx context.Context, actorID, userID int, id string) error {
	return a.adminKeyTx(ctx, actorID, userID, func(tx pgx.Tx) (*auditmutation.Entry, error) {
		return denyPendingKeyTx(ctx, tx, userID, id)
	})
}

// adminKeyTx runs fn in a transaction that holds the actor's and the target
// account's row locks (lockAdminActorAndTarget), then commits and publishes
// the audit row fn recorded, if any.
func (a *AppKeySignIn) adminKeyTx(ctx context.Context, actorID, userID int, fn func(pgx.Tx) (*auditmutation.Entry, error)) error {
	var audit *auditmutation.Entry
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		if err := lockAdminActorAndTarget(ctx, tx, actorID, userID); err != nil {
			return err
		}
		var err error
		audit, err = fn(tx)
		return err
	})
	if err != nil {
		return err
	}
	auditmutation.CommitMutation(ctx, audit)
	return nil
}

// denyIntroducedWaitingKeysTx denies the account's device keys that
// personKeyID introduced and that still wait for approval, in tx, which holds
// the account row lock and has revoked the person key. A waiting key may be
// the work of whoever copied the person key.
func denyIntroducedWaitingKeysTx(ctx context.Context, tx pgx.Tx, userID int, personKeyID string) (int64, error) {
	tag, err := tx.Exec(ctx, `UPDATE auth_device_keys SET status = $3
		WHERE user_id = $1 AND person_key_id = $2 AND status = $4 AND revoked_at IS NULL`,
		userID, personKeyID, models.DeviceKeyStatusDenied, models.DeviceKeyStatusPending)
	if err != nil {
		return 0, fmt.Errorf("denying the person key's waiting devices: %w", err)
	}
	return tag.RowsAffected(), nil
}
