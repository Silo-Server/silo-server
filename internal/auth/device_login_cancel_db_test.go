package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestDeviceLoginCancelDB(t *testing.T) {
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

	users := NewUserRepository(pool)
	sessions := NewSessionRepository(pool)
	jwt := NewJWTService("device-login-cancel-test", 15*time.Minute, 24*time.Hour)
	svc := NewDeviceLoginService(pool, users, jwt, sessions, nil, nil)

	name := fmt.Sprintf("device-cancel-%d", time.Now().UnixNano())
	approver, err := users.Create(ctx, models.CreateUserInput{Username: name, Email: name + "@example.invalid", Password: "device-cancel-pass", Role: models.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.WithoutCancel(ctx)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM device_login_requests WHERE device_name = $1`, name)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, approver.ID)
	})
	start := func() *DeviceLoginStartResult {
		t.Helper()
		started, err := svc.Start(ctx, DeviceLoginStartInput{DeviceName: name, IPAddress: "192.0.2.10"})
		if err != nil {
			t.Fatal(err)
		}
		return started
	}
	cancel := func(deviceCode, want string) {
		t.Helper()
		if got, err := svc.Cancel(ctx, deviceCode); err != nil || got != want {
			t.Fatalf("cancel = %q, %v; want %q", got, err, want)
		}
	}

	// A pending request is withdrawn; repeating the cancel reports the same.
	pending := start()
	cancel(pending.DeviceCode, DeviceLoginStatusCancelled)
	cancel(pending.DeviceCode, DeviceLoginStatusCancelled)
	byUserCode := DeviceLoginLookupInput{UserCode: pending.UserCode}
	if info, err := svc.Lookup(ctx, byUserCode); err != nil || info.Status != DeviceLoginStatusCancelled {
		t.Fatalf("lookup = %+v, %v", info, err)
	}
	if poll, err := svc.Poll(ctx, pending.DeviceCode); err != nil || poll.Status != DeviceLoginStatusCancelled || poll.TokenPair != nil {
		t.Fatalf("poll = %+v, %v", poll, err)
	}
	if err := svc.Approve(ctx, byUserCode, approver.ID); !errors.Is(err, ErrDeviceLoginCancelled) {
		t.Fatalf("approve after cancel: %v", err)
	}
	if err := svc.Deny(ctx, byUserCode); !errors.Is(err, ErrDeviceLoginCancelled) {
		t.Fatalf("deny after cancel: %v", err)
	}

	// A request that already has a decision keeps it, and still hands out
	// its tokens.
	approved := start()
	if err := svc.Approve(ctx, DeviceLoginLookupInput{UserCode: approved.UserCode}, approver.ID); err != nil {
		t.Fatal(err)
	}
	cancel(approved.DeviceCode, DeviceLoginStatusApproved)
	if poll, err := svc.Poll(ctx, approved.DeviceCode); err != nil || poll.TokenPair == nil {
		t.Fatalf("poll after a refused cancel = %+v, %v", poll, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM auth_sessions WHERE user_id = $1`, approver.ID)
	})

	// An expired request stays as it is and reports expired.
	expired := start()
	if _, err := pool.Exec(ctx, `UPDATE device_login_requests SET expires_at = NOW() - INTERVAL '1 second' WHERE device_code_hash = $1`, hashDeviceLoginSecret(expired.DeviceCode)); err != nil {
		t.Fatal(err)
	}
	cancel(expired.DeviceCode, "expired")
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM device_login_requests WHERE device_code_hash = $1`, hashDeviceLoginSecret(expired.DeviceCode)).Scan(&status); err != nil || status != DeviceLoginStatusPending {
		t.Fatalf("expired request stored as %q (%v)", status, err)
	}

	if _, err := svc.Cancel(ctx, "no-such-device-code"); !errors.Is(err, ErrDeviceLoginNotFound) {
		t.Fatalf("unknown device code: %v", err)
	}
}
