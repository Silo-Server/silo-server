package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestProviderRecheckSingleConnectionDB(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost_answer_%v", lost), func(t *testing.T) {
			env := newRecheckEnv(t, "single_connection", "rt-1")
			ctx := t.Context()
			poolConfig := env.pool.Config().Copy()
			poolConfig.MaxConns = 1
			pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			resolver := *env.resolver
			resolver.pool = pool
			recheck := NewProviderRecheck(&resolver, fakeCheckerSource{checker: env.checker})
			recheck.timeout = time.Second
			markedBeforeCall := false
			env.checker.respond = func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
				markedBeforeCall = env.checkOutcomeUnknown(t)
				if lost {
					return nil, status.Error(codes.Unavailable, "answer lost")
				}
				return &pluginv1.CheckAccountResponse{
					Status:  pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE,
					Account: &pluginv1.AuthenticateResponse{RefreshState: refreshState("rt-2")},
				}, nil
			}
			checkStatus, revoked, err := recheck.recheck(ctx, env.identityState(t), 0, false)
			want := CheckStatusActive
			if lost {
				want = CheckStatusUnavailable
			}
			if err != nil || revoked || checkStatus != want || env.checker.callCount() != 1 || !markedBeforeCall {
				t.Fatalf("single-connection re-check = %q, %v, %v; calls = %d, marker = %v", checkStatus, revoked, err, env.checker.callCount(), markedBeforeCall)
			}
			if env.checkOutcomeUnknown(t) != lost {
				t.Fatal("call marker did not reflect whether the answer committed")
			}
			if !lost && env.storedToken(t) != "rt-2" {
				t.Fatal("rotated state was not committed")
			}
			// The only pooled connection must carry no session advisory lock
			// or modified timeout after either a committed or lost answer.
			assertRecheckConnectionReleased(t, pool)
		})
	}
}

func TestProviderRecheckLockTimeoutReleasesConnectionDB(t *testing.T) {
	env := newRecheckEnv(t, "single_connection_timeout", "rt-1")
	ctx := t.Context()
	identity := env.identityState(t)
	holder, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
	if err := lockExternalSubject(ctx, holder, identity.InstallationID, identity.ExternalSubject); err != nil {
		t.Fatal(err)
	}
	poolConfig := env.pool.Config().Copy()
	poolConfig.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	resolver := *env.resolver
	resolver.pool = pool
	recheck := NewProviderRecheck(&resolver, fakeCheckerSource{checker: env.checker})
	recheck.lockWait = 50 * time.Millisecond
	checkStatus, revoked, err := recheck.recheck(ctx, identity, 0, false)
	if err != nil || revoked || checkStatus != CheckStatusUnavailable || env.checker.callCount() != 0 {
		t.Fatalf("locked re-check = %q, %v, %v; calls = %d", checkStatus, revoked, err, env.checker.callCount())
	}
	assertRecheckConnectionReleased(t, pool)
}

func assertRecheckConnectionReleased(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var locks int
	var lockTimeout string
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid()`).Scan(&locks); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SHOW lock_timeout`).Scan(&lockTimeout); err != nil {
		t.Fatal(err)
	}
	if locks != 0 || lockTimeout != "0" {
		t.Fatalf("returned connection retained %d locks and timeout %q", locks, lockTimeout)
	}
}

func TestProviderRecheckIgnoresAnswerAfterUnlinkDB(t *testing.T) {
	env := newRecheckEnv(t, "unlink_during_check", "")
	ctx := t.Context()
	sessionID, refresh := env.session(t, true)
	key, err := NewAPIKeyRepository(env.pool).Create(ctx, env.user.ID, "keep-after-unlink", nil)
	if err != nil {
		t.Fatal(err)
	}
	env.makeDue(t)
	entered, release := make(chan struct{}), make(chan struct{})
	env.checker.respond = func(ctx context.Context, _ *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &pluginv1.CheckAccountResponse{Status: pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_DISABLED}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := env.svc.Refresh(ctx, refresh)
		done <- err
	}()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-waitCtx.Done():
		t.Fatal("provider did not enter")
	}
	unlinkErr := NewIdentityService(env.pool).AdminUnlink(waitCtx, env.user.ID, env.identityID, 0, false)
	close(release)
	if unlinkErr != nil {
		t.Fatalf("unlink during provider call: %v", unlinkErr)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stale provider answer refused the detached session: %v", err)
		}
	case <-waitCtx.Done():
		t.Fatal("refresh did not finish after unlink")
	}
	row := env.sessionRow(t, sessionID)
	if row.IdentityID != nil || row.RevokedAt != nil {
		t.Fatalf("unlinked session = %+v", row)
	}
	if _, err := NewAPIKeyRepository(env.pool).GetByKey(ctx, key.Key); err != nil {
		t.Fatalf("stale provider answer removed the API key: %v", err)
	}
	if _, err := identityByID(ctx, env.pool, env.identityID); !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("identity after unlink = %v", err)
	}
}

func TestProviderRecheckKeepsRefusalOnUserLockFailureDB(t *testing.T) {
	env := newRecheckEnv(t, "refusal_user_lock", "rt-1")
	ctx := t.Context()
	env.recheck.writeWait = 100 * time.Millisecond
	sessionID, refresh := env.session(t, true)
	key, err := NewAPIKeyRepository(env.pool).Create(ctx, env.user.ID, "pending-refusal", nil)
	if err != nil {
		t.Fatal(err)
	}
	env.makeDue(t)
	env.checker.respond = func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
		return &pluginv1.CheckAccountResponse{
			Status:  pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_PERMITTED,
			Account: &pluginv1.AuthenticateResponse{RefreshState: oidcRefusedState()},
		}, nil
	}
	holder, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := holder.Exec(ctx, `SELECT id FROM users WHERE id = $1 FOR NO KEY UPDATE`, env.user.ID); err != nil {
		t.Fatal(err)
	}
	_, refreshErr := env.svc.Refresh(ctx, refresh)
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if refreshErr == nil || errors.Is(refreshErr, ErrSessionRevoked) {
		t.Fatalf("blocked revocation = %v, want a failure", refreshErr)
	}
	if env.storedToken(t) != "" || env.pendingRefusal(t) != CheckStatusNotPermitted || env.checkOutcomeUnknown(t) {
		t.Fatal("account lock failure lost the known refusal or replacement state")
	}
	if env.sessionRow(t, sessionID).RevokedAt != nil {
		t.Fatal("failed revocation ended the session")
	}
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSUPPORTED, "")
	if _, err := env.svc.Refresh(ctx, refresh); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("pending refusal = %v, want ErrSessionRevoked", err)
	}
	if env.checker.callCount() != 1 || env.sessionRow(t, sessionID).RevokedAt == nil {
		t.Fatal("the pending refusal did not end the session without another provider call")
	}
	if _, err := NewAPIKeyRepository(env.pool).GetByKey(ctx, key.Key); err == nil {
		t.Fatal("the refused account kept its API key")
	}
}

func TestProviderRecheckActiveRoleFailureAfterUnavailableRetriesDB(t *testing.T) {
	env := newRecheckEnv(t, "unavailable_role_retry", "rt-1")
	ctx := t.Context()
	env.recheck.writeWait = 100 * time.Millisecond
	env.setSetting(t, config.AuthProviderRecheckIntervalSettingKey, "12h")
	if _, err := env.pool.Exec(ctx, `UPDATE users SET role = 'admin' WHERE id = $1`, env.user.ID); err != nil {
		t.Fatal(err)
	}
	env.user.Role = models.RoleAdmin
	env.localAccount(t, "retry_spare", models.RoleAdmin)
	sessionID, refresh := env.session(t, true)
	if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_identities SET last_check_status = 'unavailable',
		last_checked_at = NOW() - INTERVAL '3 minutes' WHERE id = $1`, env.identityID); err != nil {
		t.Fatal(err)
	}
	env.checker.respond = func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
		return &pluginv1.CheckAccountResponse{
			Status: pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE,
			Account: &pluginv1.AuthenticateResponse{
				ManagedRole:  pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_USER,
				RefreshState: refreshState("rt-2"),
			},
		}, nil
	}
	holder, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, adminRoleLock); err != nil {
		t.Fatal(err)
	}
	_, refreshErr := env.svc.Refresh(ctx, refresh)
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if refreshErr == nil || errors.Is(refreshErr, ErrSessionRevoked) {
		t.Fatalf("blocked role sync = %v, want a failure", refreshErr)
	}
	identity := env.identityState(t)
	if identity.LastCheckStatus != CheckStatusActive || identity.LastCheckedAt != nil || env.storedToken(t) != "rt-2" {
		t.Fatal("failed role sync lost the rotated state or did not leave the active answer due")
	}
	user, err := NewUserRepository(env.pool).GetByID(ctx, env.user.ID)
	if err != nil || user.Role != models.RoleAdmin || env.sessionRow(t, sessionID).RevokedAt != nil {
		t.Fatalf("failed role sync changed the role or revoked the session: user = %+v, err = %v", user, err)
	}
	if _, err := env.svc.Refresh(ctx, refresh); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("retried demotion = %v, want ErrSessionRevoked", err)
	}
	if env.checker.callCount() != 2 || env.checker.states[1] != "rt-2" {
		t.Fatalf("calls = %d, states = %v; want a second check with rt-2", env.checker.callCount(), env.checker.states)
	}
	user, err = NewUserRepository(env.pool).GetByID(ctx, env.user.ID)
	if err != nil || user.Role != models.RoleUser || env.sessionRow(t, sessionID).RevokedAt == nil {
		t.Fatalf("retried role sync did not demote and revoke the session: user = %+v, err = %v", user, err)
	}
}
