package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
)

// countingAttempts is an in-memory AttemptCounter allowing max attempts per
// key until Reset (ratelimit.AttemptLimiter, which imports this package, has
// the real windowed policy).
type countingAttempts struct {
	max    int
	counts map[string]int
}

func (c *countingAttempts) Reserve(_ context.Context, key string) (time.Duration, bool) {
	if c.counts[key] >= c.max {
		return 15 * time.Minute, false
	}
	c.counts[key]++
	return 0, true
}

func (c *countingAttempts) Reset(_ context.Context, key string) { delete(c.counts, key) }

// preapprovalSecret is a device's binding secret and the fingerprint it
// hands the approver.
func preapprovalSecret(t *testing.T) (secret, binding string) {
	t.Helper()
	secret, err := randomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(secret)
	sum := sha256.Sum256(raw)
	return secret, base64.RawURLEncoding.EncodeToString(sum[:])
}

// preapprovalApprover opens a login session for userID, with identityID as
// its provider identity when non-nil, and returns a context carrying its
// claims, as the request middleware would.
func preapprovalApprover(t *testing.T, svc *DeviceLoginService, userID int, identityID *int64, providerSince *time.Time) context.Context {
	t.Helper()
	openedVia := models.SessionOpenedPassword
	if identityID != nil || providerSince != nil {
		openedVia = models.SessionOpenedProvider
	}
	session := models.AuthSession{ID: uuid.New().String(), UserID: userID, DeviceName: "approving phone",
		ExpiresAt: time.Now().Add(24 * time.Hour), IdentityID: identityID, ProviderSince: providerSince, OpenedVia: openedVia}
	if err := svc.sessions.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	return WithClaims(t.Context(), &Claims{UserID: userID, SessionID: session.ID, TokenType: TokenTypeAccess})
}

// atClock sets the service clock to now+offset for the duration of fn.
func atClock(svc *DeviceLoginService, offset time.Duration, fn func()) {
	svc.now = func() time.Time { return time.Now().Add(offset) }
	defer func() { svc.now = time.Now }()
	fn()
}

func preapprovalStatus(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM device_login_requests WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// TestDevicePreapprovalRedeemDB: a sign-in approved one day is redeemed the
// next by the device holding the secret, once. A wrong secret is refused
// and leaves the request as it was; so does a cancel, which takes no secret.
// The device's session records the polling device's address, not the
// phone's. No user code or browser code reaches the request.
func TestDevicePreapprovalRedeemDB(t *testing.T) {
	svc, pool, userID, name := deviceLoginTestService(t)
	ctx := preapprovalApprover(t, svc, userID, nil, nil)
	secret, binding := preapprovalSecret(t)
	var created *DevicePreapprovalResult
	atClock(svc, -24*time.Hour, func() {
		var err error
		created, err = svc.Preapprove(ctx, DevicePreapprovalInput{DeviceName: name("tv"), DevicePlatform: "tvos", Binding: binding}, userID)
		if err != nil {
			t.Fatalf("preapprove: %v", err)
		}
	})
	if got := time.Until(created.ExpiresAt); got < 28*24*time.Hour || got > 29*24*time.Hour {
		t.Fatalf("expires in %v, want 29 days after a day-old approval", got)
	}

	// The short-code lookups never find it, even by its stored hashes.
	if _, err := pool.Exec(t.Context(), `UPDATE device_login_requests SET user_code_hash = $2, browser_code_hash = $3 WHERE id = $1`,
		created.ID, hashDeviceLoginSecret("12345678"), hashDeviceLoginSecret("browser-"+created.ID)); err != nil {
		t.Fatal(err)
	}
	for _, in := range []DeviceLoginLookupInput{{UserCode: "1234-5678"}, {BrowserCode: "browser-" + created.ID}} {
		if _, err := svc.Lookup(t.Context(), in); !errors.Is(err, ErrDeviceLoginNotFound) {
			t.Fatalf("lookup %+v: %v, want not found", in, err)
		}
	}
	if _, err := svc.Cancel(t.Context(), created.DeviceCode); !errors.Is(err, ErrDeviceLoginNotFound) {
		t.Fatalf("cancel: %v, want not found", err)
	}

	wrong, _ := preapprovalSecret(t)
	if _, err := svc.Poll(t.Context(), created.DeviceCode, wrong); !errors.Is(err, ErrDeviceLoginBindingMismatch) {
		t.Fatalf("wrong secret: %v", err)
	}
	if got := preapprovalStatus(t, pool, created.ID); got != DeviceLoginStatusApproved {
		t.Fatalf("after wrong secret and cancel: status %s", got)
	}

	pollCtx := clientip.SetContext(t.Context(), "203.0.113.9")
	poll, err := svc.Poll(pollCtx, created.DeviceCode, secret)
	if err != nil || poll.TokenPair == nil || poll.User.ID != userID {
		t.Fatalf("redeem = %+v, %v", poll, err)
	}
	session, err := svc.sessions.GetByID(t.Context(), poll.TokenPair.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.IPAddress != "203.0.113.9" || session.DeviceName != name("tv") {
		t.Fatalf("device session ip %q name %q", session.IPAddress, session.DeviceName)
	}

	again, err := svc.Poll(pollCtx, created.DeviceCode, secret)
	if err != nil || again.TokenPair != nil || again.Status != DeviceLoginStatusConsumed {
		t.Fatalf("second redeem = %+v, %v", again, err)
	}
	if list, err := svc.ListPreapprovals(ctx, userID); err != nil || len(list) != 0 {
		t.Fatalf("list after redeem = %+v, %v", list, err)
	}
}

// TestDevicePreapprovalRefusedDB: a revoked request, one past its 30 days,
// and one whose approving account was disabled or deleted each answer a
// terminal status with no tokens, never a server error.
func TestDevicePreapprovalRefusedDB(t *testing.T) {
	svc, pool, userID, name := deviceLoginTestService(t)
	ctx := preapprovalApprover(t, svc, userID, nil, nil)
	preapprove := func(label string, offset time.Duration) (string, string, string) {
		secret, binding := preapprovalSecret(t)
		var created *DevicePreapprovalResult
		atClock(svc, offset, func() {
			var err error
			if created, err = svc.Preapprove(ctx, DevicePreapprovalInput{DeviceName: name(label), Binding: binding}, userID); err != nil {
				t.Fatalf("%s: preapprove: %v", label, err)
			}
		})
		return created.ID, created.DeviceCode, secret
	}

	revokedID, revokedCode, revokedSecret := preapprove("revoked", 0)
	keptID, _, _ := preapprove("kept", 0)
	if list, err := svc.ListPreapprovals(ctx, userID); err != nil || len(list) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := svc.RevokePreapproval(ctx, userID, revokedID); err != nil {
		t.Fatal(err)
	}
	var otherID int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'x', 'user', true) RETURNING id`, name("other"), name("other")+"@example.invalid").Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE id = $1`, otherID)
	})
	if err := svc.RevokePreapproval(preapprovalApprover(t, svc, otherID, nil, nil), otherID, keptID); !errors.Is(err, ErrDeviceLoginNotFound) {
		t.Fatalf("another account's revoke: %v", err)
	}
	if list, err := svc.ListPreapprovals(ctx, userID); err != nil || len(list) != 1 || list[0].ID != keptID {
		t.Fatalf("list after revoke = %+v, %v", list, err)
	}

	_, oldCode, oldSecret := preapprove("old", -(DevicePreapprovalTTL + time.Hour))
	for label, tc := range map[string]struct{ code, secret, want string }{
		"revoked": {revokedCode, revokedSecret, DeviceLoginStatusDenied},
		"expired": {oldCode, oldSecret, DeviceLoginStatusExpired},
	} {
		poll, err := svc.Poll(t.Context(), tc.code, tc.secret)
		if err != nil || poll.TokenPair != nil || poll.Status != tc.want {
			t.Fatalf("%s: poll = %+v, %v; want %s", label, poll, err, tc.want)
		}
	}

	_, disabledCode, disabledSecret := preapprove("disabled", 0)
	if _, err := pool.Exec(t.Context(), `UPDATE users SET enabled = FALSE WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if poll, err := svc.Poll(t.Context(), disabledCode, disabledSecret); err != nil || poll.Status != DeviceLoginStatusDenied {
		t.Fatalf("disabled approver: poll = %+v, %v", poll, err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if poll, err := svc.Poll(t.Context(), disabledCode, disabledSecret); err != nil || poll.Status != DeviceLoginStatusDenied {
		t.Fatalf("deleted approver: poll = %+v, %v", poll, err)
	}
}

// TestDevicePreapprovalWithdrawnWithSessionDB: once the one login session
// that approved a sign-in in advance has ended, whichever way it ended
// (expiring included), the sign-in is left out of the list and refused when
// collected, and what the account approved from another session stands.
// Without this a caller holding someone else's session could leave a sign-in
// behind after the session was ended, or hide it by logging the session out.
func TestDevicePreapprovalWithdrawnWithSessionDB(t *testing.T) {
	svc, pool, userID, name := deviceLoginTestService(t)
	keys := NewAppKeyRepository(pool)
	// approve pre-approves from a new session of the account, opened by the
	// device key when one is given, and returns that session's ID with the
	// pre-approval's ID, device code and secret.
	approve := func(label string, deviceKeyID *string) (session, id, code, secret string) {
		t.Helper()
		row := models.AuthSession{ID: uuid.New().String(), UserID: userID, DeviceName: "approving phone",
			ExpiresAt: time.Now().Add(24 * time.Hour), DeviceKeyID: deviceKeyID, OpenedVia: models.SessionOpenedPassword}
		if err := svc.sessions.Create(t.Context(), row); err != nil {
			t.Fatal(err)
		}
		ctx := WithClaims(t.Context(), &Claims{UserID: userID, SessionID: row.ID, TokenType: TokenTypeAccess})
		secret, binding := preapprovalSecret(t)
		created, err := svc.Preapprove(ctx, DevicePreapprovalInput{DeviceName: name(label), Binding: binding}, userID)
		if err != nil {
			t.Fatalf("%s: preapprove: %v", label, err)
		}
		return row.ID, created.ID, created.DeviceCode, secret
	}
	var adminID int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'x', 'admin', true) RETURNING id`, name("admin"), name("admin")+"@example.invalid").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE id = $1`, adminID)
	})
	key, err := keys.InsertDeviceKey(t.Context(), NewDeviceKey{
		UserID: userID, PublicKey: newAppPublicKey(t), Kind: models.DeviceKeyKindDevice, Name: "Phone",
		EnrolledVia: models.AppKeyEnrolledSession,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, keptID, _, _ := approve("kept", nil)
	for label, end := range map[string]struct {
		deviceKeyID *string
		end         func(session string) error
	}{
		"ended from the session list": {end: func(session string) error {
			return svc.sessions.revokeChecked(t.Context(), session, func(*models.AuthSession) error { return nil },
				func(pgx.Tx, *models.AuthSession) (*auditmutation.Entry, error) { return nil, nil })
		}},
		"logged out": {end: func(session string) error { return svc.sessions.Revoke(t.Context(), session) }},
		"ended by an administrator": {end: func(session string) error {
			_, err := svc.sessions.RevokeAsAdmin(t.Context(), adminID, userID, &session)
			return err
		}},
		"app key revoked": {deviceKeyID: &key.ID, end: func(string) error {
			return keys.RevokeDeviceKey(t.Context(), userID, key.ID)
		}},
		// What a node from a release before pre-approval does on logout.
		"revoked by a bare update": {end: func(session string) error {
			_, err := pool.Exec(t.Context(), `UPDATE auth_sessions SET revoked_at = NOW() WHERE id = $1`, session)
			return err
		}},
		// An approving session that simply expires stops its pre-approvals
		// at once.
		"expired": {end: func(session string) error {
			_, err := pool.Exec(t.Context(), `UPDATE auth_sessions SET expires_at = NOW() - interval '1 second' WHERE id = $1`, session)
			return err
		}},
	} {
		session, id, code, secret := approve(label, end.deviceKeyID)
		if err := end.end(session); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		listed, err := svc.listPreapprovals(t.Context(), userID)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range listed {
			if p.ID == id {
				t.Fatalf("%s: the pre-approval of an ended session is still listed", label)
			}
		}
		poll, err := svc.Poll(t.Context(), code, secret)
		if err != nil || poll.TokenPair != nil || poll.Status != DeviceLoginStatusDenied {
			t.Fatalf("%s: poll = %+v, %v; want denied", label, poll, err)
		}
	}
	if got := preapprovalStatus(t, pool, keptID); got != DeviceLoginStatusApproved {
		t.Fatalf("ending other sessions left the kept pre-approval %s", got)
	}
}

// TestDevicePreapprovalProviderIdentityDB: a sign-in approved from a
// provider session opens a session carrying that identity, vouched for from
// redemption rather than from the day it was approved. Once the identity is
// refused, or unlinked, the sign-in is refused as denied and withdrawn.
func TestDevicePreapprovalProviderIdentityDB(t *testing.T) {
	svc, pool, userID, name := deviceLoginTestService(t)
	var installationID int
	if err := pool.QueryRow(t.Context(), `INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		VALUES ($1, '0', '/nonexistent/preapproval-test', true, 'manual', 'plugin') RETURNING id`, name("plugin")).Scan(&installationID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM plugin_installations WHERE id = $1`, installationID)
	})
	var identityID int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, $2, $3) RETURNING id`, installationID, name("subject"), userID).Scan(&identityID); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-48 * time.Hour)
	ctx := preapprovalApprover(t, svc, userID, &identityID, &since)
	preapprove := func(label string) (string, string, string) {
		secret, binding := preapprovalSecret(t)
		created, err := svc.Preapprove(ctx, DevicePreapprovalInput{DeviceName: name(label), Binding: binding}, userID)
		if err != nil {
			t.Fatalf("%s: preapprove: %v", label, err)
		}
		return created.ID, created.DeviceCode, secret
	}

	_, code, secret := preapprove("tv")
	poll, err := svc.Poll(t.Context(), code, secret)
	if err != nil || poll.TokenPair == nil {
		t.Fatalf("redeem = %+v, %v", poll, err)
	}
	session, err := svc.sessions.GetByID(t.Context(), poll.TokenPair.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	// The provider vouched when the approving session signed in, not when
	// the device redeemed: a later time would extend the chain's limit.
	if session.IdentityID == nil || *session.IdentityID != identityID || session.ProviderSince == nil ||
		session.ProviderSince.Sub(since).Abs() > time.Millisecond {
		t.Fatalf("device session identity %v since %v, want identity %d since %v", session.IdentityID, session.ProviderSince, identityID, since)
	}

	refusedID, refusedCode, refusedSecret := preapprove("refused")
	goneID, goneCode, goneSecret := preapprove("gone")
	if _, err := pool.Exec(t.Context(), `UPDATE plugin_auth_identities SET last_check_status = 'not_permitted' WHERE id = $1`, identityID); err != nil {
		t.Fatal(err)
	}
	if poll, err := svc.Poll(t.Context(), refusedCode, refusedSecret); err != nil || poll.Status != DeviceLoginStatusDenied || poll.TokenPair != nil {
		t.Fatalf("refused identity: poll = %+v, %v", poll, err)
	}
	if got := preapprovalStatus(t, pool, refusedID); got != DeviceLoginStatusDenied {
		t.Fatalf("refused identity: status %s", got)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM plugin_auth_identities WHERE id = $1`, identityID); err != nil {
		t.Fatal(err)
	}
	if poll, err := svc.Poll(t.Context(), goneCode, goneSecret); err != nil || poll.Status != DeviceLoginStatusDenied || poll.TokenPair != nil {
		t.Fatalf("unlinked identity: poll = %+v, %v", poll, err)
	}
	if got := preapprovalStatus(t, pool, goneID); got != DeviceLoginStatusDenied {
		t.Fatalf("unlinked identity: status %s", got)
	}
}

// TestDevicePreapprovalCapDB: an account holds at most
// MaxDevicePreapprovals waiting sign-ins; revoking one makes room.
func TestDevicePreapprovalCapDB(t *testing.T) {
	svc, _, userID, name := deviceLoginTestService(t)
	ctx := preapprovalApprover(t, svc, userID, nil, nil)
	var first string
	for i := range MaxDevicePreapprovals {
		_, binding := preapprovalSecret(t)
		created, err := svc.Preapprove(ctx, DevicePreapprovalInput{DeviceName: name("tv"), Binding: binding}, userID)
		if err != nil {
			t.Fatalf("preapproval %d: %v", i+1, err)
		}
		if i == 0 {
			first = created.ID
		}
	}
	_, binding := preapprovalSecret(t)
	if _, err := svc.Preapprove(ctx, DevicePreapprovalInput{DeviceName: name("tv"), Binding: binding}, userID); !errors.Is(err, ErrDevicePreapprovalLimit) {
		t.Fatalf("over the cap: %v", err)
	}
	if err := svc.RevokePreapproval(ctx, userID, first); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Preapprove(ctx, DevicePreapprovalInput{DeviceName: name("tv"), Binding: binding}, userID); err != nil {
		t.Fatalf("after a revoke: %v", err)
	}
}

// TestDevicePreapprovalWrongSecretBudgetDB: wrong secrets for one
// pre-approved request are counted against its device code and locked out
// past the budget, but the lock never refuses the right secret, so someone
// who learned the code cannot keep the device out.
func TestDevicePreapprovalWrongSecretBudgetDB(t *testing.T) {
	svc, pool, userID, name := deviceLoginTestService(t)
	budget := &countingAttempts{max: 10, counts: map[string]int{}}
	svc.WithPreapprovalAttempts(budget)
	ctx := preapprovalApprover(t, svc, userID, nil, nil)
	secret, binding := preapprovalSecret(t)
	created, err := svc.Preapprove(ctx, DevicePreapprovalInput{DeviceName: name("tv"), Binding: binding}, userID)
	if err != nil {
		t.Fatal(err)
	}
	wrong, _ := preapprovalSecret(t)
	for i := range budget.max {
		if _, err := svc.Poll(t.Context(), created.DeviceCode, wrong); !errors.Is(err, ErrDeviceLoginBindingMismatch) {
			t.Fatalf("wrong secret %d: %v", i+1, err)
		}
	}
	var attempts *DevicePreapprovalAttemptsError
	if _, err := svc.Poll(t.Context(), created.DeviceCode, wrong); !errors.As(err, &attempts) || attempts.RetryAfter <= 0 {
		t.Fatalf("wrong secret past the budget: %v", err)
	}
	if got := preapprovalStatus(t, pool, created.ID); got != DeviceLoginStatusApproved {
		t.Fatalf("status %s after the budget ran out", got)
	}
	result, err := svc.Poll(t.Context(), created.DeviceCode, secret)
	if err != nil || result.TokenPair == nil {
		t.Fatalf("right secret past the budget: %+v, %v", result, err)
	}
}

// TestDevicePreapprovalSessionRuleDB: a sign-in approved in advance outlives
// the session that made it, so a session opened by device sign-in (a TV), by
// another app's key, or with no record of how it was opened can't make one,
// list the waiting ones, or withdraw one; the person's own password session
// can do all three. Regression: a shared TV's session listed and withdrew
// the account's waiting approvals.
func TestDevicePreapprovalSessionRuleDB(t *testing.T) {
	svc, pool, userID, name := deviceLoginTestService(t)
	grant, err := NewAppKeyRepository(pool).InsertDeviceKey(t.Context(), NewDeviceKey{UserID: userID, PublicKey: newAppPublicKey(t),
		Kind: models.DeviceKeyKindApp, Name: "Other app", EnrolledVia: models.AppKeyEnrolledGrant})
	if err != nil {
		t.Fatal(err)
	}
	owner := preapprovalApprover(t, svc, userID, nil, nil)
	for _, tc := range []struct {
		name, via string
		deviceKey *string
		allowed   bool
	}{
		{name: "password", via: models.SessionOpenedPassword, allowed: true},
		{name: "device sign-in", via: models.SessionOpenedDeviceLogin},
		{name: "another app's key", via: models.SessionOpenedAppKey, deviceKey: &grant.ID},
		{name: "no record", via: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := models.AuthSession{ID: uuid.New().String(), UserID: userID, DeviceName: "caller",
				ExpiresAt: time.Now().Add(time.Hour), OpenedVia: tc.via, DeviceKeyID: tc.deviceKey}
			if err := svc.sessions.Create(t.Context(), session); err != nil {
				t.Fatal(err)
			}
			ctx := WithClaims(t.Context(), &Claims{UserID: userID, SessionID: session.ID, TokenType: TokenTypeAccess})
			refused := func(op string, err error) {
				t.Helper()
				if tc.allowed != (err == nil) || (!tc.allowed && !errors.Is(err, ErrSessionNotAllowed)) {
					t.Fatalf("%s from a %s session = %v", op, tc.name, err)
				}
			}
			_, binding := preapprovalSecret(t)
			_, err := svc.Preapprove(ctx, DevicePreapprovalInput{DeviceName: name(tc.name), Binding: binding}, userID)
			refused("preapprove", err)
			_, err = svc.ListPreapprovals(ctx, userID)
			refused("list", err)
			_, binding = preapprovalSecret(t)
			waiting, err := svc.Preapprove(owner, DevicePreapprovalInput{DeviceName: name(tc.name + " waiting"), Binding: binding}, userID)
			if err != nil {
				t.Fatal(err)
			}
			refused("withdraw", svc.RevokePreapproval(ctx, userID, waiting.ID))
			want := DeviceLoginStatusDenied
			if !tc.allowed {
				want = DeviceLoginStatusApproved
			}
			if got := preapprovalStatus(t, pool, waiting.ID); got != want {
				t.Fatalf("after withdraw from a %s session: status %s, want %s", tc.name, got, want)
			}
		})
	}
}
