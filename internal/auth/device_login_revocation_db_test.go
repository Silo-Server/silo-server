package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDeviceLoginApprovalRevalidatesSessionDB(t *testing.T) {
	for _, purpose := range []string{DeviceLoginPurposeLogin, DeviceLoginPurposeRemote} {
		for _, state := range []string{"revoked", "expired", "unknown", "missing_session", "api_key", "impersonation"} {
			t.Run(purpose+"/"+state, func(t *testing.T) {
				env := newRecheckEnv(t, "stale_approval", "")
				ctx := t.Context()
				approver, refresh := env.session(t, true)
				claims := &Claims{UserID: env.user.ID, SessionID: approver, TokenType: TokenTypeAccess}
				devices, start, profileID := revocationDevice(t, env, purpose)
				switch state {
				case "revoked":
					env.makeDue(t)
					env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_DISABLED, "")
					if _, err := env.svc.Refresh(ctx, refresh); !errors.Is(err, ErrSessionRevoked) {
						t.Fatalf("provider refusal = %v", err)
					}
				case "expired":
					if _, err := env.pool.Exec(ctx, `UPDATE auth_sessions SET expires_at = NOW() - INTERVAL '1 second' WHERE id = $1`, approver); err != nil {
						t.Fatal(err)
					}
				case "unknown":
					claims.SessionID = "unknown-session"
				case "missing_session":
					claims.SessionID = ""
				case "api_key":
					claims.TokenType = TokenTypeAPIKey
				case "impersonation":
					claims.ImpersonatorUserID = new(env.user.ID)
				}
				if err := approveRevocationDevice(WithClaims(ctx, claims), devices, start, env.user.ID, profileID); !errors.Is(err, ErrSessionRevoked) {
					t.Fatalf("approval from %s session = %v, want ErrSessionRevoked", state, err)
				}
				poll, err := devices.Poll(ctx, start.DeviceCode)
				if err != nil || poll.Status != DeviceLoginStatusPending || poll.TokenPair != nil {
					t.Fatalf("refused approval changed the device request: %+v, %v", poll, err)
				}
			})
		}
	}
}

func TestDeviceLoginRevocationWithdrawsCommittedApprovalDB(t *testing.T) {
	for _, purpose := range []string{DeviceLoginPurposeLogin, DeviceLoginPurposeRemote} {
		for _, breakGlass := range []bool{false, true} {
			name := purpose
			if breakGlass {
				name += "/break_glass"
			}
			t.Run(name, func(t *testing.T) {
				env := newRecheckEnv(t, "approved_revoked", "")
				ctx := t.Context()
				if breakGlass {
					if _, err := env.pool.Exec(ctx, `UPDATE users SET role = 'admin', break_glass = true, local_password_login_enabled = true WHERE id = $1`, env.user.ID); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						_, _ = env.pool.Exec(context.WithoutCancel(ctx), `UPDATE users SET break_glass = false WHERE id = $1`, env.user.ID)
					})
				}
				approver, refresh := env.session(t, true)
				devices, start, profileID := revocationDevice(t, env, purpose)
				claims := &Claims{UserID: env.user.ID, SessionID: approver, TokenType: TokenTypeAccess}
				if err := approveRevocationDevice(WithClaims(ctx, claims), devices, start, env.user.ID, profileID); err != nil {
					t.Fatal(err)
				}
				var localDevices *DeviceLoginService
				var localStart *DeviceLoginStartResult
				if breakGlass {
					localSession, _ := env.session(t, false)
					var localProfileID string
					localDevices, localStart, localProfileID = revocationDevice(t, env, purpose)
					localClaims := &Claims{UserID: env.user.ID, SessionID: localSession, TokenType: TokenTypeAccess}
					if err := approveRevocationDevice(WithClaims(ctx, localClaims), localDevices, localStart, env.user.ID, localProfileID); err != nil {
						t.Fatal(err)
					}
				}
				env.makeDue(t)
				env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_DISABLED, "")
				if _, err := env.svc.Refresh(ctx, refresh); !errors.Is(err, ErrSessionRevoked) {
					t.Fatalf("provider refusal = %v", err)
				}
				poll, err := devices.Poll(ctx, start.DeviceCode)
				if err != nil || poll.Status != DeviceLoginStatusDenied || poll.TokenPair != nil {
					t.Fatalf("revocation did not withdraw approval: %+v, %v", poll, err)
				}
				if breakGlass {
					localPoll, err := localDevices.Poll(ctx, localStart.DeviceCode)
					if err != nil || localPoll.Status != DeviceLoginStatusApproved || localPoll.TokenPair == nil {
						t.Fatalf("provider refusal withdrew a local approval: %+v, %v", localPoll, err)
					}
				}
			})
		}
	}
}

// Poll must wait for the account before it locks the device. Otherwise the
// revocation's device update and the session INSERT's user FK form a deadlock.
func TestDeviceLoginPollSerializesWithRevocationDB(t *testing.T) {
	env := newRecheckEnv(t, "poll_revoked", "")
	ctx := t.Context()
	approver, _ := env.session(t, true)
	devices, start, profileID := revocationDevice(t, env, DeviceLoginPurposeLogin)
	if err := approveRevocationDevice(WithClaims(ctx, &Claims{UserID: env.user.ID, SessionID: approver, TokenType: TokenTypeAccess}), devices, start, env.user.ID, profileID); err != nil {
		t.Fatal(err)
	}
	holder, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := lockUser(ctx, holder, env.user.ID); err != nil {
		t.Fatal(err)
	}
	var holderPID int
	if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		poll *DeviceLoginPollResult
		err  error
	}
	collected := make(chan result, 1)
	go func() {
		poll, err := devices.Poll(ctx, start.DeviceCode)
		collected <- result{poll, err}
	}()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		var blocked bool
		if err := env.pool.QueryRow(waitCtx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE datname = current_database()
			AND $1 = ANY(pg_blocking_pids(pid)))`, holderPID).Scan(&blocked); err != nil {
			t.Fatalf("waiting for poll to block on the account: %v", err)
		}
		if blocked {
			break
		}
	}
	if err := RevokeSignInsInTransaction(ctx, holder, env.user.ID); err != nil {
		t.Fatalf("revocation deadlocked against device collection: %v", err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-collected:
		if got.err != nil || got.poll.Status != DeviceLoginStatusDenied || got.poll.TokenPair != nil {
			t.Fatalf("poll revived access after revocation: %+v, %v", got.poll, got.err)
		}
	case <-waitCtx.Done():
		t.Fatal("device collection did not finish after revocation")
	}
}

func TestDeviceLoginApprovalReloadUsesTransactionDB(t *testing.T) {
	for _, purpose := range []string{DeviceLoginPurposeLogin, DeviceLoginPurposeRemote} {
		t.Run(purpose, func(t *testing.T) {
			env := newRecheckEnv(t, "approval_reload", "")
			ctx := t.Context()
			approver, _ := env.session(t, true)
			devices, start, profileID := revocationDevice(t, env, purpose)
			poolConfig := env.pool.Config().Copy()
			poolConfig.MaxConns = 1
			pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			limited := *devices
			limited.pool = pool
			holder, err := env.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
			if _, err := lockUser(ctx, holder, env.user.ID); err != nil {
				t.Fatal(err)
			}
			var holderPID int
			if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
				t.Fatal(err)
			}
			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			claims := &Claims{UserID: env.user.ID, SessionID: approver, TokenType: TokenTypeAccess}
			go func() {
				done <- approveRevocationDevice(WithClaims(waitCtx, claims), &limited, start, env.user.ID, profileID)
			}()
			for {
				var blocked bool
				if err := env.pool.QueryRow(waitCtx, `SELECT EXISTS (
					SELECT 1 FROM pg_stat_activity WHERE datname = current_database()
					AND $1 = ANY(pg_blocking_pids(pid)))`, holderPID).Scan(&blocked); err != nil {
					t.Fatalf("waiting for approval to lock the account: %v", err)
				}
				if blocked {
					break
				}
			}
			if _, err := devices.Cancel(ctx, start.DeviceCode); err != nil {
				t.Fatal(err)
			}
			if err := holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, ErrDeviceLoginCanceled) {
					t.Fatalf("approval after cancellation = %v, want ErrDeviceLoginCanceled", err)
				}
			case <-waitCtx.Done():
				t.Fatal("approval reload waited for a second pool connection")
			}
		})
	}
}

func revocationDevice(t *testing.T, env *recheckEnv, purpose string) (*DeviceLoginService, *DeviceLoginStartResult, string) {
	t.Helper()
	ctx := t.Context()
	stores := pgstore.NewPostgresProvider(env.pool)
	profiles := access.NewProfileTokenService("device-revocation-profile-secret", time.Hour)
	devices := NewDeviceLoginService(env.pool, NewUserRepository(env.pool), env.jwt, NewSessionRepository(env.pool), stores, profiles)
	name := "device-revocation-" + env.suffix
	t.Cleanup(func() {
		_, _ = env.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM device_login_requests WHERE device_name = $1`, name)
	})
	start, err := devices.Start(ctx, DeviceLoginStartInput{DeviceName: name, ClientPurpose: purpose, Temporary: purpose == DeviceLoginPurposeRemote})
	if err != nil {
		t.Fatal(err)
	}
	profileID := ""
	if purpose == DeviceLoginPurposeRemote {
		store, err := stores.ForUser(ctx, env.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		accounts, err := store.ListProfiles(ctx)
		if err != nil || len(accounts) == 0 {
			t.Fatalf("profiles = %v, %v", accounts, err)
		}
		profileID = accounts[0].ID
	}
	return devices, start, profileID
}

func approveRevocationDevice(ctx context.Context, devices *DeviceLoginService, start *DeviceLoginStartResult, userID int, profileID string) error {
	if profileID == "" {
		return devices.Approve(ctx, DeviceLoginLookupInput{UserCode: start.UserCode}, userID)
	}
	return devices.ApproveRemotePlayback(ctx, DeviceLoginLookupInput{UserCode: start.UserCode}, userID, profileID)
}
