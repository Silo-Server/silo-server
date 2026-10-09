package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

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
	poll, err := svc.Poll(ctx, start.DeviceCode)
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
	poll, err = svc.Poll(ctx, start.DeviceCode)
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
	if err := svc.Deny(ctx, DeviceLoginLookupInput{UserCode: start.UserCode}); !errors.Is(err, ErrDeviceLoginCanceled) {
		t.Fatalf("deny error = %v", err)
	}
	poll, err := svc.Poll(ctx, start.DeviceCode)
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
	if poll, err := svc.Poll(ctx, approved.DeviceCode); err != nil || poll.TokenPair != nil {
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
	if poll, err := svc.Poll(ctx, consumed.DeviceCode); err != nil || poll.TokenPair == nil {
		t.Fatalf("collect = %+v, %v", poll, err)
	}
	if status, err := svc.Cancel(ctx, consumed.DeviceCode); err != nil || status != DeviceLoginStatusConsumed {
		t.Fatalf("cancel consumed = %q, %v", status, err)
	}

	if _, err := svc.Cancel(ctx, "no-such-device-code"); !errors.Is(err, ErrDeviceLoginNotFound) {
		t.Fatalf("cancel unknown error = %v", err)
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
	if poll, err := nodeA.Poll(ctx, start.DeviceCode); err != nil || !poll.Opened {
		t.Fatalf("poll on A after lookup on B = %+v, %v", poll, err)
	}
	if err := nodeB.Approve(ctx, DeviceLoginLookupInput{UserCode: start.UserCode}, userID); err != nil {
		t.Fatalf("approve on B: %v", err)
	}
	poll, err := nodeA.Poll(ctx, start.DeviceCode)
	if err != nil || poll.Status != DeviceLoginStatusApproved || poll.TokenPair == nil {
		t.Fatalf("collect on A = %+v, %v", poll, err)
	}
	if poll, err := nodeB.Poll(ctx, start.DeviceCode); err != nil || poll.Status != DeviceLoginStatusConsumed || poll.TokenPair != nil {
		t.Fatalf("second collect on B = %+v, %v", poll, err)
	}

	canceled, err := nodeA.Start(ctx, DeviceLoginStartInput{DeviceName: name("node-a-cancel")})
	if err != nil {
		t.Fatal(err)
	}
	if status, err := nodeB.Cancel(ctx, canceled.DeviceCode); err != nil || status != DeviceLoginStatusCanceled {
		t.Fatalf("cancel on B = %q, %v", status, err)
	}
	if poll, err := nodeA.Poll(ctx, canceled.DeviceCode); err != nil || poll.Status != DeviceLoginStatusCanceled {
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
	if len(left) != 1 || left[0] != name("recent") {
		t.Fatalf("remaining = %v, want only the recent request", left)
	}
}
