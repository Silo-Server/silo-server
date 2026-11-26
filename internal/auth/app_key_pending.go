package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/models"
)

// MaxPendingKeys caps the device keys an account may have waiting for
// approval inside their appkeys.PendingKeyLifetime. Anyone who holds an
// administrator's person key can introduce new devices; each leaves a waiting
// row, so the count is bounded.
const MaxPendingKeys = 10

var (
	// ErrPendingKeyLimit refuses an introduce that would leave the account
	// with more than MaxPendingKeys waiting keys.
	ErrPendingKeyLimit = errors.New("too many devices waiting for approval")
	// ErrApproverNotAdmin refuses approving a waiting key on an account that
	// is no longer an administrator: only an administrator's devices wait.
	ErrApproverNotAdmin = errors.New("only an administrator account approves a waiting device")
	// ErrKeyDecidesItself is ErrSessionNotAllowed for a session a device key
	// opened that approves or refuses that same key. errors.Is matches both;
	// a caller that tells the person what to do next checks this one first,
	// since that session already is the person's own app.
	ErrKeyDecidesItself = fmt.Errorf("%w: a key cannot approve or refuse itself", ErrSessionNotAllowed)
)

// auditFieldDeviceKey names the device key a pending key decision acted on.
const auditFieldDeviceKey = "device_key_id"

// pendingDecisionAllowed refuses a waiting-key operation from a session
// action does not allow (SessionAllows), and from the session the key itself
// opened. A waiting key has opened none, so the second check only matters
// for a key that is already active.
func (a *AppKeySignIn) pendingDecisionAllowed(ctx context.Context, claims *Claims, action SessionAction, id string) error {
	origin, err := allowedSessionOrigin(ctx, a.pool, claims, action)
	if err != nil {
		return err
	}
	if origin.DeviceKeyID != "" && origin.DeviceKeyID == id {
		return ErrKeyDecidesItself
	}
	return nil
}

// ListPendingKeys lists the account's device keys that wait for approval and
// are inside their lifetime, oldest first.
func (a *AppKeySignIn) ListPendingKeys(ctx context.Context, claims *Claims) ([]*models.AppDeviceKey, error) {
	if err := a.pendingDecisionAllowed(ctx, claims, ActionReadKeys, ""); err != nil {
		return nil, err
	}
	rows, err := a.pool.Query(ctx, `SELECT `+deviceKeyColumns+` FROM auth_device_keys k
		WHERE user_id = $1 AND status = $2 AND revoked_at IS NULL AND created_at > clock_timestamp() - make_interval(secs => $3)
			AND `+identityLive+` ORDER BY created_at, id`,
		claims.UserID, models.DeviceKeyStatusPending, appkeys.PendingKeyLifetime.Seconds())
	if err != nil {
		return nil, fmt.Errorf("listing waiting device keys: %w", err)
	}
	defer rows.Close()
	var out []*models.AppDeviceKey
	for rows.Next() {
		key, err := scanDeviceKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing waiting device keys: %w", err)
	}
	return out, nil
}

// ApprovePendingKey makes one of the account's waiting device keys active,
// so the device's next sign-in opens a session. It locks the account, then
// the key, then the approving session, and re-checks each under its lock: the
// account is still an enabled administrator, the key is still waiting inside
// its lifetime, and the session is still live. Approving an active key
// changes nothing; a key that is unknown, denied, revoked or past its
// lifetime is ErrAppKeyNotFound. acting is the main profile the route
// checked, checked again under the account lock.
func (a *AppKeySignIn) ApprovePendingKey(ctx context.Context, claims *Claims, id string, acting ActingProfile) error {
	if err := a.pendingDecisionAllowed(ctx, claims, ActionManageKeys, id); err != nil {
		return err
	}
	var audit *auditmutation.Entry
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		user, err := lockEnabledUser(ctx, tx, claims.UserID)
		if err != nil {
			return err
		}
		if err := recheckActingProfile(ctx, tx, user.ID, acting); err != nil {
			return err
		}
		if user.Role != models.RoleAdmin {
			return ErrApproverNotAdmin
		}
		key, err := lockDeviceKey(ctx, tx, user.ID, id)
		if err != nil {
			return err
		}
		if key.RevokedAt != nil {
			return ErrAppKeyNotFound
		}
		switch key.Status {
		case models.DeviceKeyStatusActive:
			return nil
		case models.DeviceKeyStatusPending:
		default:
			return ErrAppKeyNotFound
		}
		if expired, err := pendingKeyExpired(ctx, tx, key); err != nil || expired {
			if err == nil {
				err = ErrAppKeyNotFound
			}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE auth_device_keys SET status = $2 WHERE id = $1`, key.ID, models.DeviceKeyStatusActive); err != nil {
			return fmt.Errorf("approving device key: %w", err)
		}
		// The approving session's row last: one that ended meanwhile rolls
		// the approval back.
		if _, err := sessionProviderChainOf(ctx, tx, claims.SessionID, user.ID); err != nil {
			return err
		}
		audit, err = auditmutation.RecordMutation(ctx, tx, auditmutation.ActionDeviceKeyApproved, auditmutation.TargetUser,
			strconv.Itoa(user.ID), http.StatusNoContent, auditmutation.Created(auditFieldDeviceKey, key.ID))
		return err
	})
	if err != nil {
		return err
	}
	auditmutation.CommitMutation(ctx, audit)
	return nil
}

// DenyPendingKey refuses one of the account's waiting device keys: its next
// sign-in answers ErrAppKeyDenied and neither key list shows it. Denying a
// denied key changes nothing; an active, revoked or unknown key is
// ErrAppKeyNotFound.
func (a *AppKeySignIn) DenyPendingKey(ctx context.Context, claims *Claims, id string) error {
	if err := a.pendingDecisionAllowed(ctx, claims, ActionReadKeys, id); err != nil {
		return err
	}
	var audit *auditmutation.Entry
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := lockUser(ctx, tx, claims.UserID); err != nil {
			return err
		}
		var err error
		audit, err = denyPendingKeyTx(ctx, tx, claims.UserID, id)
		return err
	})
	if err != nil {
		return err
	}
	auditmutation.CommitMutation(ctx, audit)
	return nil
}

// denyPendingKeyTx denies one of the account's waiting device keys in tx,
// which holds the account row lock, and records the audit row. It returns no
// row for a key that was denied already; an active, revoked or unknown key is
// ErrAppKeyNotFound.
func denyPendingKeyTx(ctx context.Context, tx pgx.Tx, userID int, id string) (*auditmutation.Entry, error) {
	key, err := lockDeviceKey(ctx, tx, userID, id)
	if err != nil {
		return nil, err
	}
	if key.RevokedAt != nil {
		return nil, ErrAppKeyNotFound
	}
	switch key.Status {
	case models.DeviceKeyStatusDenied:
		return nil, nil
	case models.DeviceKeyStatusPending:
	default:
		return nil, ErrAppKeyNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_device_keys SET status = $2 WHERE id = $1`, key.ID, models.DeviceKeyStatusDenied); err != nil {
		return nil, fmt.Errorf("denying device key: %w", err)
	}
	return auditmutation.RecordMutation(ctx, tx, auditmutation.ActionDeviceKeyDenied, auditmutation.TargetUser,
		strconv.Itoa(userID), http.StatusNoContent, auditmutation.Created(auditFieldDeviceKey, key.ID))
}

// pendingKeyExpired reports whether a waiting key has outlived
// appkeys.PendingKeyLifetime, on the database's wall clock (keyStatusAllows).
func pendingKeyExpired(ctx context.Context, db rowQuerier, key *models.AppDeviceKey) (bool, error) {
	var now time.Time
	if err := db.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return false, fmt.Errorf("reading the clock: %w", err)
	}
	return now.Sub(key.CreatedAt) >= appkeys.PendingKeyLifetime, nil
}

// checkPendingKeyRoom refuses another waiting key once the account has
// MaxPendingKeys inside their lifetime. tx holds the account row lock, which
// serializes this count with the insert that follows it.
func checkPendingKeyRoom(ctx context.Context, tx pgx.Tx, userID int) error {
	var waiting int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM auth_device_keys
		WHERE user_id = $1 AND status = $2 AND revoked_at IS NULL AND created_at > clock_timestamp() - make_interval(secs => $3)`,
		userID, models.DeviceKeyStatusPending, appkeys.PendingKeyLifetime.Seconds()).Scan(&waiting); err != nil {
		return fmt.Errorf("counting waiting device keys: %w", err)
	}
	if waiting >= MaxPendingKeys {
		return ErrPendingKeyLimit
	}
	return nil
}

// RevokeExpiredPendingKeys revokes waiting device keys past their lifetime,
// for the retention step. The rows stay, so a public key is still enrolled
// only once; a waiting key never opened a session, so none ends. Each account
// is handled in its own transaction under its account lock, the order every
// key path takes.
func (r *AppKeyRepository) RevokeExpiredPendingKeys(ctx context.Context) (int, error) {
	lifetime := appkeys.PendingKeyLifetime.Seconds()
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT user_id FROM auth_device_keys
		WHERE status = $1 AND revoked_at IS NULL AND created_at <= clock_timestamp() - make_interval(secs => $2)`,
		models.DeviceKeyStatusPending, lifetime)
	if err != nil {
		return 0, fmt.Errorf("finding expired waiting device keys: %w", err)
	}
	var users []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("finding expired waiting device keys: %w", err)
		}
		users = append(users, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("finding expired waiting device keys: %w", err)
	}
	revoked := 0
	for _, userID := range users {
		err := r.inAccountTx(ctx, userID, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE auth_device_keys SET revoked_at = clock_timestamp()
				WHERE user_id = $1 AND status = $2 AND revoked_at IS NULL AND created_at <= clock_timestamp() - make_interval(secs => $3)`,
				userID, models.DeviceKeyStatusPending, lifetime)
			if err != nil {
				return fmt.Errorf("revoking expired waiting device keys: %w", err)
			}
			revoked += int(tag.RowsAffected())
			return nil
		})
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return revoked, err
		}
	}
	return revoked, nil
}
