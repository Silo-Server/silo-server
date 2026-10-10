package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// TestLoginSessionActivityAndAdminRevocationDB needs a migrated throwaway DB.
// It checks persistence and live authentication, rather than a mocked revoke.
func TestLoginSessionActivityAndAdminRevocationDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// The concurrent revoker, collector, impersonation and observer each need
	// a connection in addition to the transaction holding a session row.
	poolConfig.MaxConns = 6
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := fmt.Sprint(time.Now().UnixNano())
	ids := []int{}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM device_login_requests WHERE device_name=$1`, suffix)
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM auth_sessions WHERE user_id=ANY($1) OR impersonator_user_id=ANY($1)`, ids)
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id=ANY($1)`, ids)
	})
	hash, err := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	account := func(role string) int {
		t.Helper()
		var id int
		n := fmt.Sprintf("admin-%s-%d", suffix, len(ids))
		if err := pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,role,enabled) VALUES($1,$2,$3,$4,true) RETURNING id`, n, n+"@admin.com", hash, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		return id
	}
	actor, target, other := account("admin"), account("user"), account("user")
	repo := NewSessionRepository(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := func(userID int) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions(id,user_id,device_name,created_at,expires_at) VALUES($1,$2,'Test client',$3,$4)`, id, userID, now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		return id
	}
	one, two, untouched := session(target), session(target), session(other)
	targetJellyfin, otherJellyfin := insertJellyfinSession(t, pool, target), insertJellyfinSession(t, pool, other)
	read := func(id string) *time.Time {
		t.Helper()
		s, err := repo.GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return s.LastSeenAt
	}
	if read(one) != nil {
		t.Fatal("new session invented activity")
	}
	for _, at := range []time.Time{now, now.Add(30 * time.Second), now.Add(-time.Minute)} {
		if err := repo.UpdateLastSeen(ctx, one, at); err != nil {
			t.Fatal(err)
		}
	}
	if at := read(one); at == nil || !at.Equal(now) {
		t.Fatalf("activity regressed or exceeded throttle: %v", at)
	}
	later := now.Add(time.Minute)
	if err := repo.UpdateLastSeen(ctx, one, later); err != nil {
		t.Fatal(err)
	}
	if at := read(one); at == nil || !at.Equal(later) {
		t.Fatalf("later activity not persisted: %v", at)
	}
	if _, err := repo.RevokeAsAdmin(ctx, actor, other, &one); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("cross-account revoke = %v", err)
	}
	if _, err := repo.RevokeAsAdmin(ctx, target, target, &one); !errors.Is(err, ErrAdminProtected) {
		t.Fatalf("non-admin revoke = %v", err)
	}
	if n, err := repo.RevokeAsAdmin(ctx, actor, target, &one); err != nil || n != 1 {
		t.Fatalf("single revoke %d %v", n, err)
	}
	if !jellyfinSessionExists(t, pool, targetJellyfin) {
		t.Fatal("single-session revoke must leave the account's Jellyfin-compatible sessions")
	}
	if _, active, err := repo.ActiveSessionRole(ctx, one); err != nil || active {
		t.Fatalf("revoked session still valid: %t %v", active, err)
	}
	if err := repo.UpdateLastSeen(ctx, one, later.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if at := read(one); at == nil || !at.Equal(later) {
		t.Fatalf("revoked activity changed: %v", at)
	}
	absSession := func(userID int) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO abs_sessions(user_id,token_hash,device_id) VALUES($1,$2,'test') RETURNING id`, userID, uuid.NewString()).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	absRevoked := func(id int64) bool {
		t.Helper()
		var revoked bool
		if err := pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM abs_sessions WHERE id=$1`, id).Scan(&revoked); err != nil {
			t.Fatal(err)
		}
		return revoked
	}
	targetABS, otherABS := absSession(target), absSession(other)
	if n, err := repo.RevokeAsAdmin(ctx, actor, target, nil); err != nil || n != 1 {
		t.Fatalf("all revoke %d %v", n, err)
	}
	if !absRevoked(targetABS) || absRevoked(otherABS) {
		t.Fatal("account-wide revoke must end only the target's Audiobookshelf sessions")
	}
	if jellyfinSessionExists(t, pool, targetJellyfin) || !jellyfinSessionExists(t, pool, otherJellyfin) {
		t.Fatal("account-wide revoke must delete only the target's Jellyfin-compatible sessions")
	}
	for _, id := range []string{one, two} {
		if _, active, err := repo.ActiveSessionRole(ctx, id); err != nil || active {
			t.Fatalf("target session remains valid: %t %v", active, err)
		}
	}
	if _, active, err := repo.ActiveSessionRole(ctx, untouched); err != nil || !active {
		t.Fatalf("other account lost access: %t %v", active, err)
	}
	var passwordHash string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, target).Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(&models.User{PasswordHash: passwordHash}, "password") {
		t.Fatal("revocation changed password")
	}
	var ownerID int
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE is_owner`).Scan(&ownerID); err == nil {
		if _, err := repo.RevokeAsAdmin(ctx, actor, ownerID, nil); !errors.Is(err, ErrOwnerProtected) {
			t.Fatalf("owner protection = %v", err)
		}
	}
	t.Run("derived sign-ins", func(t *testing.T) {
		users := NewUserRepository(pool)
		jwt := NewJWTService("admin-revocation-test-secret", time.Hour, 24*time.Hour)
		svc := NewService(nil, jwt, repo, users, nil, nil, nil)
		direct, single := session(actor), session(actor)
		claims := &Claims{UserID: actor, Role: models.RoleAdmin, SessionID: direct, TokenType: TokenTypeAccess}
		actingCtx := WithClaims(ctx, claims)
		pair, _, _, err := svc.StartImpersonation(actingCtx, actor, other, suffix, "")
		if err != nil {
			t.Fatal(err)
		}
		impersonation, err := jwt.ValidateToken(pair.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		// Starting another View as user from inside one is a conflict, whatever
		// the viewed account could otherwise do.
		if _, _, _, err := svc.StartImpersonation(WithClaims(ctx, impersonation), impersonation.UserID, actor, suffix, ""); !errors.Is(err, ErrAlreadyImpersonating) {
			t.Fatalf("nested impersonation = %v, want ErrAlreadyImpersonating", err)
		}
		stores := pgstore.NewPostgresProvider(pool)
		store, err := stores.ForUser(ctx, actor)
		if err != nil {
			t.Fatal(err)
		}
		profileID := uuid.NewString()
		if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "admin"}); err != nil {
			t.Fatal(err)
		}
		devices := NewDeviceLoginService(pool, users, jwt, repo, stores, access.NewProfileTokenService("admin-revocation-profile-secret", time.Hour))
		start := func(purpose string) *DeviceLoginStartResult {
			t.Helper()
			request, err := devices.Start(ctx, DeviceLoginStartInput{DeviceName: suffix, ClientPurpose: purpose, Temporary: purpose == DeviceLoginPurposeRemote})
			if err != nil {
				t.Fatal(err)
			}
			return request
		}
		approvals := []*DeviceLoginStartResult{}
		for _, purpose := range []string{DeviceLoginPurposeLogin, DeviceLoginPurposeRemote} {
			request := start(purpose)
			p := ""
			if purpose == DeviceLoginPurposeRemote {
				p = profileID
			}
			if err := approveRevocationDevice(actingCtx, devices, request, actor, p); err != nil {
				t.Fatal(err)
			}
			approvals = append(approvals, request)
		}
		unrelated := start(DeviceLoginPurposeLogin)
		otherClaims := &Claims{UserID: other, SessionID: untouched, TokenType: TokenTypeAccess}
		if err := devices.Approve(WithClaims(ctx, otherClaims), DeviceLoginLookupInput{UserCode: unrelated.UserCode}, other); err != nil {
			t.Fatal(err)
		}
		pending := start(DeviceLoginPurposeLogin)
		if n, err := repo.RevokeAsAdmin(ctx, actor, actor, &single); err != nil || n != 1 {
			t.Fatalf("single revoke %d %v", n, err)
		}
		for _, request := range approvals {
			info, err := devices.Lookup(ctx, DeviceLoginLookupInput{UserCode: request.UserCode})
			if err != nil || info.Status != DeviceLoginStatusApproved {
				t.Fatalf("single revoke withdrew %s approval: %+v, %v", request.ClientPurpose, info, err)
			}
		}
		if _, active, err := repo.ActiveSessionRole(ctx, impersonation.SessionID); err != nil || !active {
			t.Fatalf("single revoke ended another impersonation: %t %v", active, err)
		}
		// Hold one session row so revocation pauses after acquiring the account
		// lock. Device collection must wait for that revocation, rather than
		// minting another session or deadlocking on the device row.
		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		holder, err := pool.Begin(waitCtx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
		if _, err := holder.Exec(waitCtx, `SELECT id FROM auth_sessions WHERE id=$1 FOR UPDATE`, direct); err != nil {
			t.Fatal(err)
		}
		var holderPID int
		if err := holder.QueryRow(waitCtx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
			t.Fatal(err)
		}
		type revokeResult struct {
			n   int
			err error
		}
		revoked := make(chan revokeResult, 1)
		go func() {
			n, err := repo.RevokeAsAdmin(waitCtx, actor, actor, nil)
			revoked <- revokeResult{n, err}
		}()
		var revokerPID int
		for revokerPID == 0 {
			if err := pool.QueryRow(waitCtx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity
				WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid))
				AND query LIKE 'UPDATE auth_sessions%' LIMIT 1), 0)`, holderPID).Scan(&revokerPID); err != nil {
				t.Fatalf("waiting for revocation to hold the account: %v", err)
			}
		}
		type pollResult struct {
			poll *DeviceLoginPollResult
			err  error
		}
		collected := make(chan pollResult, 1)
		go func() {
			poll, err := devices.Poll(waitCtx, approvals[0].DeviceCode)
			collected <- pollResult{poll, err}
		}()
		impersonated := make(chan error, 1)
		go func() {
			_, _, _, err := svc.StartImpersonation(WithClaims(waitCtx, claims), actor, other, suffix, "")
			impersonated <- err
		}()
		for {
			var blocked int
			if err := pool.QueryRow(waitCtx, `SELECT count(*) FROM pg_stat_activity
				WHERE datname=current_database() AND pid<>$1
				AND cardinality(pg_blocking_pids(pid))>0`, revokerPID).Scan(&blocked); err != nil {
				t.Fatalf("waiting for device collection to lock the account: %v", err)
			}
			if blocked >= 2 {
				break
			}
		}
		if err := holder.Commit(waitCtx); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-revoked:
			if got.err != nil || got.n != 2 {
				t.Fatalf("account-wide revoke %d %v, want direct and impersonation", got.n, got.err)
			}
		case <-waitCtx.Done():
			t.Fatal("account-wide revocation did not finish")
		}
		select {
		case got := <-collected:
			if got.err != nil || got.poll.Status != DeviceLoginStatusDenied || got.poll.TokenPair != nil {
				t.Fatalf("concurrent collection revived access: %+v, %v", got.poll, got.err)
			}
		case <-waitCtx.Done():
			t.Fatal("device collection did not finish after account-wide revocation")
		}
		select {
		case err := <-impersonated:
			if !errors.Is(err, ErrSessionRevoked) {
				t.Fatalf("in-flight impersonation after account-wide revoke = %v, want ErrSessionRevoked", err)
			}
		case <-waitCtx.Done():
			t.Fatal("impersonation did not finish after account-wide revocation")
		}
		for _, id := range []string{direct, single, impersonation.SessionID} {
			if _, active, err := repo.ActiveSessionRole(ctx, id); err != nil || active {
				t.Fatalf("account-wide revoke left a session active: %t %v", active, err)
			}
		}
		if _, err := svc.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrSessionRevoked) {
			t.Fatalf("impersonation refresh after account-wide revoke = %v", err)
		}
		for _, request := range approvals {
			poll, err := devices.Poll(ctx, request.DeviceCode)
			if err != nil || poll.Status != DeviceLoginStatusDenied || poll.TokenPair != nil {
				t.Fatalf("account-wide revoke allowed %s collection: %+v, %v", request.ClientPurpose, poll, err)
			}
		}
		if poll, err := devices.Poll(ctx, pending.DeviceCode); err != nil || poll.Status != DeviceLoginStatusPending {
			t.Fatalf("unapproved request changed: %+v, %v", poll, err)
		}
		if poll, err := devices.Poll(ctx, unrelated.DeviceCode); err != nil || poll.Status != DeviceLoginStatusApproved || poll.TokenPair == nil {
			t.Fatalf("other account's device approval lost: %+v, %v", poll, err)
		}
		if _, active, err := repo.ActiveSessionRole(ctx, untouched); err != nil || !active {
			t.Fatalf("viewed account's own session lost: %t %v", active, err)
		}
		if n, err := repo.RevokeAsAdmin(ctx, actor, actor, nil); err != nil || n != 0 {
			t.Fatalf("repeated account-wide revoke %d %v", n, err)
		}
		expired := session(actor)
		if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, expired); err != nil {
			t.Fatal(err)
		}
		for state, id := range map[string]string{"revoked": direct, "expired": expired, "unknown": uuid.NewString(), "wrong account": untouched} {
			stale := *claims
			stale.SessionID = id
			if _, _, _, err := svc.StartImpersonation(WithClaims(ctx, &stale), actor, other, suffix, ""); !errors.Is(err, ErrSessionRevoked) {
				t.Fatalf("%s source opened an impersonation: %v", state, err)
			}
		}
	})
}
