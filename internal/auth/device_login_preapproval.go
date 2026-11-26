package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
)

// A device sign-in approved in advance: the account's own session approves a
// sign-in for a device that is not running yet, bound to a fingerprint of a
// secret the device keeps. The device later redeems it with the ordinary
// poll, sending the device code and that secret. See "Approval in advance"
// in docs/architecture/device-login.md.

const (
	// DevicePreapprovalTTL is how long a pre-approved sign-in waits.
	DevicePreapprovalTTL = 30 * 24 * time.Hour
	// MaxDevicePreapprovals caps an account's waiting pre-approved sign-ins.
	MaxDevicePreapprovals = 20
)

var (
	// ErrDevicePreapprovalLimit refuses a pre-approval while the account
	// already has MaxDevicePreapprovals waiting.
	ErrDevicePreapprovalLimit = errors.New("too many waiting pre-approved device sign-ins")
	// ErrDevicePreapprovalUnbound refuses a pre-approval without a valid
	// binding: only the device holding the secret may redeem it.
	ErrDevicePreapprovalUnbound = errors.New("a pre-approved device sign-in needs a binding")
)

// DevicePreapprovalAttemptsError refuses a poll of a pre-approved request
// whose device code has had too many wrong binding secrets recently.
type DevicePreapprovalAttemptsError struct {
	RetryAfter time.Duration
}

func (e *DevicePreapprovalAttemptsError) Error() string {
	return "too many wrong binding secrets for this device sign-in"
}

// AttemptCounter counts attempts at a secret per key, as AttemptReserver
// does, and clears a key's count after a correct one
// (ratelimit.AttemptLimiter).
type AttemptCounter interface {
	AttemptReserver
	Reset(ctx context.Context, key string)
}

// WithPreapprovalAttempts sets the per-request limiter on wrong binding
// secrets for pre-approved requests and returns s.
func (s *DeviceLoginService) WithPreapprovalAttempts(l AttemptCounter) *DeviceLoginService {
	s.preapprovalAttempts = l
	return s
}

// DevicePreapprovalInput names the device a sign-in is approved for.
type DevicePreapprovalInput struct {
	DeviceName     string
	DevicePlatform string
	// Binding is the device's fingerprint, as for a bound start.
	Binding string
	// Acting is the main profile the route checked, checked again under
	// the account lock.
	Acting ActingProfile
}

// DevicePreapprovalResult is what the approver hands to the device.
type DevicePreapprovalResult struct {
	ID         string
	DeviceCode string
	ExpiresAt  time.Time
}

// DevicePreapproval is a waiting pre-approved sign-in as its account lists
// it. It never carries the device code or the binding.
type DevicePreapproval struct {
	ID             string
	DeviceName     string
	DevicePlatform string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// A pre-approved request records the login session that approved it
// (preapproved_session_id). The database withdraws it when that session is
// revoked: the trigger auth_sessions_withdraw_preapprovals fires on every
// write that revokes a login session, whichever code path or release made
// it, so no revocation here has to remember to. Preapprove holds the
// session's row lock while it inserts, and a revocation takes that lock
// first, so neither can miss the other.

// Preapprove approves a sign-in in advance for the device holding the secret
// behind input.Binding, as approverUserID's account. The request claims in
// ctx must be that account's own login session; the account and session are
// locked as for an ordinary approval, and the device's session will continue
// the session's provider identity. The browser and user codes are random and
// never returned, so the request is reachable only by its device code.
func (s *DeviceLoginService) Preapprove(ctx context.Context, input DevicePreapprovalInput, approverUserID int) (*DevicePreapprovalResult, error) {
	if !validDeviceLoginBinding(input.Binding) {
		return nil, ErrDevicePreapprovalUnbound
	}
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		// Unlike Approve, there is no trusted internal caller.
		return nil, ErrSessionRevoked
	}
	deviceCode, err := randomToken(deviceCodeBytes)
	if err != nil {
		return nil, fmt.Errorf("generate device code: %w", err)
	}
	browserCode, err := randomToken(browserCodeBytes)
	if err != nil {
		return nil, fmt.Errorf("generate browser code: %w", err)
	}
	userCode, err := randomToken(deviceCodeBytes)
	if err != nil {
		return nil, fmt.Errorf("generate user code: %w", err)
	}
	matchCode, err := randomMatchCode()
	if err != nil {
		return nil, fmt.Errorf("generate match code: %w", err)
	}
	now := s.now().UTC()
	result := &DevicePreapprovalResult{ID: uuid.New().String(), DeviceCode: deviceCode, ExpiresAt: now.Add(DevicePreapprovalTTL)}
	deviceName := fallbackDeviceName(input.DeviceName, "")
	var audit *auditmutation.Entry
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		chain, err := s.approvingProviderChain(ctx, tx, approverUserID)
		if err != nil {
			return err
		}
		if err := recheckActingProfile(ctx, tx, approverUserID, input.Acting); err != nil {
			return err
		}
		// A sign-in approved in advance outlives the session that made it,
		// so it needs a session that may hand out lasting access: not one a
		// device sign-in or another app's key opened.
		if err := SessionAllows(ctx, tx, claims, ActionManageKeys); err != nil {
			return err
		}
		if chain.identityID == nil && chain.providerSince != nil {
			// The session's provider identity is gone, so redemption would
			// refuse this request.
			return ErrNotPermitted
		}
		// The account lock taken above serializes this count with the insert.
		var waiting int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM device_login_requests
			WHERE approved_by_user_id = $1 AND preapproved_at IS NOT NULL AND status = $2 AND expires_at > $3`,
			approverUserID, DeviceLoginStatusApproved, now).Scan(&waiting); err != nil {
			return fmt.Errorf("count device pre-approvals: %w", err)
		}
		if waiting >= MaxDevicePreapprovals {
			return ErrDevicePreapprovalLimit
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO device_login_requests (
				id, device_code_hash, browser_code_hash, user_code_hash, match_code,
				device_name, device_platform, ip_address, requested_user_agent,
				status, approved_by_user_id, approved_at, approved_identity_id, approved_provider_since,
				client_purpose, temporary, expires_at, created_at, updated_at, binding_hash, preapproved_at,
				preapproved_session_id
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, NULL, '', $8, $9, $10, $11, $12, $13, FALSE, $14, $10, $10, $15, $10, $16)`,
			result.ID,
			hashDeviceLoginSecret(deviceCode),
			hashDeviceLoginSecret(browserCode),
			hashDeviceLoginSecret(userCode),
			matchCode,
			deviceName,
			trimDeviceField(input.DevicePlatform, maxDevicePlatformLen),
			DeviceLoginStatusApproved,
			approverUserID,
			now,
			chain.identityID,
			chain.providerSince,
			DeviceLoginPurposeLogin,
			result.ExpiresAt,
			input.Binding,
			claims.SessionID,
		)
		if err != nil {
			return fmt.Errorf("create device pre-approval: %w", err)
		}
		// The request's ID and device name only: never its device code or
		// binding.
		audit, err = auditmutation.RecordMutation(ctx, tx, auditmutation.ActionDeviceSignInPreapproved, auditmutation.TargetUser,
			strconv.Itoa(approverUserID), http.StatusCreated, auditmutation.Changes(nil, map[string]any{
				auditFieldDeviceLoginRequest: result.ID, auditFieldDeviceName: deviceName,
				auditFieldExpiresAt: auditInstant(result.ExpiresAt),
			}))
		return err
	})
	if err != nil {
		return nil, err
	}
	auditmutation.CommitMutation(ctx, audit)
	return result, nil
}

// ListPreapprovals lists userID's waiting pre-approved sign-ins, newest
// first, for the account's own login session in ctx. The list names the
// devices the person approved, so it takes a session that may read the
// account's keys (ActionReadKeys): not one a device sign-in or another app's
// key opened. The cap keeps the list short, so it is not paged.
func (s *DeviceLoginService) ListPreapprovals(ctx context.Context, userID int) ([]DevicePreapproval, error) {
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		return nil, ErrSessionRevoked
	}
	if claims.UserID != userID {
		return nil, ErrSessionNotAllowed
	}
	if err := SessionAllows(ctx, s.pool, claims, ActionReadKeys); err != nil {
		return nil, err
	}
	return s.listPreapprovals(ctx, userID)
}

// listPreapprovals lists userID's waiting pre-approved sign-ins, newest
// first, with no check on the caller.
func (s *DeviceLoginService) listPreapprovals(ctx context.Context, userID int) ([]DevicePreapproval, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, device_name, device_platform, created_at, expires_at FROM device_login_requests
		WHERE approved_by_user_id = $1 AND preapproved_at IS NOT NULL AND status = $2 AND expires_at > $3
		ORDER BY created_at DESC, id`, userID, DeviceLoginStatusApproved, s.now().UTC())
	if err != nil {
		return nil, fmt.Errorf("list device pre-approvals: %w", err)
	}
	defer rows.Close()
	out := []DevicePreapproval{}
	for rows.Next() {
		var p DevicePreapproval
		if err := rows.Scan(&p.ID, &p.DeviceName, &p.DevicePlatform, &p.CreatedAt, &p.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan device pre-approval: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list device pre-approvals: %w", err)
	}
	return out, nil
}

// RevokePreapproval withdraws one of userID's waiting pre-approved sign-ins,
// for the account's own login session in ctx: it moves to denied, and the
// device's next poll is told so. Withdrawing changes what the account's
// devices may do, so it takes a session that may manage the account's keys
// (ActionManageKeys), as approving in advance does. Revoking one already
// withdrawn succeeds; any other id, a redeemed or expired one included, is
// ErrDeviceLoginNotFound.
func (s *DeviceLoginService) RevokePreapproval(ctx context.Context, userID int, id string) error {
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		return ErrSessionRevoked
	}
	if claims.UserID != userID {
		return ErrSessionNotAllowed
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockUser(ctx, tx, userID); err != nil {
			return err
		}
		if err := SessionAllows(ctx, tx, claims, ActionManageKeys); err != nil {
			return err
		}
		return s.revokePreapprovalTx(ctx, tx, userID, id)
	})
}

// AdminListPreapprovals lists userID's waiting pre-approved sign-ins for an
// administrator.
func (s *DeviceLoginService) AdminListPreapprovals(ctx context.Context, userID int) ([]DevicePreapproval, error) {
	if _, err := s.users.GetByID(ctx, userID); err != nil {
		return nil, err
	}
	return s.listPreapprovals(ctx, userID)
}

// AdminRevokePreapproval revokes one of userID's pre-approved sign-ins as
// actorID, an enabled administrator, under the owner protection that
// revoking the account's login sessions has (CheckOwnerTarget).
func (s *DeviceLoginService) AdminRevokePreapproval(ctx context.Context, actorID, userID int, id string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockAdminActorAndTarget(ctx, tx, actorID, userID); err != nil {
			return err
		}
		return s.revokePreapprovalTx(ctx, tx, userID, id)
	})
}

func (s *DeviceLoginService) revokePreapprovalTx(ctx context.Context, tx pgx.Tx, userID int, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrDeviceLoginNotFound
	}
	var status string
	var expiresAt time.Time
	err := tx.QueryRow(ctx, `SELECT status, expires_at FROM device_login_requests
		WHERE id = $1 AND approved_by_user_id = $2 AND preapproved_at IS NOT NULL FOR UPDATE`, id, userID).Scan(&status, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDeviceLoginNotFound
	}
	if err != nil {
		return fmt.Errorf("read device pre-approval: %w", err)
	}
	switch {
	case status == DeviceLoginStatusDenied:
		return nil
	case status != DeviceLoginStatusApproved || !expiresAt.After(s.now()):
		return ErrDeviceLoginNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE device_login_requests SET status = $2, denied_at = $3, updated_at = $3 WHERE id = $1`,
		id, DeviceLoginStatusDenied, s.now().UTC()); err != nil {
		return fmt.Errorf("revoke device pre-approval: %w", err)
	}
	return nil
}

// checkPollBinding checks a poll's binding secret. The right secret always
// passes. For a pre-approved request each wrong secret spends one of the
// request's own attempts, keyed by its device code hash, and past the budget
// a wrong secret is answered with the lock instead of a mismatch. The budget
// never refuses the right secret and wrong secrets never change the row, so
// someone who learned the device code can neither lock the device out nor
// destroy its sign-in.
func (s *DeviceLoginService) checkPollBinding(ctx context.Context, record *deviceLoginRecord, secret string) error {
	matches := deviceLoginBindingMatches(record.BindingHash, secret)
	if record.PreapprovedAt == nil || s.preapprovalAttempts == nil {
		if !matches {
			return ErrDeviceLoginBindingMismatch
		}
		return nil
	}
	key := "device_preapproval:" + record.DeviceCodeHash
	if matches {
		s.preapprovalAttempts.Reset(ctx, key)
		return nil
	}
	if retryAfter, ok := s.preapprovalAttempts.Reserve(ctx, key); !ok {
		return &DevicePreapprovalAttemptsError{RetryAfter: retryAfter}
	}
	return ErrDeviceLoginBindingMismatch
}

// deviceLoginDeniedResult is the terminal answer to a poll of a request that
// can no longer be collected.
func deviceLoginDeniedResult() *DeviceLoginPollResult {
	return &DeviceLoginPollResult{Status: DeviceLoginStatusDenied, PollAfter: int(deviceLoginPollInterval.Seconds())}
}

// lockPreapprovalIdentity locks and checks the provider identity a
// pre-approved request was approved through, as a provider sign-in does
// (lockOAuthIdentity). Poll calls it after the account lock and before the
// device row, the order an uninstall takes. It is nil for any other request.
func lockPreapprovalIdentity(ctx context.Context, tx pgx.Tx, preview *deviceLoginRecord, userID int) error {
	if preview.PreapprovedAt == nil || preview.ApprovedIdentityID == nil {
		return nil
	}
	_, err := lockOAuthIdentity(ctx, tx, userID, *preview.ApprovedIdentityID)
	return err
}

// preapprovedSession adjusts the session collecting a pre-approved request:
// it records the polling device's address. The session keeps the approving
// session's provider time, as every device sign-in does: redeeming is not a
// provider sign-in, so it must not restart the limit that ends a chain the
// provider can no longer vouch for. It reports denied, after withdrawing the
// request in tx, when the request was approved through a provider identity
// that is gone or refused since; identityErr is lockPreapprovalIdentity's
// answer. It answers that denial once the withdrawal is committed, any other
// identity failure (a disabled provider) as its error, and nil to go on
// collecting.
func preapprovedSession(ctx context.Context, tx pgx.Tx, record *deviceLoginRecord, identityErr error, session *models.AuthSession) (*DeviceLoginPollResult, error) {
	gone := record.ApprovedIdentityID == nil && record.ApprovedProviderSince != nil
	if errors.Is(identityErr, ErrNotPermitted) || gone {
		if _, err := tx.Exec(ctx, `UPDATE device_login_requests SET status = $2, denied_at = NOW(), updated_at = NOW() WHERE id = $1`,
			record.ID, DeviceLoginStatusDenied); err != nil {
			return nil, fmt.Errorf("withdraw device pre-approval: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit device pre-approval withdrawal: %w", err)
		}
		return deviceLoginDeniedResult(), nil
	}
	if identityErr != nil {
		return nil, identityErr
	}
	session.IPAddress = clientip.FromContext(ctx)
	return nil, nil
}
