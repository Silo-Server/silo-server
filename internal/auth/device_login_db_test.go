package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// deviceLoginTestService builds a DeviceLoginService on the migrated database
// named by SILO_TEST_DATABASE_URL, plus one enabled account to approve with
// and a namer for this test's device names, which cleanup deletes by.
func deviceLoginTestService(t *testing.T) (*DeviceLoginService, *pgxpool.Pool, int, func(string) string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var userID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'x', 'user', true)
		RETURNING id`,
		"device-login-"+suffix, "device-login-"+suffix+"@example.invalid",
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM device_login_requests WHERE device_name LIKE $1`, "device-login-test-"+suffix+"%")
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM auth_sessions WHERE user_id = $1`, userID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})

	svc := NewDeviceLoginService(pool, NewUserRepository(pool), NewJWTService("device-login-test-secret", time.Hour, 24*time.Hour), NewSessionRepository(pool), nil, nil)
	name := func(label string) string { return "device-login-test-" + suffix + "-" + label }
	return svc, pool, userID, name
}

// TestDeviceLoginStartShowsShortCodeLinkDB: the code is eight grouped digits,
// the QR link carries the code rather than the long browser token, and the
// request lives 15 minutes.
func TestDeviceLoginStartShowsShortCodeLinkDB(t *testing.T) {
	svc, _, _, name := deviceLoginTestService(t)
	ctx := t.Context()
	before := time.Now()
	start, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("start"), BaseURL: "https://media.example.test/"})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9]{4}-[0-9]{4}$`).MatchString(start.UserCode) {
		t.Fatalf("user code = %q", start.UserCode)
	}
	if start.VerificationURI != "https://media.example.test/activate" {
		t.Fatalf("verification_uri = %q", start.VerificationURI)
	}
	if want := "https://media.example.test/activate?code=" + normalizeUserCode(start.UserCode); start.VerificationURIComplete != want {
		t.Fatalf("verification_uri_complete = %q, want %q", start.VerificationURIComplete, want)
	}
	if start.ExpiresIn != 900 || start.ExpiresAt.Before(before.Add(14*time.Minute)) {
		t.Fatalf("expires_in = %d, expires_at = %v", start.ExpiresIn, start.ExpiresAt)
	}
	// Spaces and dashes don't matter when the person types it.
	info, err := svc.Lookup(ctx, DeviceLoginLookupInput{UserCode: " " + normalizeUserCode(start.UserCode)[:4] + " " + normalizeUserCode(start.UserCode)[4:]})
	if err != nil || info.Status != DeviceLoginStatusPending {
		t.Fatalf("lookup = %+v, %v", info, err)
	}
}

// TestDeviceLoginStartLegacyKeepsV1AnswerDB: the frozen /api/v1 start keeps
// its 10-minute request and a link carrying the browser code, which a lookup
// by token still finds.
func TestDeviceLoginStartLegacyKeepsV1AnswerDB(t *testing.T) {
	svc, _, _, name := deviceLoginTestService(t)
	ctx := t.Context()
	before := time.Now()
	start, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("legacy"), BaseURL: "http://127.0.0.1:8090", Legacy: true})
	if err != nil {
		t.Fatal(err)
	}
	if start.ExpiresIn != 600 || start.ExpiresAt.Before(before.Add(9*time.Minute)) || start.ExpiresAt.After(before.Add(11*time.Minute)) {
		t.Fatalf("expires_in = %d, expires_at = %v", start.ExpiresIn, start.ExpiresAt)
	}
	match := regexp.MustCompile(`^http://127\.0\.0\.1:8090/activate\?token=([A-Za-z0-9_-]{43})$`).FindStringSubmatch(start.VerificationURIComplete)
	if match == nil {
		t.Fatalf("verification_uri_complete = %q", start.VerificationURIComplete)
	}
	info, err := svc.Lookup(ctx, DeviceLoginLookupInput{BrowserCode: match[1]})
	if err != nil || info.Status != DeviceLoginStatusPending || info.DeviceName != name("legacy") {
		t.Fatalf("lookup by token = %+v, %v", info, err)
	}
}

// TestDeviceLoginStartRetriesCodeCollisionDB: a user code already stored is
// drawn again rather than failing the start.
func TestDeviceLoginStartRetriesCodeCollisionDB(t *testing.T) {
	svc, _, _, name := deviceLoginTestService(t)
	ctx := t.Context()
	first, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("first")})
	if err != nil {
		t.Fatal(err)
	}
	draws := 0
	svc.newUserCode = func() (string, error) {
		draws++
		if draws == 1 {
			return first.UserCode, nil
		}
		return randomUserCode()
	}
	second, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("second")})
	if err != nil {
		t.Fatal(err)
	}
	if draws != 2 || second.UserCode == first.UserCode {
		t.Fatalf("draws = %d, codes %q and %q", draws, first.UserCode, second.UserCode)
	}
	// Every draw colliding gives up after the attempt limit.
	svc.newUserCode = func() (string, error) { return first.UserCode, nil }
	if _, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("third")}); err == nil {
		t.Fatal("start with only colliding codes succeeded")
	}
}

// TestDeviceLoginLookupMarksOpenedAndHoldsCodeDB: the first approver lookup
// is visible to the device's poll, holds the code for at least five minutes,
// and never past thirty minutes after the request was opened. The poll
// reports the moved expiry. A lookup that doesn't ask to mark (v1) leaves
// the row untouched.
func TestDeviceLoginLookupMarksOpenedAndHoldsCodeDB(t *testing.T) {
	svc, pool, _, name := deviceLoginTestService(t)
	ctx := t.Context()
	start, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("opened")})
	if err != nil {
		t.Fatal(err)
	}
	poll, err := svc.Poll(ctx, start.DeviceCode, "")
	if err != nil || poll.Opened || !poll.ExpiresAt.Equal(start.ExpiresAt.Truncate(time.Microsecond)) {
		t.Fatalf("poll before lookup = %+v, %v", poll, err)
	}

	rowState := func() string {
		t.Helper()
		var state string
		if err := pool.QueryRow(ctx, `SELECT concat_ws('|', status, opened_at, expires_at, updated_at) FROM device_login_requests WHERE device_name = $1`, name("opened")).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	unmarked := rowState()
	info, err := svc.Lookup(ctx, DeviceLoginLookupInput{UserCode: start.UserCode})
	if err != nil || info.Status != DeviceLoginStatusPending || !info.RequestedAt.Before(time.Now()) {
		t.Fatalf("unmarked lookup = %+v, %v", info, err)
	}
	if got := rowState(); got != unmarked {
		t.Fatalf("unmarked lookup changed the row: %s -> %s", unmarked, got)
	}

	// Two minutes left: a lookup pushes the expiry out to about five minutes.
	if _, err := pool.Exec(ctx, `UPDATE device_login_requests SET expires_at = NOW() + interval '2 minutes' WHERE device_name = $1`, name("opened")); err != nil {
		t.Fatal(err)
	}
	info, err = svc.Lookup(ctx, DeviceLoginLookupInput{UserCode: start.UserCode, MarkOpened: true})
	if err != nil || info.Status != DeviceLoginStatusPending {
		t.Fatalf("lookup = %+v, %v", info, err)
	}
	if left := time.Until(info.ExpiresAt); left < 4*time.Minute || left > 6*time.Minute {
		t.Fatalf("expiry after lookup in %v, want about 5m", left)
	}
	poll, err = svc.Poll(ctx, start.DeviceCode, "")
	if err != nil || !poll.Opened || poll.Status != DeviceLoginStatusPending || !poll.ExpiresAt.Equal(info.ExpiresAt) {
		t.Fatalf("poll after lookup = %+v, %v (lookup expiry %v)", poll, err, info.ExpiresAt)
	}

	// Opened 28 minutes ago: the hold stops at the 30-minute cap.
	if _, err := pool.Exec(ctx, `UPDATE device_login_requests SET created_at = NOW() - interval '28 minutes', expires_at = NOW() + interval '1 minute' WHERE device_name = $1`, name("opened")); err != nil {
		t.Fatal(err)
	}
	info, err = svc.Lookup(ctx, DeviceLoginLookupInput{UserCode: start.UserCode, MarkOpened: true})
	if err != nil {
		t.Fatal(err)
	}
	if left := time.Until(info.ExpiresAt); left > 2*time.Minute+10*time.Second {
		t.Fatalf("expiry after capped lookup in %v, want at most about 2m", left)
	}
}

// TestDeviceLoginCancelDB: the device withdraws its request; nobody can then
// approve or deny it, the device's poll sees it, and repeating converges. A
// request already collected is left as it is.
func TestDeviceLoginCancelDB(t *testing.T) {
	svc, _, userID, name := deviceLoginTestService(t)
	ctx := t.Context()

	start, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("cancel")})
	if err != nil {
		t.Fatal(err)
	}
	status, err := svc.Cancel(ctx, start.DeviceCode)
	if err != nil || status != DeviceLoginStatusCanceled {
		t.Fatalf("cancel = %q, %v", status, err)
	}
	info, err := svc.Lookup(ctx, DeviceLoginLookupInput{UserCode: start.UserCode})
	if err != nil || info.Status != DeviceLoginStatusCanceled {
		t.Fatalf("lookup = %+v, %v", info, err)
	}
	if err := svc.Approve(ctx, DeviceLoginLookupInput{UserCode: start.UserCode}, userID); !errors.Is(err, ErrDeviceLoginCanceled) {
		t.Fatalf("approve error = %v", err)
	}
	if err := svc.Deny(ctx, DeviceLoginLookupInput{UserCode: start.UserCode}, userID); !errors.Is(err, ErrDeviceLoginCanceled) {
		t.Fatalf("deny error = %v", err)
	}
	poll, err := svc.Poll(ctx, start.DeviceCode, "")
	if err != nil || poll.Status != DeviceLoginStatusCanceled || poll.TokenPair != nil {
		t.Fatalf("poll = %+v, %v", poll, err)
	}
	if status, err := svc.Cancel(ctx, start.DeviceCode); err != nil || status != DeviceLoginStatusCanceled {
		t.Fatalf("second cancel = %q, %v", status, err)
	}

	// Approved but not yet collected: canceling withdraws it, so the
	// tokens are never issued.
	approved, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("approved")})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, DeviceLoginLookupInput{UserCode: approved.UserCode}, userID); err != nil {
		t.Fatal(err)
	}
	if status, err := svc.Cancel(ctx, approved.DeviceCode); err != nil || status != DeviceLoginStatusCanceled {
		t.Fatalf("cancel approved = %q, %v", status, err)
	}
	if poll, err := svc.Poll(ctx, approved.DeviceCode, ""); err != nil || poll.TokenPair != nil {
		t.Fatalf("poll after canceling an approval = %+v, %v", poll, err)
	}

	// Collected: stays consumed.
	consumed, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("consumed")})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, DeviceLoginLookupInput{UserCode: consumed.UserCode}, userID); err != nil {
		t.Fatal(err)
	}
	if poll, err := svc.Poll(ctx, consumed.DeviceCode, ""); err != nil || poll.TokenPair == nil {
		t.Fatalf("collect = %+v, %v", poll, err)
	}
	if status, err := svc.Cancel(ctx, consumed.DeviceCode); err != nil || status != DeviceLoginStatusConsumed {
		t.Fatalf("cancel consumed = %q, %v", status, err)
	}

	if _, err := svc.Cancel(ctx, "no-such-device-code"); !errors.Is(err, ErrDeviceLoginNotFound) {
		t.Fatalf("cancel unknown error = %v", err)
	}
}

// TestDeviceLoginDenyApprovedByAnotherAccountDB: once one account approves,
// another account can no more deny the request than approve it, so the
// device still collects the approval. The approving account can still
// withdraw its own approval.
func TestDeviceLoginDenyApprovedByAnotherAccountDB(t *testing.T) {
	svc, pool, userID, name := deviceLoginTestService(t)
	ctx := t.Context()
	var otherID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'x', 'user', true)
		RETURNING id`,
		name("other"), name("other")+"@example.invalid",
	).Scan(&otherID); err != nil {
		t.Fatalf("insert other user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = $1`, otherID)
	})

	start, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("deny-other")})
	if err != nil {
		t.Fatal(err)
	}
	lookup := DeviceLoginLookupInput{UserCode: start.UserCode}
	if err := svc.Approve(ctx, lookup, userID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, lookup, otherID); !errors.Is(err, ErrDeviceLoginConflict) {
		t.Fatalf("approve by another account error = %v", err)
	}
	if err := svc.Deny(ctx, lookup, otherID); !errors.Is(err, ErrDeviceLoginConflict) {
		t.Fatalf("deny by another account error = %v", err)
	}
	if poll, err := svc.Poll(ctx, start.DeviceCode, ""); err != nil || poll.TokenPair == nil {
		t.Fatalf("poll after another account's deny = %+v, %v", poll, err)
	}

	withdrawn, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("deny-own")})
	if err != nil {
		t.Fatal(err)
	}
	lookup = DeviceLoginLookupInput{UserCode: withdrawn.UserCode}
	if err := svc.Approve(ctx, lookup, userID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deny(ctx, lookup, userID); err != nil {
		t.Fatalf("deny own approval error = %v", err)
	}
	if poll, err := svc.Poll(ctx, withdrawn.DeviceCode, ""); err != nil || poll.Status != DeviceLoginStatusDenied || poll.TokenPair != nil {
		t.Fatalf("poll after withdrawing = %+v, %v", poll, err)
	}

	// Deleting the approver sets approved_by_user_id NULL and leaves the
	// request approved but uncollectable; any account can still deny it.
	orphaned, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("deny-orphaned")})
	if err != nil {
		t.Fatal(err)
	}
	lookup = DeviceLoginLookupInput{UserCode: orphaned.UserCode}
	if err := svc.Approve(ctx, lookup, otherID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, otherID); err != nil {
		t.Fatalf("delete approving user: %v", err)
	}
	if err := svc.Deny(ctx, lookup, userID); err != nil {
		t.Fatalf("deny orphaned approval error = %v", err)
	}
	if poll, err := svc.Poll(ctx, orphaned.DeviceCode, ""); err != nil || poll.Status != DeviceLoginStatusDenied {
		t.Fatalf("poll after denying orphaned approval = %+v, %v", poll, err)
	}
}

// TestDeviceLoginCancelExpiredDB: canceling a request that already expired
// answers expired, as poll and lookup do, and leaves its stored status alone.
func TestDeviceLoginCancelExpiredDB(t *testing.T) {
	svc, pool, userID, name := deviceLoginTestService(t)
	ctx := t.Context()
	for _, label := range []string{"expired-pending", "expired-approved"} {
		start, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name(label)})
		if err != nil {
			t.Fatal(err)
		}
		want := DeviceLoginStatusPending
		if label == "expired-approved" {
			if err := svc.Approve(ctx, DeviceLoginLookupInput{UserCode: start.UserCode}, userID); err != nil {
				t.Fatal(err)
			}
			want = DeviceLoginStatusApproved
		}
		if _, err := pool.Exec(ctx, `UPDATE device_login_requests SET expires_at = NOW() - interval '1 minute' WHERE device_name = $1`, name(label)); err != nil {
			t.Fatal(err)
		}
		status, err := svc.Cancel(ctx, start.DeviceCode)
		if err != nil || status != DeviceLoginStatusExpired {
			t.Fatalf("%s: cancel = %q, %v", label, status, err)
		}
		var stored string
		var canceledAt *time.Time
		if err := pool.QueryRow(ctx, `SELECT status, canceled_at FROM device_login_requests WHERE device_name = $1`, name(label)).Scan(&stored, &canceledAt); err != nil {
			t.Fatal(err)
		}
		if stored != want || canceledAt != nil {
			t.Fatalf("%s: stored status = %q, canceled_at = %v", label, stored, canceledAt)
		}
	}
}

// testDeviceLoginBinding draws a binding secret and returns it with its
// binding, both unpadded base64url, as a TV makes them.
func testDeviceLoginBinding(t *testing.T) (secret, binding string) {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), base64.RawURLEncoding.EncodeToString(sum[:])
}

// deviceLoginRowState is what a poll can change on a stored request.
type deviceLoginRowState struct {
	status            string
	consumed, session bool
	updatedAt         time.Time
}

func readDeviceLoginRow(t *testing.T, pool *pgxpool.Pool, deviceName string) deviceLoginRowState {
	t.Helper()
	var st deviceLoginRowState
	if err := pool.QueryRow(t.Context(), `SELECT status, consumed_at IS NOT NULL, auth_session_id IS NOT NULL, updated_at
		FROM device_login_requests WHERE device_name = $1`, deviceName).Scan(&st.status, &st.consumed, &st.session, &st.updatedAt); err != nil {
		t.Fatal(err)
	}
	return st
}

// requireBindingMismatch polls deviceCode with each secret and requires the
// binding refusal every time.
func requireBindingMismatch(t *testing.T, svc *DeviceLoginService, label, deviceCode string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if poll, err := svc.Poll(t.Context(), deviceCode, secret); !errors.Is(err, ErrDeviceLoginBindingMismatch) || poll != nil {
			t.Fatalf("%s: poll with secret %q = %+v, %v", label, secret, poll, err)
		}
	}
}

// TestDeviceLoginSessionBindingDB: a request started with a binding shows it
// to the approver and is collected only by a poll carrying the secret it was
// made from. A poll without the secret or with another one is refused
// whatever the state, and leaves the request as it was.
func TestDeviceLoginSessionBindingDB(t *testing.T) {
	svc, pool, userID, name := deviceLoginTestService(t)
	ctx := t.Context()
	secret, binding := testDeviceLoginBinding(t)
	otherSecret, _ := testDeviceLoginBinding(t)
	// No secret, another device's secret, the binding itself, and a value
	// that is not base64url.
	wrong := []string{"", otherSecret, binding, "not base64url!"}

	for _, bad := range []string{binding[:42], binding + "=", "+" + binding[1:], binding + "A"} {
		if _, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("bad"), Binding: bad}); !errors.Is(err, ErrDeviceLoginBadBinding) {
			t.Fatalf("start with binding %q error = %v", bad, err)
		}
	}

	tv, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("tv"), Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := svc.Lookup(ctx, DeviceLoginLookupInput{UserCode: tv.UserCode}); err != nil || info.Binding != binding {
		t.Fatalf("lookup = %+v, %v", info, err)
	}
	requireBindingMismatch(t, svc, "pending", tv.DeviceCode, wrong...)
	if poll, err := svc.Poll(ctx, tv.DeviceCode, secret); err != nil || poll.Status != DeviceLoginStatusPending {
		t.Fatalf("pending poll with the secret = %+v, %v", poll, err)
	}
	if err := svc.Approve(ctx, DeviceLoginLookupInput{UserCode: tv.UserCode}, userID); err != nil {
		t.Fatal(err)
	}
	before := readDeviceLoginRow(t, pool, name("tv"))
	requireBindingMismatch(t, svc, "approved", tv.DeviceCode, wrong...)
	if after := readDeviceLoginRow(t, pool, name("tv")); after != before || after.status != DeviceLoginStatusApproved || after.consumed || after.session {
		t.Fatalf("refused polls changed the request: %+v, was %+v", after, before)
	}
	poll, err := svc.Poll(ctx, tv.DeviceCode, secret)
	if err != nil || poll.TokenPair == nil || poll.User == nil || poll.User.ID != userID {
		t.Fatalf("poll with the secret = %+v, %v", poll, err)
	}
	requireBindingMismatch(t, svc, "consumed", tv.DeviceCode, "")
}

// TestDeviceLoginCopiedBindingDB: a request that copies another device's
// binding from a lookup looks like that device's to the approver but cannot
// be collected without the secret. A request without a binding ignores any
// secret.
func TestDeviceLoginCopiedBindingDB(t *testing.T) {
	svc, pool, userID, name := deviceLoginTestService(t)
	ctx := t.Context()
	_, binding := testDeviceLoginBinding(t)
	otherSecret, _ := testDeviceLoginBinding(t)

	copied, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("copied"), Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := svc.Lookup(ctx, DeviceLoginLookupInput{UserCode: copied.UserCode}); err != nil || info.Binding != binding {
		t.Fatalf("copied lookup = %+v, %v", info, err)
	}
	if err := svc.Approve(ctx, DeviceLoginLookupInput{UserCode: copied.UserCode}, userID); err != nil {
		t.Fatal(err)
	}
	requireBindingMismatch(t, svc, "copied", copied.DeviceCode, "", otherSecret)
	// A node from an older release collects with this statement and never
	// checks the secret; the schema refuses it.
	_, err = pool.Exec(ctx, `UPDATE device_login_requests
		SET status = 'consumed', consumed_at = NOW(), updated_at = NOW()
		WHERE device_name = $1`, name("copied"))
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23514" {
		t.Fatalf("older-release collection error = %v", err)
	}
	if st := readDeviceLoginRow(t, pool, name("copied")); st.status != DeviceLoginStatusApproved || st.consumed || st.session {
		t.Fatalf("copied request = %+v", st)
	}

	plain, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("plain")})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := svc.Lookup(ctx, DeviceLoginLookupInput{UserCode: plain.UserCode}); err != nil || info.Binding != "" {
		t.Fatalf("plain lookup = %+v, %v", info, err)
	}
	if err := svc.Approve(ctx, DeviceLoginLookupInput{UserCode: plain.UserCode}, userID); err != nil {
		t.Fatal(err)
	}
	if poll, err := svc.Poll(ctx, plain.DeviceCode, otherSecret); err != nil || poll.TokenPair == nil {
		t.Fatalf("plain poll = %+v, %v", poll, err)
	}
}

// TestDeviceLoginTwoAccountsOneDeviceDB: one TV signs in to two accounts
// with two requests from the same device ID and address, bound to the same
// setup session as a phone sending both accounts makes them. Each account
// approves its own request, both sessions stay live, and signing one out
// leaves the other.
func TestDeviceLoginTwoAccountsOneDeviceDB(t *testing.T) {
	svc, pool, alexID, name := deviceLoginTestService(t)
	ctx := t.Context()
	var samID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'x', 'user', true)
		RETURNING id`,
		name("sam"), name("sam")+"@example.invalid",
	).Scan(&samID); err != nil {
		t.Fatalf("insert second user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM auth_sessions WHERE user_id = $1`, samID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, samID)
	})

	const deviceID, address = "tv-two-accounts", "192.0.2.10"
	h := http.Header{}
	h.Set(ClientDeviceIDHeader, deviceID)
	deviceCtx := WithClientDevice(ctx, h)
	secret, binding := testDeviceLoginBinding(t)

	accounts := []int{alexID, samID}
	starts := make([]*DeviceLoginStartResult, len(accounts))
	for i := range accounts {
		start, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name("tv"), IPAddress: address, Binding: binding})
		if err != nil {
			t.Fatal(err)
		}
		starts[i] = start
	}
	for i, account := range accounts {
		if err := svc.Approve(ctx, DeviceLoginLookupInput{UserCode: starts[i].UserCode}, account); err != nil {
			t.Fatalf("approve as %d: %v", account, err)
		}
	}
	sessions := NewSessionRepository(pool)
	sessionIDs := make([]string, len(accounts))
	for i, account := range accounts {
		poll, err := svc.Poll(deviceCtx, starts[i].DeviceCode, secret)
		if err != nil || poll.TokenPair == nil || poll.User == nil || poll.User.ID != account {
			t.Fatalf("poll for %d = %+v, %v", account, poll, err)
		}
		session, err := sessions.GetByID(ctx, poll.TokenPair.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if session.UserID != account || session.DeviceID != deviceID || session.IPAddress != address {
			t.Fatalf("session for %d = user %d device %q ip %q", account, session.UserID, session.DeviceID, session.IPAddress)
		}
		sessionIDs[i] = session.ID
	}
	for _, id := range sessionIDs {
		if live, err := sessions.IsValid(ctx, id); err != nil || !live {
			t.Fatalf("session %s live = %v, %v", id, live, err)
		}
	}

	if err := sessions.Revoke(ctx, sessionIDs[0]); err != nil {
		t.Fatal(err)
	}
	if live, err := sessions.IsValid(ctx, sessionIDs[0]); err != nil || live {
		t.Fatalf("signed-out session live = %v, %v", live, err)
	}
	if live, err := sessions.IsValid(ctx, sessionIDs[1]); err != nil || !live {
		t.Fatalf("other account's session live = %v, %v", live, err)
	}
}

// TestDeviceLoginAcrossNodesDB: two API nodes share only Postgres. A request
// started on one is looked up, approved and canceled on the other, and the
// first node sees each step.
func TestDeviceLoginAcrossNodesDB(t *testing.T) {
	nodeA, _, userID, name := deviceLoginTestService(t)
	ctx := t.Context()
	poolB, err := pgxpool.New(ctx, os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect second node: %v", err)
	}
	t.Cleanup(poolB.Close)
	nodeB := NewDeviceLoginService(poolB, NewUserRepository(poolB), NewJWTService("device-login-test-secret", time.Hour, 24*time.Hour), NewSessionRepository(poolB), nil, nil)

	start, err := nodeA.Start(ctx, DeviceLoginStartInput{DeviceName: name("node-a")})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := nodeB.Lookup(ctx, DeviceLoginLookupInput{UserCode: start.UserCode, MarkOpened: true}); err != nil || info.Status != DeviceLoginStatusPending {
		t.Fatalf("lookup on B = %+v, %v", info, err)
	}
	if poll, err := nodeA.Poll(ctx, start.DeviceCode, ""); err != nil || !poll.Opened {
		t.Fatalf("poll on A after lookup on B = %+v, %v", poll, err)
	}
	if err := nodeB.Approve(ctx, DeviceLoginLookupInput{UserCode: start.UserCode}, userID); err != nil {
		t.Fatalf("approve on B: %v", err)
	}
	poll, err := nodeA.Poll(ctx, start.DeviceCode, "")
	if err != nil || poll.Status != DeviceLoginStatusApproved || poll.TokenPair == nil {
		t.Fatalf("collect on A = %+v, %v", poll, err)
	}
	if poll, err := nodeB.Poll(ctx, start.DeviceCode, ""); err != nil || poll.Status != DeviceLoginStatusConsumed || poll.TokenPair != nil {
		t.Fatalf("second collect on B = %+v, %v", poll, err)
	}

	canceled, err := nodeA.Start(ctx, DeviceLoginStartInput{DeviceName: name("node-a-cancel")})
	if err != nil {
		t.Fatal(err)
	}
	if status, err := nodeB.Cancel(ctx, canceled.DeviceCode); err != nil || status != DeviceLoginStatusCanceled {
		t.Fatalf("cancel on B = %q, %v", status, err)
	}
	if poll, err := nodeA.Poll(ctx, canceled.DeviceCode, ""); err != nil || poll.Status != DeviceLoginStatusCanceled {
		t.Fatalf("poll on A after cancel on B = %+v, %v", poll, err)
	}
	if err := nodeA.Approve(ctx, DeviceLoginLookupInput{UserCode: canceled.UserCode}, userID); !errors.Is(err, ErrDeviceLoginCanceled) {
		t.Fatalf("approve on A after cancel on B: %v", err)
	}
}

// TestDeviceLoginRetentionDeletesOldRequestsDB: requests more than a day past
// expiry go, whatever their status; recent ones stay.
func TestDeviceLoginRetentionDeletesOldRequestsDB(t *testing.T) {
	svc, pool, _, name := deviceLoginTestService(t)
	ctx := t.Context()
	for _, label := range []string{"old", "recent"} {
		if _, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name(label)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE device_login_requests SET status = 'consumed', expires_at = NOW() - interval '25 hours' WHERE device_name = $1`, name("old")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE device_login_requests SET expires_at = NOW() - interval '23 hours' WHERE device_name = $1`, name("recent")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDeviceLoginRetention(pool).DeleteExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, err := pool.Query(ctx, `SELECT device_name FROM device_login_requests WHERE device_name IN ($1, $2)`, name("old"), name("recent"))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		left = append(left, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0] != name("recent") {
		t.Fatalf("remaining = %v, want only the recent request", left)
	}
}
