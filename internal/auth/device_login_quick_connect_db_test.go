package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// quickConnectTestService is deviceLoginTestService with a profile store and
// one profile on the approving account.
func quickConnectTestService(t *testing.T) (*DeviceLoginService, *pgxpool.Pool, int, string, func(string) string) {
	t.Helper()
	svc, pool, userID, name := deviceLoginTestService(t)
	ctx := t.Context()
	svc.stores = pgstore.NewPostgresProvider(pool)
	store, err := svc.stores.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	profileID := uuid.NewString()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Kids"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM user_profiles WHERE user_id = $1`, userID)
	})
	return svc, pool, userID, profileID, name
}

// countingReserver stands in for the per-account attempt limiter: it records
// every key and allows the first max attempts.
type countingReserver struct {
	mu   sync.Mutex
	max  int
	keys []string
}

func (r *countingReserver) Reserve(_ context.Context, key string) (time.Duration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, key)
	return time.Minute, len(r.keys) <= r.max
}

func (r *countingReserver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.keys)
}

func quickConnectStatus(t *testing.T, svc *DeviceLoginService, secret string) string {
	t.Helper()
	state, err := svc.QuickConnectState(t.Context(), secret)
	if err != nil {
		t.Fatalf("quick connect state: %v", err)
	}
	return state.Status
}

// TestQuickConnectApproveAndCollectDB walks a Quick Connect request from
// start to collection: the code is six digits derived from the secret and
// visible only to a signed-in account, the account-level approval and the
// native poll refuse it, and the approval for a profile is collected once
// as a session of the approving account bound to that profile.
func TestQuickConnectApproveAndCollectDB(t *testing.T) {
	svc, pool, userID, profileID, name := quickConnectTestService(t)
	ctx := t.Context()
	before := time.Now()
	start, err := svc.StartQuickConnect(ctx, QuickConnectStartInput{DeviceName: name("qc"), AppName: "Jellyfin Web", IPAddress: "192.0.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[1-9][0-9]{5}$`).MatchString(start.Code) {
		t.Fatalf("code = %q, want six digits without a leading zero", start.Code)
	}
	if !regexp.MustCompile(`^[0-9A-F]{64}$`).MatchString(start.Secret) {
		t.Fatalf("secret = %q, want 64 uppercase hex digits", start.Secret)
	}
	if start.Code != QuickConnectCode(start.Secret) {
		t.Fatalf("code %q is not the secret's code %q", start.Code, QuickConnectCode(start.Secret))
	}
	if start.ExpiresAt.Before(before.Add(14*time.Minute)) || start.AppName != "Jellyfin Web" {
		t.Fatalf("start = %+v", start)
	}

	if _, err := svc.Lookup(ctx, DeviceLoginLookupInput{UserCode: start.Code}); !errors.Is(err, ErrDeviceLoginNotFound) {
		t.Fatalf("anonymous lookup = %v, want ErrDeviceLoginNotFound", err)
	}
	info, err := svc.Lookup(ctx, DeviceLoginLookupInput{UserCode: start.Code, AccountID: userID, MarkOpened: true})
	if err != nil || info.Status != DeviceLoginStatusPending || info.ClientPurpose != DeviceLoginPurposeQuickConnect ||
		info.UserCode != start.Code || info.DevicePlatform != "Jellyfin Web" || info.Temporary {
		t.Fatalf("lookup = %+v, %v", info, err)
	}
	state, err := svc.QuickConnectState(ctx, start.Secret)
	if err != nil || state.Status != DeviceLoginStatusPending || state.Code != start.Code {
		t.Fatalf("state = %+v, %v", state, err)
	}

	lookup := DeviceLoginLookupInput{UserCode: start.Code, AccountID: userID}
	if err := svc.Approve(ctx, lookup, userID); !errors.Is(err, ErrDeviceLoginPurpose) {
		t.Fatalf("account approval = %v, want ErrDeviceLoginPurpose", err)
	}
	if _, err := svc.Poll(ctx, start.Secret); !errors.Is(err, ErrDeviceLoginNotFound) {
		t.Fatalf("native poll = %v, want ErrDeviceLoginNotFound", err)
	}
	if err := svc.ApproveForProfile(ctx, lookup, userID, profileID); err != nil {
		t.Fatalf("approve for profile: %v", err)
	}
	if got := quickConnectStatus(t, svc, start.Secret); got != DeviceLoginStatusApproved {
		t.Fatalf("status after approval = %q", got)
	}

	result, err := svc.CollectQuickConnect(ctx, start.Secret, nil)
	if err != nil || result.TokenPair == nil || result.User == nil || result.User.ID != userID ||
		result.ProfileID != profileID || result.ProfileName != "Kids" || result.Temporary {
		t.Fatalf("collect = %+v, %v", result, err)
	}
	claims, err := svc.jwt.ValidateToken(result.TokenPair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	// A Jellyfin-compatible session lives as long as the pair's login session.
	if result.TokenPair.SessionID != claims.SessionID {
		t.Fatalf("pair session = %q, want the tokens' %q", result.TokenPair.SessionID, claims.SessionID)
	}
	var active bool
	if err := pool.QueryRow(ctx, `SELECT revoked_at IS NULL AND expires_at > NOW() FROM auth_sessions WHERE id = $1 AND user_id = $2`,
		claims.SessionID, userID).Scan(&active); err != nil || !active {
		t.Fatalf("collected session active = %v, %v", active, err)
	}
	if got := quickConnectStatus(t, svc, start.Secret); got != DeviceLoginStatusConsumed {
		t.Fatalf("status after collection = %q", got)
	}
	again, err := svc.CollectQuickConnect(ctx, start.Secret, nil)
	if err != nil || again.Status != DeviceLoginStatusConsumed || again.TokenPair != nil {
		t.Fatalf("second collect = %+v, %v", again, err)
	}
}

// TestQuickConnectCodeBudgetDB: every six-digit lookup or decision by an
// account spends one attempt, a hit included; once refused the code is not
// checked. Anonymous lookups find nothing and eight-digit codes are not
// counted.
func TestQuickConnectCodeBudgetDB(t *testing.T) {
	svc, _, userID, profileID, name := quickConnectTestService(t)
	ctx := t.Context()
	reserver := &countingReserver{max: 2}
	svc.WithQuickConnectAttempts(reserver)
	start, err := svc.StartQuickConnect(ctx, QuickConnectStartInput{DeviceName: name("budget")})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Lookup(ctx, DeviceLoginLookupInput{UserCode: start.Code}); !errors.Is(err, ErrDeviceLoginNotFound) || reserver.count() != 0 {
		t.Fatalf("anonymous lookup = %v after %d attempts", err, reserver.count())
	}
	if _, err := svc.Lookup(ctx, DeviceLoginLookupInput{UserCode: start.Code, AccountID: userID}); err != nil {
		t.Fatal(err)
	}
	wrong := "100000"
	if start.Code == wrong {
		wrong = "100001"
	}
	if _, err := svc.Lookup(ctx, DeviceLoginLookupInput{UserCode: wrong, AccountID: userID}); !errors.Is(err, ErrDeviceLoginNotFound) {
		t.Fatalf("wrong code = %v", err)
	}
	err = svc.ApproveForProfile(ctx, DeviceLoginLookupInput{UserCode: start.Code, AccountID: userID}, userID, profileID)
	var attempts *QuickConnectAttemptsError
	if !errors.As(err, &attempts) || attempts.RetryAfter != time.Minute {
		t.Fatalf("approval past the budget = %v, want QuickConnectAttemptsError", err)
	}
	if got := quickConnectStatus(t, svc, start.Secret); got != DeviceLoginStatusPending {
		t.Fatalf("status after refused approval = %q", got)
	}
	for _, key := range reserver.keys {
		if want := "quick_connect_code:" + strconv.Itoa(userID); key != want {
			t.Fatalf("attempt key = %q, want %q", key, want)
		}
	}

	device, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("eight-digit")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Lookup(ctx, DeviceLoginLookupInput{UserCode: device.UserCode, AccountID: userID}); err != nil || reserver.count() != 3 {
		t.Fatalf("eight-digit lookup = %v after %d attempts, want 3", err, reserver.count())
	}
}

// TestQuickConnectDenyAndExpiryDB: a denied request is never collected and
// an expired one can no longer be approved, as for device sign-in.
func TestQuickConnectDenyAndExpiryDB(t *testing.T) {
	svc, pool, userID, profileID, name := quickConnectTestService(t)
	ctx := t.Context()

	denied, err := svc.StartQuickConnect(ctx, QuickConnectStartInput{DeviceName: name("denied")})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Deny(ctx, DeviceLoginLookupInput{UserCode: denied.Code, AccountID: userID}); err != nil {
		t.Fatal(err)
	}
	if got := quickConnectStatus(t, svc, denied.Secret); got != DeviceLoginStatusDenied {
		t.Fatalf("status after deny = %q", got)
	}
	result, err := svc.CollectQuickConnect(ctx, denied.Secret, nil)
	if err != nil || result.Status != DeviceLoginStatusDenied || result.TokenPair != nil {
		t.Fatalf("collect denied = %+v, %v", result, err)
	}

	expired, err := svc.StartQuickConnect(ctx, QuickConnectStartInput{DeviceName: name("expired")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE device_login_requests SET expires_at = NOW() - INTERVAL '1 second' WHERE device_name = $1`, name("expired")); err != nil {
		t.Fatal(err)
	}
	if got := quickConnectStatus(t, svc, expired.Secret); got != DeviceLoginStatusExpired {
		t.Fatalf("status after expiry = %q", got)
	}
	if err := svc.ApproveForProfile(ctx, DeviceLoginLookupInput{UserCode: expired.Code, AccountID: userID}, userID, profileID); !errors.Is(err, ErrDeviceLoginExpired) {
		t.Fatalf("approve expired = %v, want ErrDeviceLoginExpired", err)
	}
	if _, err := svc.QuickConnectState(ctx, "not-a-secret"); !errors.Is(err, ErrDeviceLoginNotFound) {
		t.Fatalf("unknown secret = %v", err)
	}
}

// TestQuickConnectAcrossNodesDB: a request started on one node is approved
// on another and collected back on the first, since all of its state is in
// Postgres.
func TestQuickConnectAcrossNodesDB(t *testing.T) {
	first, _, userID, profileID, name := quickConnectTestService(t)
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	second := NewDeviceLoginService(pool, NewUserRepository(pool), first.jwt, NewSessionRepository(pool), pgstore.NewPostgresProvider(pool), nil)

	start, err := first.StartQuickConnect(ctx, QuickConnectStartInput{DeviceName: name("nodes")})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.ApproveForProfile(ctx, DeviceLoginLookupInput{UserCode: start.Code, AccountID: userID}, userID, profileID); err != nil {
		t.Fatal(err)
	}
	result, err := first.CollectQuickConnect(ctx, start.Secret, nil)
	if err != nil || result.TokenPair == nil || result.ProfileID != profileID {
		t.Fatalf("collect on the first node = %+v, %v", result, err)
	}
}

// TestQuickConnectApproveAsSessionDB: a Jellyfin app's login session approves
// a Quick Connect request for its own profile; a revoked session, a token that
// is not an access token, and a request of another purpose are refused.
func TestQuickConnectApproveAsSessionDB(t *testing.T) {
	svc, pool, userID, profileID, name := quickConnectTestService(t)
	ctx := t.Context()
	session := models.AuthSession{ID: uuid.NewString(), UserID: userID, ExpiresAt: time.Now().Add(time.Hour)}
	if err := svc.sessions.Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	token, err := svc.jwt.generateAccessToken(Claims{UserID: userID, Role: "user", SessionID: session.ID})
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := svc.jwt.generateRefreshToken(Claims{UserID: userID, Role: "user", SessionID: session.ID})
	if err != nil {
		t.Fatal(err)
	}

	start, err := svc.StartQuickConnect(ctx, QuickConnectStartInput{DeviceName: name("as-session")})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApproveQuickConnectAsSession(ctx, start.Code, refresh, profileID); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("approve with a refresh token = %v, want ErrSessionRevoked", err)
	}
	if err := svc.ApproveQuickConnectAsSession(ctx, start.Code, token, profileID); err != nil {
		t.Fatal(err)
	}
	if got := quickConnectStatus(t, svc, start.Secret); got != DeviceLoginStatusApproved {
		t.Fatalf("status = %q", got)
	}

	remote, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("remote"), ClientPurpose: DeviceLoginPurposeRemote, Temporary: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApproveQuickConnectAsSession(ctx, remote.UserCode, token, profileID); !errors.Is(err, ErrDeviceLoginPurpose) {
		t.Fatalf("approve remote playback as a Jellyfin session = %v, want ErrDeviceLoginPurpose", err)
	}

	revoked, err := svc.StartQuickConnect(ctx, QuickConnectStartInput{DeviceName: name("revoked")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at = NOW() WHERE id = $1`, session.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.ApproveQuickConnectAsSession(ctx, revoked.Code, token, profileID); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("approve from a revoked session = %v, want ErrSessionRevoked", err)
	}
}

// TestQuickConnectFreesExpiredCodesDB: an expired Quick Connect request gives
// its code back at once, while a live one still makes a start draw again.
func TestQuickConnectFreesExpiredCodesDB(t *testing.T) {
	svc, pool, _, _, name := quickConnectTestService(t)
	ctx := t.Context()
	first, err := randomQuickConnectSecret()
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomQuickConnectSecret()
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{first}
	svc.newQuickConnectSecret = func() (string, error) {
		next := secrets[0]
		if len(secrets) > 1 {
			secrets = secrets[1:]
		}
		return next, nil
	}

	old, err := svc.StartQuickConnect(ctx, QuickConnectStartInput{DeviceName: name("old")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE device_login_requests SET expires_at = NOW() - INTERVAL '1 second' WHERE device_name = $1`, name("old")); err != nil {
		t.Fatal(err)
	}
	reused, err := svc.StartQuickConnect(ctx, QuickConnectStartInput{DeviceName: name("reused")})
	if err != nil || reused.Code != old.Code {
		t.Fatalf("start over an expired code = %+v, %v; want code %s", reused, err, old.Code)
	}

	secrets = []string{first, second}
	fresh, err := svc.StartQuickConnect(ctx, QuickConnectStartInput{DeviceName: name("fresh")})
	if err != nil || fresh.Code == reused.Code || fresh.Code != QuickConnectCode(second) {
		t.Fatalf("start over a live code = %+v, %v; want the second secret's code", fresh, err)
	}
}

// TestQuickConnectRefusesTemporaryPasswordDB: an account that must replace a
// temporary password cannot collect, and its approval is withdrawn so the
// app stops polling.
func TestQuickConnectRefusesTemporaryPasswordDB(t *testing.T) {
	svc, pool, userID, profileID, name := quickConnectTestService(t)
	ctx := t.Context()
	start, err := svc.StartQuickConnect(ctx, QuickConnectStartInput{DeviceName: name("temp-password")})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApproveForProfile(ctx, DeviceLoginLookupInput{UserCode: start.Code, AccountID: userID}, userID, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET password_change_required = TRUE WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CollectQuickConnect(ctx, start.Secret, nil); !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatalf("collect = %v, want ErrPasswordChangeRequired", err)
	}
	if got := quickConnectStatus(t, svc, start.Secret); got != DeviceLoginStatusDenied {
		t.Fatalf("status after refused collection = %q, want denied", got)
	}
}

// TestQuickConnectCollectsOnlyWithItsSessionDB: when storing the app's
// session fails, the request is not consumed and no login session is left
// behind, so the app can collect again.
func TestQuickConnectCollectsOnlyWithItsSessionDB(t *testing.T) {
	svc, pool, userID, profileID, name := quickConnectTestService(t)
	ctx := t.Context()
	start, err := svc.StartQuickConnect(ctx, QuickConnectStartInput{DeviceName: name("persist")})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApproveForProfile(ctx, DeviceLoginLookupInput{UserCode: start.Code, AccountID: userID}, userID, profileID); err != nil {
		t.Fatal(err)
	}
	countSessions := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE user_id = $1`, userID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := countSessions()
	failed := errors.New("compat store down")
	if _, err := svc.CollectQuickConnect(ctx, start.Secret, func(context.Context, pgx.Tx, *DeviceLoginPollResult) error {
		return failed
	}); !errors.Is(err, failed) {
		t.Fatalf("collect with a failing store = %v", err)
	}
	if got := quickConnectStatus(t, svc, start.Secret); got != DeviceLoginStatusApproved || countSessions() != before {
		t.Fatalf("after a failed store: status %q, %d sessions (was %d)", got, countSessions(), before)
	}
	var stored *DeviceLoginPollResult
	result, err := svc.CollectQuickConnect(ctx, start.Secret, func(_ context.Context, tx pgx.Tx, r *DeviceLoginPollResult) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE user_id = $1`, userID).Scan(&n); err != nil || n != before+1 {
			return fmt.Errorf("the transaction does not hold the new login session (%d): %w", n, err)
		}
		stored = r
		return nil
	})
	if err != nil || result.TokenPair == nil || stored != result {
		t.Fatalf("collect = %+v, %v", result, err)
	}
}
