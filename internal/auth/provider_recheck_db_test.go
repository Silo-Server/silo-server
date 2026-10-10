package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/migrations"
)

// fakeChecker is a plugin's CheckAccount: it records what the host sent and
// answers with respond.
type fakeChecker struct {
	mu      sync.Mutex
	calls   int
	states  []string // the refresh_token the host presented, per call
	respond func(ctx context.Context, req *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error)
}

func (f *fakeChecker) CheckAccount(ctx context.Context, req *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
	f.mu.Lock()
	f.calls++
	token := ""
	if req.GetRefreshState() != nil {
		token = req.GetRefreshState().GetFields()["refresh_token"].GetStringValue()
	}
	f.states = append(f.states, token)
	f.mu.Unlock()
	return f.respond(ctx, req)
}

func (f *fakeChecker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// answer is a respond func with a fixed status and, when rotated is not
// empty, a new refresh token.
func answer(checkStatus pluginv1.CheckAccountStatus, rotated string) func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
	return func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
		response := &pluginv1.CheckAccountResponse{Status: checkStatus}
		if rotated != "" {
			response.Account = &pluginv1.AuthenticateResponse{RefreshState: refreshState(rotated)}
		}
		return response, nil
	}
}

type fakeCheckerSource struct {
	checker AccountChecker
	err     error
}

func (s fakeCheckerSource) AccountChecker(context.Context, int) (AccountChecker, error) {
	return s.checker, s.err
}

func refreshState(token string) *structpb.Struct {
	state, err := structpb.NewStruct(map[string]any{"refresh_token": token})
	if err != nil {
		panic(err)
	}
	return state
}

// recheckEnv is an external sign-in env with a secret cipher, an auth
// service whose refresh re-checks, and a person signed in through the
// provider.
type recheckEnv struct {
	*externalSignInEnv
	svc        *Service
	jwt        *JWTService
	checker    *fakeChecker
	recheck    *ProviderRecheck
	user       *models.User
	identityID int64
}

const recheckRefreshExpiry = 30 * 24 * time.Hour

func newRecheckEnv(t *testing.T, label string, initialToken string) *recheckEnv {
	t.Helper()
	base := newExternalSignInEnv(t)
	t.Cleanup(saveSettings(t, base.pool, config.AuthProviderRecheckIntervalSettingKey, config.AuthProviderRecheckOutagePolicySettingKey))
	cipher, err := secret.New([]byte("provider-recheck-test-secret-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	base.resolver.WithSecretCipher(cipher)
	env := &recheckEnv{externalSignInEnv: base, checker: &fakeChecker{respond: answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE, "")}}
	env.recheck = NewProviderRecheck(base.resolver, fakeCheckerSource{checker: env.checker})
	users := NewUserRepository(base.pool)
	sessions := NewSessionRepository(base.pool)
	env.jwt = NewJWTService("provider-recheck-test-jwt-secret", time.Hour, recheckRefreshExpiry)
	env.svc = NewService(NewLocalProvider(users, sessions), env.jwt, sessions, users, nil, nil, nil)
	env.svc.SetProviderRecheck(env.recheck)

	identity := base.identity(label)
	if initialToken != "" {
		identity.RefreshState = refreshState(initialToken)
	}
	user, identityID, err := base.resolver.Resolve(t.Context(), ResolveInput{InstallationID: base.installationID, AutoProvision: true, Identity: identity})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	env.user, env.identityID = user, identityID
	return env
}

// session opens a login session through the identity (or a local one when
// withIdentity is false) and returns its id and refresh token.
func (e *recheckEnv) session(t *testing.T, withIdentity bool) (string, string) {
	t.Helper()
	session := models.AuthSession{ID: uuid.New().String(), UserID: e.user.ID, DeviceName: "recheck-test", ExpiresAt: time.Now().Add(recheckRefreshExpiry)}
	if withIdentity {
		session.IdentityID = &e.identityID
	}
	if err := e.svc.sessions.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	pair, err := e.svc.generateTokenPair(Claims{UserID: e.user.ID, Role: e.user.Role, SessionID: session.ID})
	if err != nil {
		t.Fatal(err)
	}
	return session.ID, pair.RefreshToken
}

// makeDue ages the identity's last provider answer past the interval.
func (e *recheckEnv) makeDue(t *testing.T) {
	t.Helper()
	if _, err := e.pool.Exec(t.Context(), `UPDATE plugin_auth_identities SET last_checked_at = NOW() - INTERVAL '13 hours' WHERE id = $1`, e.identityID); err != nil {
		t.Fatal(err)
	}
}

// ageUnavailable moves the identity's last (unavailable) answer past the
// retry wait.
func (e *recheckEnv) ageUnavailable(t *testing.T) {
	t.Helper()
	if _, err := e.pool.Exec(t.Context(), `UPDATE plugin_auth_identities SET last_checked_at = NOW() - $2::interval WHERE id = $1`,
		e.identityID, (providerRecheckRetryWait + time.Second).String()); err != nil {
		t.Fatal(err)
	}
}

func (e *recheckEnv) identityState(t *testing.T) *LinkedIdentity {
	t.Helper()
	identity, err := scanIdentity(e.pool.QueryRow(t.Context(), `SELECT `+identityColumns+` FROM plugin_auth_identities WHERE id = $1`, e.identityID))
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func (e *recheckEnv) storedToken(t *testing.T) string {
	t.Helper()
	state, err := e.resolver.loadRefreshState(t.Context(), e.pool, e.identityID)
	if err != nil {
		t.Fatal(err)
	}
	if state == nil {
		return ""
	}
	return state.GetFields()["refresh_token"].GetStringValue()
}

func (e *recheckEnv) sessionRow(t *testing.T, id string) *models.AuthSession {
	t.Helper()
	session, err := e.svc.sessions.GetByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// TestProviderRecheckStoresEncryptedRefreshStateDB: the sign-in's
// refresh_state is stored encrypted and bound to the identity row; an
// empty Struct clears it and an unset one keeps it.
func TestProviderRecheckStoresEncryptedRefreshStateDB(t *testing.T) {
	env := newRecheckEnv(t, "rosa", "rt-initial")
	var stored string
	if err := env.pool.QueryRow(t.Context(), `SELECT refresh_state FROM plugin_auth_identities WHERE id = $1`, env.identityID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !secret.IsEncrypted(stored) || strings.Contains(stored, "rt-initial") {
		t.Fatalf("stored refresh_state is not ciphertext: %q", stored)
	}
	if got := env.storedToken(t); got != "rt-initial" {
		t.Fatalf("decrypted token = %q", got)
	}
	// Moved to another row, the ciphertext does not decrypt.
	if _, err := env.resolver.cipher.Decrypt(stored, refreshStateAAD(env.identityID+1)); err == nil {
		t.Fatal("ciphertext decrypted under another row's AAD")
	}

	identity := env.identity("rosa")
	if _, _, err := env.resolver.Resolve(t.Context(), ResolveInput{InstallationID: env.installationID, Identity: identity}); err != nil {
		t.Fatal(err)
	}
	if got := env.storedToken(t); got != "rt-initial" {
		t.Fatalf("unset refresh_state replaced the stored one: %q", got)
	}
	identity.RefreshState = &structpb.Struct{}
	if _, _, err := env.resolver.Resolve(t.Context(), ResolveInput{InstallationID: env.installationID, Identity: identity}); err != nil {
		t.Fatal(err)
	}
	if got := env.storedToken(t); got != "" {
		t.Fatalf("empty refresh_state kept %q", got)
	}
}

// TestProviderRecheckActiveDB: a due refresh asks the provider with the
// stored state, stores the rotated state and the answer, and slides the
// session; the next refresh inside the interval does not ask again.
func TestProviderRecheckActiveDB(t *testing.T) {
	env := newRecheckEnv(t, "amos", "rt-1")
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE, "rt-2")
	sessionID, refresh := env.session(t, true)

	// The sign-in itself counts as an active answer: no check yet.
	if _, err := env.svc.Refresh(t.Context(), refresh); err != nil {
		t.Fatal(err)
	}
	if env.checker.callCount() != 0 {
		t.Fatalf("checked right after sign-in")
	}

	env.makeDue(t)
	if _, err := env.pool.Exec(t.Context(), `UPDATE auth_sessions SET expires_at = NOW() + INTERVAL '1 day' WHERE id = $1`, sessionID); err != nil {
		t.Fatal(err)
	}
	pair, err := env.svc.Refresh(t.Context(), refresh)
	if err != nil {
		t.Fatal(err)
	}
	if env.checker.callCount() != 1 || env.checker.states[0] != "rt-1" {
		t.Fatalf("calls = %d, states = %v", env.checker.callCount(), env.checker.states)
	}
	if got := env.storedToken(t); got != "rt-2" {
		t.Fatalf("rotated token not stored: %q", got)
	}
	identity := env.identityState(t)
	if identity.LastCheckStatus != CheckStatusActive || identity.LastCheckedAt == nil || time.Since(*identity.LastCheckedAt) > time.Minute {
		t.Fatalf("identity check = %q at %v", identity.LastCheckStatus, identity.LastCheckedAt)
	}
	if session := env.sessionRow(t, sessionID); time.Until(session.ExpiresAt) < 29*24*time.Hour {
		t.Fatalf("session did not slide: %v", session.ExpiresAt)
	}

	if _, err := env.svc.Refresh(t.Context(), pair.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if env.checker.callCount() != 1 {
		t.Fatalf("checked again inside the interval")
	}

	// The next due check presents the rotated token.
	env.makeDue(t)
	if _, err := env.svc.Refresh(t.Context(), pair.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if env.checker.callCount() != 2 || env.checker.states[1] != "rt-2" {
		t.Fatalf("second call states = %v", env.checker.states)
	}
}

// TestProviderRecheckRefusedAccountRevokesSessionsDB: not found, disabled
// and not permitted revoke every login session of the account, local ones
// included, and refuse the refresh.
func TestProviderRecheckRefusedAccountRevokesSessionsDB(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status pluginv1.CheckAccountStatus
		want   string
	}{
		{"not_found", pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_FOUND, CheckStatusNotFound},
		{"disabled", pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_DISABLED, CheckStatusDisabled},
		{"not_permitted", pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_PERMITTED, CheckStatusNotPermitted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newRecheckEnv(t, "ref"+tc.name, "rt-1")
			env.checker.respond = answer(tc.status, "")
			providerSession, refresh := env.session(t, true)
			localSession, _ := env.session(t, false)
			key, err := NewAPIKeyRepository(env.pool).Create(t.Context(), env.user.ID, "recheck", nil)
			if err != nil {
				t.Fatal(err)
			}
			env.makeDue(t)

			if _, err := env.svc.Refresh(t.Context(), refresh); !errors.Is(err, ErrSessionRevoked) {
				t.Fatalf("refresh = %v, want ErrSessionRevoked", err)
			}
			for _, id := range []string{providerSession, localSession} {
				if env.sessionRow(t, id).RevokedAt == nil {
					t.Fatalf("session %s not revoked", id)
				}
			}
			if len(env.revoked) != 1 || env.revoked[0] != env.user.ID {
				t.Fatalf("revocation hook = %v", env.revoked)
			}
			// API keys act without a session to re-check, so they go too.
			if _, err := NewAPIKeyRepository(env.pool).GetByKey(t.Context(), key.Key); err == nil {
				t.Fatal("the refused account kept its API key")
			}
			if got := env.identityState(t).LastCheckStatus; got != tc.want {
				t.Fatalf("status = %q", got)
			}
			// The account itself stays; a later sign-in the provider accepts
			// works again.
			user, err := NewUserRepository(env.pool).GetByID(t.Context(), env.user.ID)
			if err != nil || !user.Enabled {
				t.Fatalf("account = %+v, %v", user, err)
			}
		})
	}
}

// TestProviderRecheckRoleSyncDB: an active answer applies the managed role;
// a change revokes the account's sessions, this one included.
func TestProviderRecheckRoleSyncDB(t *testing.T) {
	env := newRecheckEnv(t, "rolf", "rt-1")
	env.checker.respond = func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
		return &pluginv1.CheckAccountResponse{
			Status: pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE,
			Account: &pluginv1.AuthenticateResponse{
				ManagedRole: pluginv1.AuthManagedRole_AUTH_MANAGED_ROLE_ADMIN,
				// Ignored by the host in a CheckAccount answer.
				ExternalSubject: "someone-else",
				Denial:          pluginv1.AuthDenial_AUTH_DENIAL_NOT_PERMITTED,
				DisplayName:     "Rolf Renamed",
			},
		}, nil
	}
	sessionID, refresh := env.session(t, true)
	env.makeDue(t)
	if _, err := env.svc.Refresh(t.Context(), refresh); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("refresh = %v, want ErrSessionRevoked", err)
	}
	user, err := NewUserRepository(env.pool).GetByID(t.Context(), env.user.ID)
	if err != nil || user.Role != models.RoleAdmin {
		t.Fatalf("role = %v, %v", user, err)
	}
	if env.sessionRow(t, sessionID).RevokedAt == nil {
		t.Fatal("role change left the session")
	}
	identity := env.identityState(t)
	if identity.LastCheckStatus != CheckStatusActive || identity.DisplayName != "Rolf Renamed" || identity.ExternalSubject != env.identity("rolf").Subject {
		t.Fatalf("identity = %+v", identity)
	}

	// The same role again changes nothing and keeps the next session.
	_, refresh = env.session(t, true)
	env.makeDue(t)
	if _, err := env.svc.Refresh(t.Context(), refresh); err != nil {
		t.Fatalf("refresh with unchanged role = %v", err)
	}
}

// TestProviderRecheckUnsupportedDB: a provider that cannot re-check the
// account (UNSUPPORTED, Unimplemented, or a plugin without CheckAccount)
// stops the sliding: the session ends an absolute age after it opened.
func TestProviderRecheckUnsupportedDB(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source func(*fakeChecker) AccountCheckerSource
	}{
		{"unsupported", func(c *fakeChecker) AccountCheckerSource {
			c.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSUPPORTED, "")
			return fakeCheckerSource{checker: c}
		}},
		{"unimplemented", func(c *fakeChecker) AccountCheckerSource {
			c.respond = func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
				return nil, status.Error(codes.Unimplemented, "unknown method CheckAccount")
			}
			return fakeCheckerSource{checker: c}
		}},
		{"no_check_account", func(*fakeChecker) AccountCheckerSource { return fakeCheckerSource{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newRecheckEnv(t, "uns"+tc.name, "")
			env.recheck.source = tc.source(env.checker)
			sessionID, refresh := env.session(t, true)
			opened := time.Now().Add(-29 * 24 * time.Hour)
			if _, err := env.pool.Exec(t.Context(), `UPDATE auth_sessions SET created_at = $2, expires_at = NOW() + INTERVAL '20 days' WHERE id = $1`, sessionID, opened); err != nil {
				t.Fatal(err)
			}
			env.makeDue(t)
			pair, err := env.svc.Refresh(t.Context(), refresh)
			if err != nil {
				t.Fatal(err)
			}
			if got := env.identityState(t).LastCheckStatus; got != CheckStatusUnsupported {
				t.Fatalf("status = %q", got)
			}
			limit := opened.Add(recheckRefreshExpiry)
			if expires := env.sessionRow(t, sessionID).ExpiresAt; expires.After(limit.Add(time.Second)) {
				t.Fatalf("expires_at = %v, beyond the absolute limit %v", expires, limit)
			}

			// Past the absolute age the refresh is refused, without asking
			// the provider again inside the interval.
			calls := env.checker.callCount()
			if _, err := env.pool.Exec(t.Context(), `UPDATE auth_sessions SET created_at = NOW() - INTERVAL '31 days' WHERE id = $1`, sessionID); err != nil {
				t.Fatal(err)
			}
			if _, err := env.svc.Refresh(t.Context(), pair.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
				t.Fatalf("refresh past the absolute age = %v", err)
			}
			if env.checker.callCount() != calls {
				t.Fatal("asked the provider again inside the interval")
			}
		})
	}
}

// TestProviderRecheckUnavailableDB: an unreachable provider keeps the
// session (fail_open, the default) and is asked again at the next refresh;
// fail_closed refuses the refresh without revoking the session.
func TestProviderRecheckUnavailableDB(t *testing.T) {
	answers := map[string]func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error){
		"unavailable": answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNAVAILABLE, ""),
		"unspecified": answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSPECIFIED, ""),
		"unknown":     answer(pluginv1.CheckAccountStatus(99), ""),
		"rpc_error": func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
			return nil, status.Error(codes.Unavailable, "connection refused")
		},
		"timeout": func(ctx context.Context, _ *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
			<-ctx.Done()
			return nil, status.FromContextError(ctx.Err()).Err()
		},
	}
	for name, respond := range answers {
		t.Run(name, func(t *testing.T) {
			env := newRecheckEnv(t, "una"+name, "rt-1")
			env.recheck.timeout = 100 * time.Millisecond
			env.checker.respond = respond
			sessionID, refresh := env.session(t, true)
			env.makeDue(t)

			pair, err := env.svc.Refresh(t.Context(), refresh)
			if err != nil {
				t.Fatalf("fail-open refresh = %v", err)
			}
			if got := env.identityState(t).LastCheckStatus; got != CheckStatusUnavailable {
				t.Fatalf("status = %q", got)
			}
			if got := env.storedToken(t); got != "rt-1" {
				t.Fatalf("stored token changed to %q", got)
			}
			// Refreshes within the retry wait use the stored answer; the
			// next one after it asks again.
			if _, err := env.svc.Refresh(t.Context(), pair.RefreshToken); err != nil || env.checker.callCount() != 1 {
				t.Fatalf("refresh within the retry wait: calls = %d, %v", env.checker.callCount(), err)
			}
			env.ageUnavailable(t)
			if _, err := env.svc.Refresh(t.Context(), pair.RefreshToken); err != nil {
				t.Fatal(err)
			}
			if env.checker.callCount() != 2 {
				t.Fatalf("calls = %d, want a retry", env.checker.callCount())
			}

			env.setSetting(t, config.AuthProviderRecheckOutagePolicySettingKey, config.AuthRecheckFailClosed)
			if _, err := env.svc.Refresh(t.Context(), pair.RefreshToken); !errors.Is(err, ErrProviderUnavailable) {
				t.Fatalf("fail-closed refresh = %v", err)
			}
			if env.sessionRow(t, sessionID).RevokedAt != nil {
				t.Fatal("fail-closed revoked the session")
			}
			// Once the provider answers again the same refresh token works.
			env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE, "")
			env.ageUnavailable(t)
			if _, err := env.svc.Refresh(t.Context(), pair.RefreshToken); err != nil {
				t.Fatalf("refresh after recovery = %v", err)
			}
		})
	}
}

// TestProviderRecheckIntervalSettingDB: the interval comes from
// auth.provider_recheck_interval.
func TestProviderRecheckIntervalSettingDB(t *testing.T) {
	env := newRecheckEnv(t, "ivan", "rt-1")
	_, refresh := env.session(t, true)
	if _, err := env.pool.Exec(t.Context(), `UPDATE plugin_auth_identities SET last_checked_at = NOW() - INTERVAL '2 hours' WHERE id = $1`, env.identityID); err != nil {
		t.Fatal(err)
	}
	pair, err := env.svc.Refresh(t.Context(), refresh)
	if err != nil || env.checker.callCount() != 0 {
		t.Fatalf("default 12h interval: calls = %d, err = %v", env.checker.callCount(), err)
	}
	env.setSetting(t, config.AuthProviderRecheckIntervalSettingKey, "1h")
	if _, err := env.svc.Refresh(t.Context(), pair.RefreshToken); err != nil || env.checker.callCount() != 1 {
		t.Fatalf("1h interval: calls = %d, err = %v", env.checker.callCount(), err)
	}
}

// TestProviderRecheckConcurrentRefreshesDB: two refreshes of the same
// identity (two devices, possibly on two nodes) make one CheckAccount call;
// the one that waited sees the stored answer.
func TestProviderRecheckConcurrentRefreshesDB(t *testing.T) {
	env := newRecheckEnv(t, "cora", "rt-1")
	var inFlight, maxInFlight atomic.Int32
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	env.checker.respond = func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
		n := inFlight.Add(1)
		for {
			old := maxInFlight.Load()
			if n <= old || maxInFlight.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		inFlight.Add(-1)
		return &pluginv1.CheckAccountResponse{
			Status:  pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE,
			Account: &pluginv1.AuthenticateResponse{RefreshState: refreshState("rt-2")},
		}, nil
	}
	_, refreshA := env.session(t, true)
	_, refreshB := env.session(t, true)
	env.makeDue(t)

	// A second service stands in for another node: separate objects, same
	// database.
	users := NewUserRepository(env.pool)
	sessions := NewSessionRepository(env.pool)
	otherNode := NewService(NewLocalProvider(users, sessions), env.jwt, sessions, users, nil, nil, nil)
	otherNode.SetProviderRecheck(NewProviderRecheck(env.resolver, fakeCheckerSource{checker: env.checker}))

	errs := make(chan error, 2)
	go func() { _, err := env.svc.Refresh(context.Background(), refreshA); errs <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first check never reached the plugin")
	}
	go func() { _, err := otherNode.Refresh(context.Background(), refreshB); errs <- err }()
	// Give the second refresh time to reach the lock before releasing.
	waitForLockWaiter(t, env)
	close(release)
	for range 2 {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("refresh timed out")
		}
	}
	if env.checker.callCount() != 1 || maxInFlight.Load() != 1 {
		t.Fatalf("calls = %d, max in flight = %d", env.checker.callCount(), maxInFlight.Load())
	}
	if got := env.storedToken(t); got != "rt-2" {
		t.Fatalf("stored token = %q", got)
	}
}

// waitForLockWaiter waits until a backend is blocked on an advisory lock,
// which is the second refresh queued behind the first check.
func waitForLockWaiter(t *testing.T, env *recheckEnv) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := env.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND NOT granted)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("second refresh never waited for the identity lock")
}

// TestProviderRecheckLockTimeoutFailsOpenDB: a refresh that cannot get the
// identity's lock in time counts the provider as unavailable.
func TestProviderRecheckLockTimeoutFailsOpenDB(t *testing.T) {
	env := newRecheckEnv(t, "lola", "rt-1")
	env.recheck.lockWait = 200 * time.Millisecond
	_, refresh := env.session(t, true)
	env.makeDue(t)

	holder, err := env.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	subject := env.identity("lola").Subject
	if err := lockExternalSubject(t.Context(), holder, env.installationID, subject); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.Refresh(t.Context(), refresh); err != nil {
		t.Fatalf("refresh while locked = %v", err)
	}
	if env.checker.callCount() != 0 {
		t.Fatal("called the plugin without the lock")
	}
	env.setSetting(t, config.AuthProviderRecheckOutagePolicySettingKey, config.AuthRecheckFailClosed)
	if _, err := env.svc.Refresh(t.Context(), refresh); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("fail-closed refresh while locked = %v", err)
	}
}

// TestProviderRecheckSessionIdentityDB: sessions carry the identity they
// came from: external sign-ins and device sign-ins approved from them do,
// local and break-glass sessions do not, and linking attaches the
// account's open sessions unless it is break-glass.
func TestProviderRecheckSessionIdentityDB(t *testing.T) {
	env := newRecheckEnv(t, "sian", "")
	ctx := t.Context()

	// Linking attaches the open local sessions of an ordinary account.
	local := env.localAccount(t, "linker", models.RoleUser)
	openLocal := models.AuthSession{ID: uuid.New().String(), UserID: local.ID, ExpiresAt: time.Now().Add(time.Hour)}
	if err := env.svc.sessions.Create(ctx, openLocal); err != nil {
		t.Fatal(err)
	}
	_, linkedID, err := env.resolver.Resolve(ctx, ResolveInput{InstallationID: env.installationID, Identity: env.identity("linker-idp"), LinkingUserID: local.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := env.sessionRow(t, openLocal.ID).IdentityID; got == nil || *got != linkedID {
		t.Fatalf("linked account's session identity = %v, want %d", got, linkedID)
	}

	// A break-glass admin's local sessions stay local.
	admin := env.localAccount(t, "glass", models.RoleAdmin)
	if _, err := env.pool.Exec(ctx, `UPDATE users SET break_glass = true WHERE id = $1`, admin.ID); err != nil {
		t.Fatal(err)
	}
	adminSession := models.AuthSession{ID: uuid.New().String(), UserID: admin.ID, ExpiresAt: time.Now().Add(time.Hour)}
	if err := env.svc.sessions.Create(ctx, adminSession); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.resolver.Resolve(ctx, ResolveInput{InstallationID: env.installationID, Identity: env.identity("glass-idp"), LinkingUserID: admin.ID}); err != nil {
		t.Fatal(err)
	}
	if got := env.sessionRow(t, adminSession.ID).IdentityID; got != nil {
		t.Fatalf("break-glass session identity = %d", *got)
	}

	// A device approved from a provider session inherits its identity.
	providerSession, _ := env.session(t, true)
	localSession, _ := env.session(t, false)
	devices := NewDeviceLoginService(env.pool, NewUserRepository(env.pool), env.jwt, NewSessionRepository(env.pool), nil, nil)
	deviceName := "recheck-device-" + env.suffix
	t.Cleanup(func() {
		_, _ = env.pool.Exec(context.Background(), `DELETE FROM device_login_requests WHERE device_name = $1`, deviceName)
	})
	for _, tc := range []struct {
		name    string
		claims  *Claims
		wantNil bool
	}{
		{"provider session", &Claims{UserID: env.user.ID, SessionID: providerSession, TokenType: TokenTypeAccess}, false},
		{"local session", &Claims{UserID: env.user.ID, SessionID: localSession, TokenType: TokenTypeAccess}, true},
	} {
		start, err := devices.Start(ctx, DeviceLoginStartInput{DeviceName: deviceName, BaseURL: "https://media.example.test/"})
		if err != nil {
			t.Fatal(err)
		}
		if err := devices.Approve(WithClaims(ctx, tc.claims), DeviceLoginLookupInput{UserCode: start.UserCode}, env.user.ID); err != nil {
			t.Fatalf("%s: approve: %v", tc.name, err)
		}
		poll, err := devices.Poll(ctx, start.DeviceCode)
		if err != nil || poll.TokenPair == nil {
			t.Fatalf("%s: poll = %+v, %v", tc.name, poll, err)
		}
		claims, err := env.jwt.ValidateToken(poll.TokenPair.RefreshToken)
		if err != nil {
			t.Fatal(err)
		}
		got := env.sessionRow(t, claims.SessionID).IdentityID
		if tc.wantNil != (got == nil) || (!tc.wantNil && *got != env.identityID) {
			t.Fatalf("%s: device session identity = %v", tc.name, got)
		}
	}

	// Unlinking (or removing the plugin) detaches the identity, and the
	// provider's sessions stop sliding: they end the refresh expiry after
	// the provider last vouched for them.
	if _, err := env.pool.Exec(ctx, `DELETE FROM plugin_auth_identities WHERE id = $1`, env.identityID); err != nil {
		t.Fatal(err)
	}
	session := env.sessionRow(t, providerSession)
	if session.IdentityID != nil || session.ProviderSince == nil {
		t.Fatalf("after unlink: identity %v, provider_since %v", session.IdentityID, session.ProviderSince)
	}
	_, refresh := env.sessionWithChain(t, nil, time.Now().Add(-31*24*time.Hour))
	if _, err := env.svc.Refresh(ctx, refresh); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("refresh of an unlinked provider session past its age = %v", err)
	}
	sessionID, refresh := env.sessionWithChain(t, nil, time.Now().Add(-29*24*time.Hour))
	if _, err := env.svc.Refresh(ctx, refresh); err != nil {
		t.Fatal(err)
	}
	if expires := env.sessionRow(t, sessionID).ExpiresAt; expires.After(time.Now().Add(25 * time.Hour)) {
		t.Fatalf("an unlinked provider session slid to %v", expires)
	}
	// A local session still slides.
	localID, refresh := env.session(t, false)
	if _, err := env.svc.Refresh(ctx, refresh); err != nil {
		t.Fatal(err)
	}
	if expires := env.sessionRow(t, localID).ExpiresAt; expires.Before(time.Now().Add(29 * 24 * time.Hour)) {
		t.Fatalf("a local session did not slide: %v", expires)
	}
}

// sessionWithChain opens a session carrying identityID whose provider chain
// started at since, and returns its id and refresh token.
func (e *recheckEnv) sessionWithChain(t *testing.T, identityID *int64, since time.Time) (string, string) {
	t.Helper()
	session := models.AuthSession{ID: uuid.New().String(), UserID: e.user.ID, DeviceName: "recheck-test",
		ExpiresAt: time.Now().Add(recheckRefreshExpiry), IdentityID: identityID, ProviderSince: &since}
	if err := e.svc.sessions.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	pair, err := e.svc.generateTokenPair(Claims{UserID: e.user.ID, Role: e.user.Role, SessionID: session.ID})
	if err != nil {
		t.Fatal(err)
	}
	return session.ID, pair.RefreshToken
}

// TestProviderRecheckDeviceChainKeepsAbsoluteAgeDB: a device sign-in
// approved from a provider session the provider cannot re-check continues
// that session's absolute age instead of starting a new one, so chaining
// device sign-ins cannot keep access alive without the provider.
func TestProviderRecheckDeviceChainKeepsAbsoluteAgeDB(t *testing.T) {
	env := newRecheckEnv(t, "chain", "")
	ctx := t.Context()
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSUPPORTED, "")
	opened := time.Now().Add(-29 * 24 * time.Hour)
	approver, _ := env.sessionWithChain(t, &env.identityID, opened)
	if _, err := env.pool.Exec(ctx, `UPDATE auth_sessions SET created_at = $2 WHERE id = $1`, approver, opened); err != nil {
		t.Fatal(err)
	}

	devices := NewDeviceLoginService(env.pool, NewUserRepository(env.pool), env.jwt, NewSessionRepository(env.pool), nil, nil)
	deviceName := "chain-device-" + env.suffix
	t.Cleanup(func() {
		_, _ = env.pool.Exec(context.Background(), `DELETE FROM device_login_requests WHERE device_name = $1`, deviceName)
	})
	start, err := devices.Start(ctx, DeviceLoginStartInput{DeviceName: deviceName, BaseURL: "https://media.example.test/"})
	if err != nil {
		t.Fatal(err)
	}
	if err := devices.Approve(WithClaims(ctx, &Claims{UserID: env.user.ID, SessionID: approver, TokenType: TokenTypeAccess}),
		DeviceLoginLookupInput{UserCode: start.UserCode}, env.user.ID); err != nil {
		t.Fatal(err)
	}
	poll, err := devices.Poll(ctx, start.DeviceCode)
	if err != nil || poll.TokenPair == nil {
		t.Fatalf("poll = %+v, %v", poll, err)
	}
	claims, err := env.jwt.ValidateToken(poll.TokenPair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	device := env.sessionRow(t, claims.SessionID)
	if device.IdentityID == nil || *device.IdentityID != env.identityID || device.ProviderSince == nil || device.ProviderSince.Sub(opened).Abs() > time.Second {
		t.Fatalf("device session chain = identity %v since %v, want since %v", device.IdentityID, device.ProviderSince, opened)
	}

	// Within the approver's age the device refreshes, but only up to it.
	env.makeDue(t)
	pair, err := env.svc.Refresh(ctx, poll.TokenPair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if expires := env.sessionRow(t, claims.SessionID).ExpiresAt; expires.After(opened.Add(recheckRefreshExpiry).Add(time.Second)) {
		t.Fatalf("device session expires %v, past the approver's limit %v", expires, opened.Add(recheckRefreshExpiry))
	}
	// Past it the device is refused, like the approver.
	if _, err := env.pool.Exec(ctx, `UPDATE auth_sessions SET provider_since = NOW() - INTERVAL '31 days' WHERE id = $1`, claims.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("device refresh past the original limit = %v", err)
	}
}

// TestProviderRecheckRemotePlaybackHandoffChainDB: a remote-playback handoff
// approved from a provider session gives the device a session carrying the
// approver's identity, which refresh re-checks and a refusal revokes.
func TestProviderRecheckRemotePlaybackHandoffChainDB(t *testing.T) {
	env := newRecheckEnv(t, "handoff", "rt-1")
	ctx := t.Context()
	approver, _ := env.session(t, true)
	stores := pgstore.NewPostgresProvider(env.pool)
	store, err := stores.ForUser(ctx, env.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := store.ListProfiles(ctx)
	if err != nil || len(profiles) == 0 {
		t.Fatalf("profiles = %+v, %v", profiles, err)
	}

	devices := NewDeviceLoginService(env.pool, NewUserRepository(env.pool), env.jwt, NewSessionRepository(env.pool),
		stores, access.NewProfileTokenService("provider-recheck-profile-secret", time.Hour))
	deviceName := "handoff-device-" + env.suffix
	t.Cleanup(func() {
		_, _ = env.pool.Exec(context.Background(), `DELETE FROM device_login_requests WHERE device_name = $1`, deviceName)
	})
	start, err := devices.Start(ctx, DeviceLoginStartInput{DeviceName: deviceName, BaseURL: "https://media.example.test/",
		ClientPurpose: DeviceLoginPurposeRemote, Temporary: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := devices.ApproveRemotePlayback(WithClaims(ctx, &Claims{UserID: env.user.ID, SessionID: approver, TokenType: TokenTypeAccess}),
		DeviceLoginLookupInput{UserCode: start.UserCode}, env.user.ID, profiles[0].ID); err != nil {
		t.Fatal(err)
	}
	poll, err := devices.Poll(ctx, start.DeviceCode)
	if err != nil || poll.TokenPair == nil {
		t.Fatalf("poll = %+v, %v", poll, err)
	}
	claims, err := env.jwt.ValidateToken(poll.TokenPair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	device := env.sessionRow(t, claims.SessionID)
	if device.IdentityID == nil || *device.IdentityID != env.identityID || device.ProviderSince == nil {
		t.Fatalf("handoff session chain = identity %v since %v, want identity %d", device.IdentityID, device.ProviderSince, env.identityID)
	}

	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_FOUND, "")
	env.makeDue(t)
	if _, err := env.svc.Refresh(ctx, poll.TokenPair.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("handoff refresh after not_found = %v, want ErrSessionRevoked", err)
	}
	if env.checker.callCount() != 1 {
		t.Fatalf("provider asked %d times, want 1", env.checker.callCount())
	}
	if env.sessionRow(t, claims.SessionID).RevokedAt == nil {
		t.Fatal("handoff session not revoked")
	}
}

// TestProviderRecheckProviderNotLoadedDB: a node without the provider does
// not store an answer for the whole cluster. When the bindings say the
// provider is off, the session stops sliding for this refresh; when they say
// it is on (this node is behind), the provider counts as unavailable.
func TestProviderRecheckProviderNotLoadedDB(t *testing.T) {
	env := newRecheckEnv(t, "nolo", "rt-1")
	ctx := t.Context()
	env.recheck.source = fakeCheckerSource{err: ErrProviderNotLoaded}
	sessionID, refresh := env.session(t, true)
	opened := time.Now().Add(-29 * 24 * time.Hour)
	if _, err := env.pool.Exec(ctx, `UPDATE auth_sessions SET created_at = $2, provider_since = $2 WHERE id = $1`, sessionID, opened); err != nil {
		t.Fatal(err)
	}
	env.makeDue(t)
	before := env.identityState(t)

	// The binding is off (newExternalSignInEnv seeds it disabled).
	pair, err := env.svc.Refresh(ctx, refresh)
	if err != nil {
		t.Fatal(err)
	}
	if expires := env.sessionRow(t, sessionID).ExpiresAt; expires.After(opened.Add(recheckRefreshExpiry).Add(time.Second)) {
		t.Fatalf("expires_at = %v, beyond the absolute limit", expires)
	}
	after := env.identityState(t)
	if after.LastCheckStatus != before.LastCheckStatus || !after.LastCheckedAt.Equal(*before.LastCheckedAt) {
		t.Fatalf("a node without the provider stored %q at %v", after.LastCheckStatus, after.LastCheckedAt)
	}

	// The binding is on, but this node has not loaded it: unavailable,
	// still not stored.
	if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, env.installationID); err != nil {
		t.Fatal(err)
	}
	env.setSetting(t, config.AuthProviderRecheckOutagePolicySettingKey, config.AuthRecheckFailClosed)
	if _, err := env.svc.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("refresh on a node behind the bindings = %v", err)
	}
	if got := env.identityState(t).LastCheckStatus; got != before.LastCheckStatus {
		t.Fatalf("status stored = %q", got)
	}
}

// TestProviderRecheckConcurrencyCapDB: a node whose re-check slots are all
// taken does not queue more database connections behind the provider; the
// refresh counts the provider as unavailable.
func TestProviderRecheckConcurrencyCapDB(t *testing.T) {
	env := newRecheckEnv(t, "capa", "rt-1")
	_, refresh := env.session(t, true)
	env.makeDue(t)
	for range cap(env.recheck.slots) {
		env.recheck.slots <- struct{}{}
	}
	if _, err := env.svc.Refresh(t.Context(), refresh); err != nil {
		t.Fatalf("fail-open refresh with no free slot = %v", err)
	}
	if env.checker.callCount() != 0 {
		t.Fatal("asked the provider without a free slot")
	}
	env.setSetting(t, config.AuthProviderRecheckOutagePolicySettingKey, config.AuthRecheckFailClosed)
	if _, err := env.svc.Refresh(t.Context(), refresh); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("fail-closed refresh with no free slot = %v", err)
	}
}

// TestProviderRecheckKeepsRotatedStateOnLaterFailureDB: when applying an
// answer fails (here the account row stays locked past the lock wait), the
// rotated refresh_state and the answer are still stored, so the next call
// presents the token the provider now expects.
func TestProviderRecheckKeepsRotatedStateOnLaterFailureDB(t *testing.T) {
	env := newRecheckEnv(t, "kira", "rt-1")
	env.recheck.writeWait = 200 * time.Millisecond
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE, "rt-2")
	_, refresh := env.session(t, true)
	env.makeDue(t)

	holder, err := env.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// FOR NO KEY UPDATE lets the identity's foreign-key checks through and
	// blocks only the account lock role sync takes.
	if _, err := holder.Exec(t.Context(), `SELECT id FROM users WHERE id = $1 FOR NO KEY UPDATE`, env.user.ID); err != nil {
		t.Fatal(err)
	}
	_, err = env.svc.Refresh(t.Context(), refresh)
	_ = holder.Rollback(context.Background())
	if err == nil || errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("refresh with the account locked = %v, want a failure", err)
	}
	if got := env.storedToken(t); got != "rt-2" {
		t.Fatalf("rotated token lost: %q", got)
	}
	if got := env.identityState(t).LastCheckStatus; got != CheckStatusActive {
		t.Fatalf("status = %q", got)
	}
	// The role sync did not run, so the check stays due: the next refresh
	// asks again with the rotated token.
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE, "rt-3")
	if _, err := env.svc.Refresh(t.Context(), refresh); err != nil {
		t.Fatalf("refresh after the lock = %v", err)
	}
	if env.checker.callCount() != 2 || env.checker.states[1] != "rt-2" {
		t.Fatalf("calls = %d, states = %v; want a second check with rt-2", env.checker.callCount(), env.checker.states)
	}
}

// TestProviderRecheckEmptyProfileFieldsKeepStoredDB: an ACTIVE answer's
// empty profile fields mean the provider did not say, so they never clear
// what is stored; the fields it fills replace the stored values.
func TestProviderRecheckEmptyProfileFieldsKeepStoredDB(t *testing.T) {
	env := newRecheckEnv(t, "kept", "")
	before := env.identityState(t)
	if before.Username == "" || before.Email == "" || before.DisplayName == "" || before.Issuer == "" {
		t.Fatalf("sign-in stored no profile: %+v", before)
	}
	_, refresh := env.session(t, true)

	env.checker.respond = func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
		return &pluginv1.CheckAccountResponse{Status: pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE,
			Account: &pluginv1.AuthenticateResponse{Username: " ", Email: "", DisplayName: ""}}, nil
	}
	env.makeDue(t)
	pair, err := env.svc.Refresh(t.Context(), refresh)
	if err != nil {
		t.Fatal(err)
	}
	after := env.identityState(t)
	if after.Username != before.Username || after.Email != before.Email || after.DisplayName != before.DisplayName || after.Issuer != before.Issuer {
		t.Fatalf("empty answer cleared the profile: before %+v, after %+v", before, after)
	}

	env.checker.respond = func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
		return &pluginv1.CheckAccountResponse{Status: pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE,
			Account: &pluginv1.AuthenticateResponse{DisplayName: "Renamed Person"}}, nil
	}
	env.makeDue(t)
	if _, err := env.svc.Refresh(t.Context(), pair.RefreshToken); err != nil {
		t.Fatal(err)
	}
	after = env.identityState(t)
	if after.DisplayName != "Renamed Person" || after.Email != before.Email || after.Username != before.Username {
		t.Fatalf("partial answer = %+v", after)
	}
}

// TestProviderRecheckScheduledPassDB: the scheduled pass asks the provider
// about identities whose accounts hold a credential no refresh re-checks
// (an API key, an Audiobookshelf session, an idle login session), once
// their last answer is older than the interval, with the refresh's
// outcomes: a refused account also loses its API keys and Audiobookshelf
// sessions. Break-glass accounts and identities of a provider that is no
// longer enabled are left alone.
func TestProviderRecheckScheduledPassDB(t *testing.T) {
	env := newRecheckEnv(t, "idle", "rt-1")
	ctx := t.Context()
	pool := env.pool
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	due := func() bool {
		t.Helper()
		ok, err := env.recheck.IdleRecheckDue(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	run := func() map[string]int {
		t.Helper()
		counts, err := env.recheck.RecheckIdleIdentities(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return counts
	}
	exec(`UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, env.installationID)

	// Due, but the account holds nothing the pass must re-check.
	env.makeDue(t)
	if due() {
		t.Fatal("an account without credentials is due")
	}

	// An API key makes it due; an active answer keeps the key.
	exec(`INSERT INTO api_keys (user_id, label, api_key, revision) VALUES ($1, 'idle', $2, 1)`, env.user.ID, "sa_idle_"+env.suffix)
	if !due() {
		t.Fatal("an account with an API key is not due")
	}
	if counts := run(); counts[CheckStatusActive] != 1 || env.checker.callCount() != 1 || env.checker.states[0] != "rt-1" {
		t.Fatalf("counts = %v, calls = %d, states = %v", counts, env.checker.callCount(), env.checker.states)
	}
	if count(`SELECT COUNT(*) FROM api_keys WHERE user_id = $1`, env.user.ID) != 1 {
		t.Fatal("an active answer deleted the API key")
	}
	// Inside the interval nothing is asked again.
	if due() || len(run()) != 0 || env.checker.callCount() != 1 {
		t.Fatal("re-checked inside the interval")
	}

	// A break-glass account keeps local credentials of its own: skipped.
	env.makeDue(t)
	exec(`UPDATE users SET role = 'admin', break_glass = true WHERE id = $1`, env.user.ID)
	if due() {
		t.Fatal("a break-glass account is due")
	}
	exec(`UPDATE users SET break_glass = false, role = 'user' WHERE id = $1`, env.user.ID)

	// A provider that is no longer enabled cannot be asked: skipped.
	exec(`UPDATE plugin_auth_bindings SET enabled = false WHERE plugin_installation_id = $1`, env.installationID)
	if due() {
		t.Fatal("an identity of a disabled provider is due")
	}
	exec(`UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, env.installationID)

	// An idle login session opened through the identity is enough too.
	exec(`DELETE FROM api_keys WHERE user_id = $1`, env.user.ID)
	if due() {
		t.Fatal("due without a credential")
	}
	sessionID, _ := env.session(t, true)
	if !due() {
		t.Fatal("an account with an idle provider session is not due")
	}

	// A refused account loses its login and Audiobookshelf sessions and its
	// API keys.
	exec(`INSERT INTO api_keys (user_id, label, api_key, revision) VALUES ($1, 'idle', $2, 1)`, env.user.ID, "sa_idle2_"+env.suffix)
	exec(`INSERT INTO abs_sessions (user_id, token_hash, device_id) VALUES ($1, $2, 'abs-device')`, env.user.ID, "abs-"+env.suffix)
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_FOUND, "")
	if counts := run(); counts[CheckStatusNotFound] != 1 {
		t.Fatalf("counts = %v", counts)
	}
	if count(`SELECT COUNT(*) FROM api_keys WHERE user_id = $1`, env.user.ID) != 0 {
		t.Fatal("a refused account kept its API key")
	}
	if count(`SELECT COUNT(*) FROM abs_sessions WHERE user_id = $1 AND revoked_at IS NULL`, env.user.ID) != 0 {
		t.Fatal("a refused account kept its Audiobookshelf session")
	}
	if session := env.sessionRow(t, sessionID); session.RevokedAt == nil {
		t.Fatal("a refused account kept its login session")
	}
	if env.identityState(t).LastCheckStatus != CheckStatusNotFound {
		t.Fatalf("identity status = %q", env.identityState(t).LastCheckStatus)
	}
	if len(env.revoked) == 0 || env.revoked[len(env.revoked)-1] != env.user.ID {
		t.Fatalf("revocation hook = %v", env.revoked)
	}
	// Nothing is left to re-check.
	env.makeDue(t)
	if due() {
		t.Fatal("a revoked account is still due")
	}

	// A live Audiobookshelf session alone makes it due.
	exec(`INSERT INTO abs_sessions (user_id, token_hash, device_id) VALUES ($1, $2, 'abs-device')`, env.user.ID, "abs2-"+env.suffix)
	if !due() {
		t.Fatal("an account with an Audiobookshelf session is not due")
	}
}

// TestProviderRecheckRolledBackRevocationStoresNothingDB: a refusal's
// answer and the refresh_state it clears commit only with the revocation.
// When the revocation fails (here a login session row stays locked past the
// write wait), the identity keeps its previous status and token and nothing
// is revoked; the refusal is kept as pending, and the next refresh applies
// it without asking the provider again.
func TestProviderRecheckRolledBackRevocationStoresNothingDB(t *testing.T) {
	env := newRecheckEnv(t, "rollback", "rt-1")
	env.recheck.writeWait = 200 * time.Millisecond
	env.checker.respond = func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
		// The plugin clears the state it holds for a refused account.
		return &pluginv1.CheckAccountResponse{
			Status:  pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_FOUND,
			Account: &pluginv1.AuthenticateResponse{RefreshState: &structpb.Struct{}},
		}, nil
	}
	sessionID, refresh := env.session(t, true)
	key, err := NewAPIKeyRepository(env.pool).Create(t.Context(), env.user.ID, "rollback", nil)
	if err != nil {
		t.Fatal(err)
	}
	env.makeDue(t)
	before := env.identityState(t)

	holder, err := env.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(t.Context(), `SELECT id FROM auth_sessions WHERE id = $1 FOR UPDATE`, sessionID); err != nil {
		t.Fatal(err)
	}
	_, err = env.svc.Refresh(t.Context(), refresh)
	_ = holder.Rollback(context.Background())
	if err == nil || errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("refresh with the revocation blocked = %v, want a failure", err)
	}
	// The store answered, so a refusal that could not be applied refuses the
	// token instead of asking the client to retry every few seconds.
	if !errors.Is(err, errAnswerNotApplied) || errors.Is(err, ErrSessionCheckUnavailable) {
		t.Fatalf("refresh with the revocation blocked = %v, want errAnswerNotApplied, not a retryable outage", err)
	}
	after := env.identityState(t)
	if after.LastCheckStatus != before.LastCheckStatus || !after.LastCheckedAt.Equal(*before.LastCheckedAt) {
		t.Fatalf("a rolled-back revocation stored the answer: before %+v, after %+v", before, after)
	}
	if got := env.storedToken(t); got != "rt-1" {
		t.Fatalf("a rolled-back revocation cleared the token: %q", got)
	}
	if env.sessionRow(t, sessionID).RevokedAt != nil || len(env.revoked) != 0 {
		t.Fatalf("session revoked = %v, hook = %v", env.sessionRow(t, sessionID).RevokedAt, env.revoked)
	}
	if _, err := NewAPIKeyRepository(env.pool).GetByKey(t.Context(), key.Key); err != nil {
		t.Fatalf("API key: %v", err)
	}
	if got := env.pendingRefusal(t); got != CheckStatusNotFound {
		t.Fatalf("pending refusal = %q, want %q", got, CheckStatusNotFound)
	}

	// Still due: the next refresh applies the refusal on record and revokes.
	if _, err := env.svc.Refresh(t.Context(), refresh); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("second refresh = %v, want ErrSessionRevoked", err)
	}
	if calls := env.checker.callCount(); calls != 1 {
		t.Fatalf("plugin calls = %d, want 1: the pending refusal applies without asking again", calls)
	}
	if got := env.identityState(t).LastCheckStatus; got != CheckStatusNotFound {
		t.Fatalf("status = %q", got)
	}
	if got := env.pendingRefusal(t); got != "" {
		t.Fatalf("pending refusal after the revocation = %q, want cleared", got)
	}
	if _, err := NewAPIKeyRepository(env.pool).GetByKey(t.Context(), key.Key); err == nil {
		t.Fatal("the refused account kept its API key")
	}
}

// lockRow holds row locks in a transaction the test rolls back.
func (e *recheckEnv) lockRow(t *testing.T, query string, args ...any) pgx.Tx {
	t.Helper()
	holder, err := e.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
	if _, err := holder.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
	return holder
}

// TestProviderRecheckAnswerFailuresAfterTheCallDB: what a refresh answers
// when the provider answered but applying the answer failed outside the
// savepoint (here a row stays locked past the write wait).
func TestProviderRecheckAnswerFailuresAfterTheCallDB(t *testing.T) {
	// An active answer whose replacement token committed first is
	// retryable: the session was not refused, and asking again presents the
	// token the provider returned.
	t.Run("active answer", func(t *testing.T) {
		env := newRecheckEnv(t, "afteractive", "rt-1")
		env.recheck.writeWait = 200 * time.Millisecond
		env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE, "rt-2")
		_, refresh := env.session(t, true)
		env.makeDue(t)

		holder := env.lockRow(t, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, env.user.ID)
		_, err := env.svc.Refresh(t.Context(), refresh)
		_ = holder.Rollback(context.Background())
		if !errors.Is(err, ErrSessionCheckUnavailable) || errors.Is(err, errAnswerNotApplied) {
			t.Fatalf("refresh with the account locked after an active answer = %v, want ErrSessionCheckUnavailable", err)
		}
		if got := env.storedToken(t); got != "rt-2" {
			t.Fatalf("stored token = %q, want the replacement rt-2", got)
		}
		if _, err := env.svc.Refresh(t.Context(), refresh); err != nil {
			t.Fatalf("retried refresh = %v", err)
		}
		if env.checker.callCount() != 2 || env.checker.states[1] != "rt-2" {
			t.Fatalf("calls = %d, states = %v; the retry should present rt-2", env.checker.callCount(), env.checker.states)
		}
	})

	// A replacement token that could not be stored may be the only one the
	// provider accepts now, so asking again cannot succeed: refused.
	t.Run("replacement not stored", func(t *testing.T) {
		env := newRecheckEnv(t, "afterlost", "rt-1")
		env.recheck.writeWait = 200 * time.Millisecond
		_, refresh := env.session(t, true)
		env.makeDue(t)
		env.checker.respond = func(ctx context.Context, req *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
			env.lockRow(t, `SELECT id FROM plugin_auth_identities WHERE id = $1 FOR UPDATE`, env.identityID)
			return answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE, "rt-2")(ctx, req)
		}
		_, err := env.svc.Refresh(t.Context(), refresh)
		if !errors.Is(err, errAnswerNotApplied) || errors.Is(err, ErrSessionCheckUnavailable) {
			t.Fatalf("refresh with the replacement token not stored = %v, want errAnswerNotApplied", err)
		}
	})

	// A refusal ends the session either way; it is not retried.
	t.Run("refusal", func(t *testing.T) {
		env := newRecheckEnv(t, "afterrefusal", "rt-1")
		env.recheck.writeWait = 200 * time.Millisecond
		env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_FOUND, "")
		_, refresh := env.session(t, true)
		env.makeDue(t)

		holder := env.lockRow(t, `SELECT id FROM users WHERE id = $1 FOR UPDATE`, env.user.ID)
		_, err := env.svc.Refresh(t.Context(), refresh)
		_ = holder.Rollback(context.Background())
		if !errors.Is(err, errAnswerNotApplied) || errors.Is(err, ErrSessionCheckUnavailable) {
			t.Fatalf("refresh with the account locked after a refusal = %v, want errAnswerNotApplied", err)
		}
		if got := env.pendingRefusal(t); got != CheckStatusNotFound {
			t.Fatalf("pending refusal = %q, want %q", got, CheckStatusNotFound)
		}
	})
}

// TestProviderRecheckUnsupportedBoundsCredentialsDB: an UNSUPPORTED answer
// revokes the account's API keys and Audiobookshelf sessions once the
// provider has not vouched for the person (sign-in or active check) within
// the absolute age, on the scheduled pass and on a refresh alike; login
// sessions keep their absolute-age bound. Someone who signs in through the
// provider within that age keeps their keys.
func TestProviderRecheckUnsupportedBoundsCredentialsDB(t *testing.T) {
	env := newRecheckEnv(t, "bound", "")
	t.Cleanup(saveSettings(t, env.pool, config.AuthRefreshTokenExpirySettingKey))
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := env.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := env.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	credentials := func() (int, int) {
		t.Helper()
		return count(`SELECT COUNT(*) FROM api_keys WHERE user_id = $1`, env.user.ID),
			count(`SELECT COUNT(*) FROM abs_sessions WHERE user_id = $1 AND revoked_at IS NULL`, env.user.ID)
	}
	lastAuthenticated := func() time.Time {
		t.Helper()
		var at *time.Time
		if err := env.pool.QueryRow(ctx, `SELECT last_authenticated_at FROM plugin_auth_identities WHERE id = $1`, env.identityID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		if at == nil {
			t.Fatal("no last_authenticated_at")
		}
		return *at
	}
	ageAuthentication := func(days int) {
		t.Helper()
		exec(`UPDATE plugin_auth_identities SET last_authenticated_at = NOW() - make_interval(days => $2) WHERE id = $1`, env.identityID, days)
	}
	exec(`UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, env.installationID)
	exec(`DELETE FROM server_settings WHERE key = $1`, config.AuthRefreshTokenExpirySettingKey)
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSUPPORTED, "")
	if time.Since(lastAuthenticated()) > time.Minute {
		t.Fatal("the sign-in did not stamp last_authenticated_at")
	}
	exec(`INSERT INTO api_keys (user_id, label, api_key, revision) VALUES ($1, 'bound', $2, 1)`, env.user.ID, "sa_bound_"+env.suffix)
	exec(`INSERT INTO abs_sessions (user_id, token_hash, device_id) VALUES ($1, $2, 'abs-device')`, env.user.ID, "abs-bound-"+env.suffix)

	// Authenticated through the provider within the absolute age (default
	// 30 days): the keys stay.
	ageAuthentication(29)
	env.makeDue(t)
	if counts, err := env.recheck.RecheckIdleIdentities(ctx); err != nil || counts[CheckStatusUnsupported] != 1 {
		t.Fatalf("counts = %v, %v", counts, err)
	}
	if keys, abs := credentials(); keys != 1 || abs != 1 {
		t.Fatalf("within the absolute age: keys = %d, abs = %d", keys, abs)
	}

	// Past it, the scheduled pass revokes them like a refusal, leaving login
	// sessions to their absolute-age bound.
	sessionID, refresh := env.session(t, true)
	ageAuthentication(31)
	env.makeDue(t)
	if counts, err := env.recheck.RecheckIdleIdentities(ctx); err != nil || counts[CheckStatusUnsupported] != 1 {
		t.Fatalf("counts = %v, %v", counts, err)
	}
	if keys, abs := credentials(); keys != 0 || abs != 0 {
		t.Fatalf("past the absolute age: keys = %d, abs = %d", keys, abs)
	}
	if env.sessionRow(t, sessionID).RevokedAt != nil {
		t.Fatal("the bound revoked a login session")
	}
	// Only ending login sessions runs the hook (it drops jellycompat's
	// cached sessions); keys and Audiobookshelf sessions need none.
	if len(env.revoked) != 0 {
		t.Fatalf("revocation hook = %v", env.revoked)
	}
	if got := env.identityState(t).LastCheckStatus; got != CheckStatusUnsupported {
		t.Fatalf("status = %q", got)
	}

	// A refresh-driven re-check applies the same bound, and the refresh
	// itself goes on under the absolute age.
	exec(`INSERT INTO api_keys (user_id, label, api_key, revision) VALUES ($1, 'bound2', $2, 1)`, env.user.ID, "sa_bound2_"+env.suffix)
	env.makeDue(t)
	if _, err := env.svc.Refresh(ctx, refresh); err != nil {
		t.Fatalf("refresh = %v", err)
	}
	if keys, _ := credentials(); keys != 0 {
		t.Fatal("a refresh-driven unsupported answer kept the API key")
	}

	// A shorter absolute age applies too.
	exec(`INSERT INTO server_settings (key, value) VALUES ($1, '7d') ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, config.AuthRefreshTokenExpirySettingKey)
	exec(`INSERT INTO api_keys (user_id, label, api_key, revision) VALUES ($1, 'bound3', $2, 1)`, env.user.ID, "sa_bound3_"+env.suffix)
	ageAuthentication(8)
	env.makeDue(t)
	if _, err := env.recheck.RecheckIdleIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	if keys, _ := credentials(); keys != 0 {
		t.Fatal("past a 7-day absolute age the API key stayed")
	}

	// Signing in through the provider again resets the clock: the next
	// unsupported answer keeps the new key.
	if _, _, err := env.resolver.Resolve(ctx, ResolveInput{InstallationID: env.installationID, AutoProvision: true, Identity: env.identity("bound")}); err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if time.Since(lastAuthenticated()) > time.Minute {
		t.Fatal("a sign-in did not stamp last_authenticated_at")
	}
	exec(`INSERT INTO api_keys (user_id, label, api_key, revision) VALUES ($1, 'bound4', $2, 1)`, env.user.ID, "sa_bound4_"+env.suffix)
	env.makeDue(t)
	if _, err := env.recheck.RecheckIdleIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	if keys, _ := credentials(); keys != 1 {
		t.Fatal("a recent sign-in lost its API key")
	}

	// An active answer counts as a provider authentication too.
	ageAuthentication(40)
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE, "")
	env.makeDue(t)
	if _, err := env.recheck.RecheckIdleIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(lastAuthenticated()) > time.Minute {
		t.Fatal("an active answer did not stamp last_authenticated_at")
	}
}

// TestProviderRecheckRolledBackRevocationKeepsRotatedStateDB: a refusal
// that comes with a rotated refresh token keeps the new token even when its
// revocation rolls back, because the plugin already spent the old one. The
// next re-check applies the pending refusal and revokes.
func TestProviderRecheckRolledBackRevocationKeepsRotatedStateDB(t *testing.T) {
	env := newRecheckEnv(t, "rotated-rollback", "rt-1")
	env.recheck.writeWait = 200 * time.Millisecond
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_PERMITTED, "rt-2")
	sessionID, refresh := env.session(t, true)
	env.makeDue(t)
	before := env.identityState(t)

	holder, err := env.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(t.Context(), `SELECT id FROM auth_sessions WHERE id = $1 FOR UPDATE`, sessionID); err != nil {
		t.Fatal(err)
	}
	_, err = env.svc.Refresh(t.Context(), refresh)
	_ = holder.Rollback(context.Background())
	if err == nil || errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("refresh with the revocation blocked = %v, want a failure", err)
	}
	// The store answered, so a refusal that could not be applied refuses the
	// token instead of asking the client to retry every few seconds.
	if !errors.Is(err, errAnswerNotApplied) || errors.Is(err, ErrSessionCheckUnavailable) {
		t.Fatalf("refresh with the revocation blocked = %v, want errAnswerNotApplied, not a retryable outage", err)
	}
	after := env.identityState(t)
	if after.LastCheckStatus != before.LastCheckStatus || !after.LastCheckedAt.Equal(*before.LastCheckedAt) {
		t.Fatalf("a rolled-back revocation stored the answer: before %+v, after %+v", before, after)
	}
	if got := env.storedToken(t); got != "rt-2" {
		t.Fatalf("stored token after a rolled-back revocation = %q, want the rotated rt-2", got)
	}
	if env.sessionRow(t, sessionID).RevokedAt != nil {
		t.Fatal("a rolled-back revocation revoked the session")
	}

	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_ACTIVE, "")
	if _, err := env.svc.Refresh(t.Context(), refresh); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("second refresh = %v, want ErrSessionRevoked", err)
	}
	if states := env.checker.states; len(states) != 1 || states[0] != "rt-1" {
		t.Fatalf("presented states = %v, want [rt-1]", states)
	}
	if got := env.storedToken(t); got != "rt-2" {
		t.Fatalf("stored token after the pending refusal = %q, want rt-2", got)
	}
	if got := env.identityState(t).LastCheckStatus; got != CheckStatusNotPermitted {
		t.Fatalf("status = %q", got)
	}
	if env.sessionRow(t, sessionID).RevokedAt == nil {
		t.Fatal("the second refusal did not revoke the session")
	}
}

// TestProviderRecheckUnsupportedSkipsLocalPasswordAccountsDB: the
// unsupported credential bound does not apply to an account with local
// password sign-in turned on. Its API key and Audiobookshelf session stay,
// the session hook (which drops jellycompat sessions) does not run, and the
// scheduled pass stops asking once the provider answered unsupported.
func TestProviderRecheckUnsupportedSkipsLocalPasswordAccountsDB(t *testing.T) {
	env := newRecheckEnv(t, "local-password", "")
	t.Cleanup(saveSettings(t, env.pool, config.AuthRefreshTokenExpirySettingKey))
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := env.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := env.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	exec(`UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, env.installationID)
	exec(`DELETE FROM server_settings WHERE key = $1`, config.AuthRefreshTokenExpirySettingKey)
	exec(`UPDATE users SET local_password_login_enabled = true WHERE id = $1`, env.user.ID)
	exec(`INSERT INTO api_keys (user_id, label, api_key, revision) VALUES ($1, 'local', $2, 1)`, env.user.ID, "sa_local_"+env.suffix)
	exec(`INSERT INTO abs_sessions (user_id, token_hash, device_id) VALUES ($1, $2, 'abs-device')`, env.user.ID, "abs-local-"+env.suffix)
	exec(`UPDATE plugin_auth_identities SET last_authenticated_at = NOW() - INTERVAL '40 days' WHERE id = $1`, env.identityID)
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSUPPORTED, "")
	env.makeDue(t)

	if counts, err := env.recheck.RecheckIdleIdentities(ctx); err != nil || counts[CheckStatusUnsupported] != 1 {
		t.Fatalf("counts = %v, %v", counts, err)
	}
	if keys := count(`SELECT COUNT(*) FROM api_keys WHERE user_id = $1`, env.user.ID); keys != 1 {
		t.Fatalf("API keys = %d, want 1", keys)
	}
	if abs := count(`SELECT COUNT(*) FROM abs_sessions WHERE user_id = $1 AND revoked_at IS NULL`, env.user.ID); abs != 1 {
		t.Fatalf("Audiobookshelf sessions = %d, want 1", abs)
	}
	if len(env.revoked) != 0 {
		t.Fatalf("revocation hook = %v, want none", env.revoked)
	}

	// Due again, the scheduled pass leaves it alone; a refresh still asks.
	env.makeDue(t)
	var due bool
	if err := env.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plugin_auth_identities i WHERE i.id = $3 AND `+idleIdentityCondition+`)`,
		config.DefaultAuthProviderRecheckInterval.Seconds(), env.recheck.retryWait.Seconds(), env.identityID).Scan(&due); err != nil || due {
		t.Fatalf("idle re-check due = %v, %v, want false", due, err)
	}
	_, refresh := env.session(t, true)
	calls := env.checker.callCount()
	if _, err := env.svc.Refresh(ctx, refresh); err != nil {
		t.Fatalf("refresh = %v", err)
	}
	if env.checker.callCount() != calls+1 {
		t.Fatal("a refresh did not re-check the identity")
	}
	if keys := count(`SELECT COUNT(*) FROM api_keys WHERE user_id = $1`, env.user.ID); keys != 1 || len(env.revoked) != 0 {
		t.Fatalf("after a refresh: API keys = %d, hook = %v", keys, env.revoked)
	}
}

// checkOutcomeUnknown reads the identity's check_outcome_unknown.
func (e *recheckEnv) checkOutcomeUnknown(t *testing.T) bool {
	t.Helper()
	var unknown bool
	if err := e.pool.QueryRow(t.Context(), `SELECT check_outcome_unknown FROM plugin_auth_identities WHERE id = $1`, e.identityID).Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	return unknown
}

// TestProviderRecheckLostAnswerDB: when a CheckAccount answer is lost after
// the plugin may have rotated the refresh token at the provider (the call
// failed in transport, or the node died before the answer committed), the
// next refusal is the provider refusing the spent token, not a revocation:
// it counts as unsupported and revokes nothing. Only a refusal that follows
// a committed answer revokes.
func TestProviderRecheckLostAnswerDB(t *testing.T) {
	for _, lose := range []string{"rpc_error", "node_died"} {
		t.Run(lose, func(t *testing.T) {
			env := newRecheckEnv(t, "lost"+lose, "rt-1")
			sessionID, refresh := env.session(t, true)
			key, err := NewAPIKeyRepository(env.pool).Create(t.Context(), env.user.ID, "lost", nil)
			if err != nil {
				t.Fatal(err)
			}
			env.makeDue(t)

			markedBeforeCall := false
			switch lose {
			case "rpc_error":
				// The plugin refreshed at the provider, then died before
				// answering.
				env.checker.respond = func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
					markedBeforeCall = env.checkOutcomeUnknown(t)
					return nil, status.Error(codes.Unavailable, "plugin process exited")
				}
				if _, err := env.svc.Refresh(t.Context(), refresh); err != nil {
					t.Fatalf("fail-open refresh = %v", err)
				}
				if !markedBeforeCall {
					t.Fatal("check_outcome_unknown was not committed before the call")
				}
				env.ageUnavailable(t)
			case "node_died":
				// The answer, a rotated token, never committed: what is left
				// is the marker committed before the call.
				if _, err := env.pool.Exec(t.Context(), `UPDATE plugin_auth_identities SET check_outcome_unknown = TRUE WHERE id = $1`, env.identityID); err != nil {
					t.Fatal(err)
				}
			}
			if !env.checkOutcomeUnknown(t) {
				t.Fatal("a lost answer cleared check_outcome_unknown")
			}
			if got := env.storedToken(t); got != "rt-1" {
				t.Fatalf("stored token = %q", got)
			}

			// The provider refuses the token the lost call spent; the plugin
			// drops it from its state.
			env.checker.respond = func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
				state, _ := structpb.NewStruct(map[string]any{"id_token": "kept"})
				return &pluginv1.CheckAccountResponse{
					Status:  pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_PERMITTED,
					Account: &pluginv1.AuthenticateResponse{RefreshState: state},
				}, nil
			}
			pair, err := env.svc.Refresh(t.Context(), refresh)
			if err != nil {
				t.Fatalf("refresh after a lost answer = %v, want no revocation", err)
			}
			if got := env.identityState(t).LastCheckStatus; got != CheckStatusUnsupported {
				t.Fatalf("status = %q, want unsupported", got)
			}
			if env.checkOutcomeUnknown(t) {
				t.Fatal("a committed answer left check_outcome_unknown set")
			}
			if got := env.storedToken(t); got != "" {
				t.Fatalf("the refused token was kept: %q", got)
			}
			if env.sessionRow(t, sessionID).RevokedAt != nil || len(env.revoked) != 0 {
				t.Fatal("a refusal after a lost answer revoked the sessions")
			}
			if _, err := NewAPIKeyRepository(env.pool).GetByKey(t.Context(), key.Key); err != nil {
				t.Fatalf("a refusal after a lost answer deleted the API key: %v", err)
			}

			// A refusal after a committed answer is a revocation again.
			env.makeDue(t)
			if _, err := env.svc.Refresh(t.Context(), pair.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
				t.Fatalf("refusal after a committed answer = %v, want ErrSessionRevoked", err)
			}
		})
	}
}

// TestProviderRecheckSignInClearsLostAnswerDB: a sign-in stores a fresh
// refresh_state, so an earlier lost answer no longer matters.
func TestProviderRecheckSignInClearsLostAnswerDB(t *testing.T) {
	env := newRecheckEnv(t, "lostsignin", "rt-1")
	if _, err := env.pool.Exec(t.Context(), `UPDATE plugin_auth_identities SET check_outcome_unknown = TRUE WHERE id = $1`, env.identityID); err != nil {
		t.Fatal(err)
	}
	identity := env.identity("lostsignin")
	identity.RefreshState = refreshState("rt-fresh")
	if _, _, err := env.resolver.Resolve(t.Context(), ResolveInput{InstallationID: env.installationID, AutoProvision: true, Identity: identity}); err != nil {
		t.Fatal(err)
	}
	if env.checkOutcomeUnknown(t) || env.storedToken(t) != "rt-fresh" {
		t.Fatalf("after sign-in: unknown = %v, token = %q", env.checkOutcomeUnknown(t), env.storedToken(t))
	}
}

func (e *recheckEnv) pendingRefusal(t *testing.T) string {
	t.Helper()
	var pending string
	if err := e.pool.QueryRow(t.Context(), `SELECT pending_refusal FROM plugin_auth_identities WHERE id = $1`, e.identityID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	return pending
}

// oidcRefusedState is the refresh_state the OIDC plugin answers with when
// the provider refuses its refresh token (invalid_grant): the ID token and
// sub, without the refresh token.
func oidcRefusedState() *structpb.Struct {
	state, err := structpb.NewStruct(map[string]any{"id_token": "header.payload.signature", "sub": "subject-1"})
	if err != nil {
		panic(err)
	}
	return state
}

// TestProviderRecheckRolledBackRefusalWithDroppedTokenDB: the OIDC plugin
// answers a refused refresh token with a refresh_state that keeps only the
// ID token and sub. That state is not empty, so it is stored before the
// savepoint and survives a rolled-back revocation; the plugin then has no
// token to present and could only answer unsupported. The refusal is kept
// as pending and the next refresh applies it without asking: the session
// ends and the API key is deleted.
func TestProviderRecheckRolledBackRefusalWithDroppedTokenDB(t *testing.T) {
	env := newRecheckEnv(t, "dropped-token", "rt-1")
	env.recheck.writeWait = 200 * time.Millisecond
	env.checker.respond = func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
		return &pluginv1.CheckAccountResponse{
			Status:  pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_PERMITTED,
			Account: &pluginv1.AuthenticateResponse{RefreshState: oidcRefusedState()},
		}, nil
	}
	sessionID, refresh := env.session(t, true)
	key, err := NewAPIKeyRepository(env.pool).Create(t.Context(), env.user.ID, "dropped-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	env.makeDue(t)

	holder, err := env.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(t.Context(), `SELECT id FROM auth_sessions WHERE id = $1 FOR UPDATE`, sessionID); err != nil {
		t.Fatal(err)
	}
	_, err = env.svc.Refresh(t.Context(), refresh)
	_ = holder.Rollback(context.Background())
	if err == nil || errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("refresh with the revocation blocked = %v, want a failure", err)
	}
	// The store answered, so a refusal that could not be applied refuses the
	// token instead of asking the client to retry every few seconds.
	if !errors.Is(err, errAnswerNotApplied) || errors.Is(err, ErrSessionCheckUnavailable) {
		t.Fatalf("refresh with the revocation blocked = %v, want errAnswerNotApplied, not a retryable outage", err)
	}
	if got := env.storedToken(t); got != "" {
		t.Fatalf("stored refresh token = %q, want the plugin's state without one", got)
	}
	if env.checkOutcomeUnknown(t) {
		t.Fatal("a known refusal left the outcome unknown")
	}
	if got := env.pendingRefusal(t); got != CheckStatusNotPermitted {
		t.Fatalf("pending refusal = %q, want %q", got, CheckStatusNotPermitted)
	}
	if env.sessionRow(t, sessionID).RevokedAt != nil {
		t.Fatal("a rolled-back revocation revoked the session")
	}

	// Without a refresh token the real plugin can only answer unsupported.
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSUPPORTED, "")
	if _, err := env.svc.Refresh(t.Context(), refresh); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("second refresh = %v, want ErrSessionRevoked", err)
	}
	if calls := env.checker.callCount(); calls != 1 {
		t.Fatalf("plugin calls = %d, want 1", calls)
	}
	if env.sessionRow(t, sessionID).RevokedAt == nil {
		t.Fatal("the pending refusal did not revoke the session")
	}
	if _, err := NewAPIKeyRepository(env.pool).GetByKey(t.Context(), key.Key); err == nil {
		t.Fatal("the refused account kept its API key")
	}
	identity := env.identityState(t)
	if identity.LastCheckStatus != CheckStatusNotPermitted || env.pendingRefusal(t) != "" {
		t.Fatalf("status = %q, pending = %q; want not_permitted and none pending", identity.LastCheckStatus, env.pendingRefusal(t))
	}
}

// TestProviderRecheckSignInClearsPendingRefusalDB: a sign-in through the
// provider supersedes a refusal whose revocation rolled back.
func TestProviderRecheckSignInClearsPendingRefusalDB(t *testing.T) {
	env := newRecheckEnv(t, "pending-sign-in", "rt-1")
	if _, err := env.pool.Exec(t.Context(), `UPDATE plugin_auth_identities SET pending_refusal = $2 WHERE id = $1`,
		env.identityID, CheckStatusNotFound); err != nil {
		t.Fatal(err)
	}
	identity := env.identity("pending-sign-in")
	if _, _, err := env.resolver.Resolve(t.Context(), ResolveInput{InstallationID: env.installationID, AutoProvision: true, Identity: identity}); err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if got := env.pendingRefusal(t); got != "" {
		t.Fatalf("pending refusal after a sign-in = %q, want cleared", got)
	}
}

// funcCheckerSource is an AccountCheckerSource answered by a func.
type funcCheckerSource func(ctx context.Context, installationID int) (AccountChecker, error)

func (f funcCheckerSource) AccountChecker(ctx context.Context, installationID int) (AccountChecker, error) {
	return f(ctx, installationID)
}

// lastAuthenticatedBackfill is the UPDATE of the migration that added
// last_authenticated_at, limited to the identity $1.
func lastAuthenticatedBackfill(t *testing.T) string {
	t.Helper()
	raw, err := migrations.FS.ReadFile("sql/20261002073701_external_sign_in_accounts.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	start := strings.Index(up, "UPDATE plugin_auth_identities")
	if start < 0 {
		t.Fatal("the migration has no backfill")
	}
	statement, _, ok := strings.Cut(up[start:], ";")
	if !ok {
		t.Fatal("the migration's backfill does not end")
	}
	return statement + " WHERE id = $1"
}

// TestProviderRecheckUpgradedIdentityKeepsAPIKeysDB: an identity linked
// before last_sign_in_at and last_authenticated_at existed (created 60 days
// ago, both NULL, never checked) starts the absolute-age bound at the
// upgrade. The first scheduled pass against a plugin that cannot re-check
// it (built before AuthProviderChecks, or with no stored refresh_state)
// keeps the account's API key.
func TestProviderRecheckUpgradedIdentityKeepsAPIKeysDB(t *testing.T) {
	answers := map[string]func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error){
		"unimplemented": func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
			return nil, status.Error(codes.Unimplemented, "unknown method CheckAccount")
		},
		"no_refresh_state": answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSUPPORTED, ""),
	}
	for name, respond := range answers {
		t.Run(name, func(t *testing.T) {
			env := newRecheckEnv(t, "upgraded"+name, "")
			t.Cleanup(saveSettings(t, env.pool, config.AuthRefreshTokenExpirySettingKey))
			ctx := t.Context()
			exec := func(sql string, args ...any) {
				t.Helper()
				if _, err := env.pool.Exec(ctx, sql, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, env.installationID)
			exec(`DELETE FROM server_settings WHERE key = $1`, config.AuthRefreshTokenExpirySettingKey)
			exec(`UPDATE users SET local_password_login_enabled = false WHERE id = $1`, env.user.ID)
			// The shape the earlier migrations leave: linked_at = created_at,
			// no sign-in, check or authentication time.
			exec(`UPDATE plugin_auth_identities SET created_at = NOW() - INTERVAL '60 days', linked_at = NOW() - INTERVAL '60 days',
				last_sign_in_at = NULL, last_authenticated_at = NULL, last_checked_at = NULL, last_check_status = '', refresh_state = ''
				WHERE id = $1`, env.identityID)
			exec(`INSERT INTO api_keys (user_id, label, api_key, revision) VALUES ($1, 'upgraded', $2, 1)`, env.user.ID, "sa_upgraded_"+name+env.suffix)
			exec(lastAuthenticatedBackfill(t), env.identityID)
			env.checker.respond = respond

			if counts, err := env.recheck.RecheckIdleIdentities(ctx); err != nil || counts[CheckStatusUnsupported] != 1 {
				t.Fatalf("counts = %v, %v", counts, err)
			}
			var keys int
			if err := env.pool.QueryRow(ctx, `SELECT COUNT(*) FROM api_keys WHERE user_id = $1`, env.user.ID).Scan(&keys); err != nil {
				t.Fatal(err)
			}
			if keys != 1 {
				t.Fatal("the first scheduled pass after the upgrade deleted the API key")
			}
		})
	}
}

// TestProviderRecheckUnansweredChecksKeepOutcomeUnknownDB: after a lost
// answer, a check in which the plugin never presented the stored token (an
// unavailable or unsupported answer without a refresh_state, or a plugin
// this node could not load) leaves check_outcome_unknown set, so a later
// refusal of the token the lost call rotated still counts as unsupported
// and revokes nothing.
func TestProviderRecheckUnansweredChecksKeepOutcomeUnknownDB(t *testing.T) {
	for _, between := range []string{"unavailable_answer", "unsupported_answer", "load_error"} {
		t.Run(between, func(t *testing.T) {
			env := newRecheckEnv(t, "unanswered"+between, "rt-1")
			ctx := t.Context()
			sessionID, refresh := env.session(t, true)
			key, err := NewAPIKeyRepository(env.pool).Create(ctx, env.user.ID, "unanswered", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_identities SET check_outcome_unknown = TRUE WHERE id = $1`, env.identityID); err != nil {
				t.Fatal(err)
			}
			env.makeDue(t)

			switch between {
			case "unavailable_answer":
				env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNAVAILABLE, "")
				if _, err := env.svc.Refresh(ctx, refresh); err != nil {
					t.Fatalf("fail-open refresh = %v", err)
				}
				env.ageUnavailable(t)
			case "unsupported_answer":
				env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNSUPPORTED, "")
				if _, err := env.svc.Refresh(ctx, refresh); err != nil {
					t.Fatalf("refresh = %v", err)
				}
				env.makeDue(t)
			case "load_error":
				env.recheck.source = fakeCheckerSource{err: errors.New("plugin process failed to start")}
				if _, err := env.svc.Refresh(ctx, refresh); err != nil {
					t.Fatalf("fail-open refresh = %v", err)
				}
				env.recheck.source = fakeCheckerSource{checker: env.checker}
			}
			if !env.checkOutcomeUnknown(t) {
				t.Fatalf("%s cleared check_outcome_unknown", between)
			}

			env.checker.respond = func(context.Context, *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
				return &pluginv1.CheckAccountResponse{
					Status:  pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_PERMITTED,
					Account: &pluginv1.AuthenticateResponse{RefreshState: oidcRefusedState()},
				}, nil
			}
			if _, err := env.svc.Refresh(ctx, refresh); err != nil {
				t.Fatalf("refresh after %s = %v, want no revocation", between, err)
			}
			if got := env.identityState(t).LastCheckStatus; got != CheckStatusUnsupported {
				t.Fatalf("status = %q, want unsupported", got)
			}
			if env.sessionRow(t, sessionID).RevokedAt != nil || len(env.revoked) != 0 {
				t.Fatal("a refusal after a lost answer revoked the sessions")
			}
			if _, err := NewAPIKeyRepository(env.pool).GetByKey(ctx, key.Key); err != nil {
				t.Fatalf("a refusal after a lost answer deleted the API key: %v", err)
			}
		})
	}
}

// TestProviderRecheckPluginLoadFailureDB: a plugin this node cannot load
// is this node's failure, not the provider's answer. Nothing is stored (so
// other nodes do not refuse refreshes under fail_closed), this refresh
// follows the outage policy, the scheduled pass counts the check as failed,
// and the plugin is loaded before the identity's advisory lock is taken.
func TestProviderRecheckPluginLoadFailureDB(t *testing.T) {
	env := newRecheckEnv(t, "loadfail", "rt-1")
	ctx := t.Context()
	if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, env.installationID); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAPIKeyRepository(env.pool).Create(ctx, env.user.ID, "loadfail", nil); err != nil {
		t.Fatal(err)
	}
	identity := env.identityState(t)
	lockKey := fmt.Sprintf("silo:auth-identity:%d:%s", identity.InstallationID, identity.ExternalSubject)
	var loads atomic.Int32
	var lockHeld atomic.Bool
	env.recheck.source = funcCheckerSource(func(ctx context.Context, _ int) (AccountChecker, error) {
		loads.Add(1)
		// A session lock belongs to its connection: probe and release on one.
		conn, err := env.pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		defer conn.Release()
		var free bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, lockKey).Scan(&free); err != nil {
			return nil, err
		}
		if free {
			_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, lockKey)
		} else {
			lockHeld.Store(true)
		}
		return nil, errors.New("plugin process failed to start")
	})
	_, refresh := env.session(t, true)
	env.makeDue(t)
	before := env.identityState(t)

	if _, err := env.svc.Refresh(ctx, refresh); err != nil {
		t.Fatalf("fail-open refresh = %v", err)
	}
	env.setSetting(t, config.AuthProviderRecheckOutagePolicySettingKey, config.AuthRecheckFailClosed)
	if _, err := env.svc.Refresh(ctx, refresh); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("fail-closed refresh = %v, want ErrProviderUnavailable", err)
	}
	counts, err := env.recheck.RecheckIdleIdentities(ctx)
	if err != nil || counts["failed"] < 1 {
		t.Fatalf("scheduled pass counts = %v, %v", counts, err)
	}
	after := env.identityState(t)
	if after.LastCheckStatus != before.LastCheckStatus || !after.LastCheckedAt.Equal(*before.LastCheckedAt) {
		t.Fatalf("a load failure stored %q at %v", after.LastCheckStatus, after.LastCheckedAt)
	}
	if env.checker.callCount() != 0 || loads.Load() < 3 {
		t.Fatalf("calls = %d, loads = %d", env.checker.callCount(), loads.Load())
	}
	if lockHeld.Load() {
		t.Fatal("the plugin was loaded while the identity's advisory lock was held")
	}
}

// TestProviderRecheckLostAnswerWithoutStateCountsDB: a plugin that checks
// without a stored refresh_state (LDAP) presents no token a lost call could
// have rotated, so a refusal after an unanswered check still revokes.
func TestProviderRecheckLostAnswerWithoutStateCountsDB(t *testing.T) {
	env := newRecheckEnv(t, "lost-nostate", "")
	ctx := t.Context()
	sessionID, refresh := env.session(t, true)
	key, err := NewAPIKeyRepository(env.pool).Create(ctx, env.user.ID, "lost-nostate", nil)
	if err != nil {
		t.Fatal(err)
	}
	env.makeDue(t)
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_UNAVAILABLE, "")
	if _, err := env.svc.Refresh(ctx, refresh); err != nil {
		t.Fatalf("fail-open refresh = %v", err)
	}
	if !env.checkOutcomeUnknown(t) {
		t.Fatal("an unavailable answer cleared check_outcome_unknown")
	}
	env.ageUnavailable(t)

	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_PERMITTED, "")
	if _, err := env.svc.Refresh(ctx, refresh); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("refusal without a stored state = %v, want ErrSessionRevoked", err)
	}
	if got := env.identityState(t).LastCheckStatus; got != CheckStatusNotPermitted {
		t.Fatalf("status = %q, want not_permitted", got)
	}
	if env.sessionRow(t, sessionID).RevokedAt == nil {
		t.Fatal("the refused account kept its login session")
	}
	if _, err := NewAPIKeyRepository(env.pool).GetByKey(ctx, key.Key); err == nil {
		t.Fatal("the refused account kept its API key")
	}
}

// TestProviderRecheckBreakGlassRefusalKeepsLocalCredentialsDB: a refusal
// for a break-glass account ends only the sessions opened through the
// identity; its local sessions and API keys stay.
func TestProviderRecheckBreakGlassRefusalKeepsLocalCredentialsDB(t *testing.T) {
	env := newRecheckEnv(t, "breakglass-refusal", "rt-1")
	ctx := t.Context()
	if _, err := env.pool.Exec(ctx, `UPDATE users SET role = 'admin', break_glass = true, local_password_login_enabled = true WHERE id = $1`, env.user.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = env.pool.Exec(context.WithoutCancel(ctx), `UPDATE users SET break_glass = false WHERE id = $1`, env.user.ID)
	})
	providerSession, refresh := env.session(t, true)
	localSession, _ := env.session(t, false)
	jellyfin := insertJellyfinSession(t, env.pool, env.user.ID)
	key, err := NewAPIKeyRepository(env.pool).Create(ctx, env.user.ID, "breakglass", nil)
	if err != nil {
		t.Fatal(err)
	}
	env.makeDue(t)
	env.checker.respond = answer(pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_FOUND, "")
	if _, err := env.svc.Refresh(ctx, refresh); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("refresh of the provider session = %v, want ErrSessionRevoked", err)
	}
	if env.sessionRow(t, providerSession).RevokedAt == nil {
		t.Fatal("the provider session survived the refusal")
	}
	// A Jellyfin-compatible session does not record its identity, so the
	// refusal deletes every one the account holds.
	if jellyfinSessionExists(t, env.pool, jellyfin) {
		t.Fatal("the refusal kept the account's Jellyfin-compatible session")
	}
	if env.sessionRow(t, localSession).RevokedAt != nil {
		t.Fatal("the refusal revoked the break-glass account's local session")
	}
	if _, err := NewAPIKeyRepository(env.pool).GetByKey(ctx, key.Key); err != nil {
		t.Fatalf("the refusal deleted the break-glass account's API key: %v", err)
	}
}

// TestProviderRecheckDisabledProviderBoundsCredentialsDB: once the
// installation is no longer an enabled sign-in provider, nobody can
// re-check its accounts, so their API keys and Audiobookshelf sessions get
// the absolute-age bound of an unsupported answer, from the scheduled pass
// and from a refresh. Nothing is stored on the identity.
func TestProviderRecheckDisabledProviderBoundsCredentialsDB(t *testing.T) {
	env := newRecheckEnv(t, "disabled-bound", "")
	t.Cleanup(saveSettings(t, env.pool, config.AuthRefreshTokenExpirySettingKey))
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := env.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	credentials := func() (int, int) {
		t.Helper()
		var keys, abs int
		if err := env.pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM api_keys WHERE user_id = $1),
			(SELECT COUNT(*) FROM abs_sessions WHERE user_id = $1 AND revoked_at IS NULL)`, env.user.ID).Scan(&keys, &abs); err != nil {
			t.Fatal(err)
		}
		return keys, abs
	}
	due := func() bool {
		t.Helper()
		ok, err := env.recheck.IdleRecheckDue(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	// The binding stays disabled (newExternalSignInEnv seeds it so), and no
	// node loads a disabled provider.
	env.recheck.source = fakeCheckerSource{err: ErrProviderNotLoaded}
	exec(`DELETE FROM server_settings WHERE key = $1`, config.AuthRefreshTokenExpirySettingKey)
	exec(`UPDATE users SET local_password_login_enabled = false WHERE id = $1`, env.user.ID)
	exec(`INSERT INTO api_keys (user_id, label, api_key, revision) VALUES ($1, 'disabled', $2, 1)`, env.user.ID, "sa_disabled_"+env.suffix)
	exec(`INSERT INTO abs_sessions (user_id, token_hash, device_id) VALUES ($1, $2, 'abs-device')`, env.user.ID, "abs-disabled-"+env.suffix)
	env.makeDue(t)
	before := env.identityState(t)

	// Within the absolute age (default 30 days) nothing is due.
	exec(`UPDATE plugin_auth_identities SET last_authenticated_at = NOW() - INTERVAL '29 days' WHERE id = $1`, env.identityID)
	if due() {
		t.Fatal("an identity of a disabled provider within the absolute age is due")
	}

	// Past it, the scheduled pass revokes the API key and the
	// Audiobookshelf session without asking anyone.
	exec(`UPDATE plugin_auth_identities SET last_authenticated_at = NOW() - INTERVAL '31 days' WHERE id = $1`, env.identityID)
	if !due() {
		t.Fatal("a stale identity of a disabled provider holding an API key is not due")
	}
	if counts, err := env.recheck.RecheckIdleIdentities(ctx); err != nil || counts[CheckStatusUnsupported] != 1 {
		t.Fatalf("counts = %v, %v", counts, err)
	}
	if keys, abs := credentials(); keys != 0 || abs != 0 {
		t.Fatalf("past the absolute age: keys = %d, abs = %d", keys, abs)
	}
	if env.checker.callCount() != 0 {
		t.Fatal("asked a disabled provider")
	}
	after := env.identityState(t)
	if after.LastCheckStatus != before.LastCheckStatus || !after.LastCheckedAt.Equal(*before.LastCheckedAt) {
		t.Fatalf("stored %q at %v for a disabled provider", after.LastCheckStatus, after.LastCheckedAt)
	}
	if due() {
		t.Fatal("still due with nothing left to bound")
	}

	// A refresh of a session opened through the identity applies the same
	// bound and goes on under the absolute age.
	_, refresh := env.session(t, true)
	exec(`INSERT INTO api_keys (user_id, label, api_key, revision) VALUES ($1, 'disabled2', $2, 1)`, env.user.ID, "sa_disabled2_"+env.suffix)
	if _, err := env.svc.Refresh(ctx, refresh); err != nil {
		t.Fatalf("refresh = %v", err)
	}
	if keys, _ := credentials(); keys != 0 {
		t.Fatal("a refresh against a disabled provider kept a stale account's API key")
	}

	// An account with local password sign-in is not the provider's to bound.
	exec(`UPDATE users SET local_password_login_enabled = true WHERE id = $1`, env.user.ID)
	exec(`INSERT INTO api_keys (user_id, label, api_key, revision) VALUES ($1, 'disabled3', $2, 1)`, env.user.ID, "sa_disabled3_"+env.suffix)
	if due() {
		t.Fatal("an account with local password sign-in is due")
	}
}

// sessionAttachBackfill is the UPDATE of the migration that attaches the
// sessions an auth plugin opened before identities were tracked, limited to
// the account $1.
func sessionAttachBackfill(t *testing.T) string {
	t.Helper()
	raw, err := migrations.FS.ReadFile("sql/20261002073701_external_sign_in_accounts.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	start := strings.Index(up, "UPDATE auth_sessions s")
	if start < 0 {
		t.Fatal("the migration does not attach sessions")
	}
	statement, _, ok := strings.Cut(up[start:], ";")
	if !ok {
		t.Fatal("the migration's session backfill does not end")
	}
	return statement + " AND s.user_id = $1"
}

// TestProviderRecheckUpgradeAttachesPluginSessionsDB: the open sessions of
// an account holding exactly one identity before the upgrade are attached
// to it, from their creation, so the provider re-checks them instead of
// letting them slide forever. Revoked, expired and impersonation sessions
// stay as they are, and an account with two identities is left alone.
func TestProviderRecheckUpgradeAttachesPluginSessionsDB(t *testing.T) {
	env := newRecheckEnv(t, "upgrade-attach", "")
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := env.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	open, _ := env.session(t, false)
	revoked, _ := env.session(t, false)
	expired, _ := env.session(t, false)
	impersonated, _ := env.session(t, false)
	exec(`UPDATE auth_sessions SET created_at = NOW() - INTERVAL '40 days' WHERE id = $1`, open)
	exec(`UPDATE auth_sessions SET revoked_at = NOW() WHERE id = $1`, revoked)
	exec(`UPDATE auth_sessions SET expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, expired)
	exec(`UPDATE auth_sessions SET impersonator_user_id = $2 WHERE id = $1`, impersonated, env.user.ID)
	exec(sessionAttachBackfill(t), env.user.ID)

	session := env.sessionRow(t, open)
	if session.IdentityID == nil || *session.IdentityID != env.identityID {
		t.Fatalf("open session identity = %v, want %d", session.IdentityID, env.identityID)
	}
	var since, created time.Time
	if err := env.pool.QueryRow(ctx, `SELECT provider_since, created_at FROM auth_sessions WHERE id = $1`, open).Scan(&since, &created); err != nil {
		t.Fatal(err)
	}
	if !since.Equal(created) {
		t.Fatalf("provider_since = %v, want the session's creation %v", since, created)
	}
	for _, id := range []string{revoked, expired, impersonated} {
		if got := env.sessionRow(t, id).IdentityID; got != nil {
			t.Fatalf("session %s attached to %d", id, *got)
		}
	}

	// A second identity makes the account's provider ambiguous: nothing is
	// attached.
	exec(`INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id) VALUES ($1, $2, $3)`,
		env.installationID, "second-"+env.suffix, env.user.ID)
	second, _ := env.session(t, false)
	exec(sessionAttachBackfill(t), env.user.ID)
	if got := env.sessionRow(t, second).IdentityID; got != nil {
		t.Fatalf("a session of an account with two identities attached to %d", *got)
	}
}

// TestProviderRecheckIgnoresAnswerForUnlinkedIdentityDB: an identity
// unlinked while the plugin answers takes the answer with it. A refusal of
// the person the account no longer signs in as revokes nothing.
func TestProviderRecheckIgnoresAnswerForUnlinkedIdentityDB(t *testing.T) {
	env := newRecheckEnv(t, "unlinked", "rt-1")
	ctx := t.Context()
	if _, err := env.pool.Exec(ctx, `UPDATE plugin_auth_bindings SET enabled = true WHERE plugin_installation_id = $1`, env.installationID); err != nil {
		t.Fatal(err)
	}
	key, err := NewAPIKeyRepository(env.pool).Create(ctx, env.user.ID, "unlinked", nil)
	if err != nil {
		t.Fatal(err)
	}
	localSession, _ := env.session(t, false)
	env.makeDue(t)
	env.checker.respond = func(callCtx context.Context, _ *pluginv1.CheckAccountRequest) (*pluginv1.CheckAccountResponse, error) {
		if _, err := env.pool.Exec(callCtx, `DELETE FROM plugin_auth_identities WHERE id = $1`, env.identityID); err != nil {
			return nil, err
		}
		return &pluginv1.CheckAccountResponse{Status: pluginv1.CheckAccountStatus_CHECK_ACCOUNT_STATUS_NOT_FOUND}, nil
	}
	if _, err := env.recheck.RecheckIdleIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	if env.checker.callCount() != 1 {
		t.Fatalf("calls = %d, want 1", env.checker.callCount())
	}
	if _, err := NewAPIKeyRepository(env.pool).GetByKey(ctx, key.Key); err != nil {
		t.Fatalf("the unlinked account lost its API key: %v", err)
	}
	if env.sessionRow(t, localSession).RevokedAt != nil {
		t.Fatal("the unlinked account lost its local session")
	}
	if len(env.revoked) != 0 {
		t.Fatalf("revocation hook = %v", env.revoked)
	}
}
