package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/auditmutation/audittest"
)

type auditActionsDB struct {
	pool     *pgxpool.Pool
	svc      *Service
	sessions *SessionRepository
	users    *UserRepository
	keys     *APIKeyRepository
	suffix   string
	ids      []int
}

func newAuditActionsDB(t *testing.T) *auditActionsDB {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	users, sessions := NewUserRepository(pool), NewSessionRepository(pool)
	jwt := NewJWTService("audit-actions-test", 15*time.Minute, 24*time.Hour)
	d := &auditActionsDB{pool: pool, sessions: sessions, users: users, keys: NewAPIKeyRepository(pool),
		svc: NewService(NewLocalProvider(users, sessions), jwt, sessions, users, nil, nil, nil), suffix: strconv.FormatInt(time.Now().UnixNano(), 10)}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM api_keys WHERE user_id = ANY($1)`, d.ids)
		_, _ = pool.Exec(ctx, `DELETE FROM auth_sessions WHERE user_id = ANY($1) OR impersonator_user_id = ANY($1)`, d.ids)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, d.ids)
	})
	return d
}

// account inserts an enabled account whose password is "password".
func (d *auditActionsDB) account(t *testing.T, role string, owner bool) int {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("audit-%s-%d", d.suffix, len(d.ids))
	var id int
	if err := d.pool.QueryRow(t.Context(), `INSERT INTO users(username, email, password_hash, role, enabled, is_owner)
		VALUES ($1, $2, $3, $4, true, $5) RETURNING id`, name, name+"@example.invalid", string(hash), role, owner).Scan(&id); err != nil {
		t.Fatal(err)
	}
	d.ids = append(d.ids, id)
	return id
}

func (d *auditActionsDB) session(t *testing.T, userID int) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := d.pool.Exec(t.Context(), `INSERT INTO auth_sessions(id, user_id, device_name, expires_at) VALUES ($1, $2, 'audit', now() + interval '1 day')`, id, userID); err != nil {
		t.Fatal(err)
	}
	return id
}

func (d *auditActionsDB) ctx(t *testing.T, method, pattern, path string, actor int, optIn bool) context.Context {
	t.Helper()
	ctx, _ := audittest.Context(t, d.pool, audittest.Request{Method: method, Pattern: pattern, Path: path, UserID: &actor, OptIn: optIn})
	return ctx
}

func noRows(t *testing.T, pool *pgxpool.Pool, action, targetType, targetID string) {
	t.Helper()
	if rows := audittest.Rows(t, pool, action, targetType, targetID); len(rows) != 0 {
		t.Fatalf("%s %s #%s: unexpected rows %+v", action, targetType, targetID, rows)
	}
}

func TestImpersonationAuditDB(t *testing.T) {
	d := newAuditActionsDB(t)
	admin, viewed := d.account(t, "admin", false), d.account(t, "user", false)
	viewedID := strconv.Itoa(viewed)

	start := d.ctx(t, http.MethodPost, "/api/v2/admin/users/{id}/impersonate", "/api/v2/admin/users/"+viewedID+"/impersonate", admin, false)
	pair, _, _, err := d.svc.StartImpersonation(start, admin, viewed, "audit", "")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := d.svc.jwt.ValidateToken(pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	started := audittest.One(t, d.pool, auditmutation.ActionImpersonationStarted, auditmutation.TargetUser, viewedID)
	if started.UserID == nil || *started.UserID != admin || started.ImpersonatorUserID != nil || started.StatusCode != http.StatusOK {
		t.Fatalf("start actor/status: %s", started.Raw)
	}
	if got := audittest.Value(started.Change(t, "impersonation_session_id").After); got != `"`+claims.SessionID+`"` {
		t.Fatalf("started session %s", got)
	}
	started.AssertNoSecrets(t, pair.AccessToken, pair.RefreshToken)

	// Ending runs in the impersonated session: the subject is the viewed
	// account and the impersonator is the acting administrator.
	end := func(optIn bool, path string) context.Context {
		ctx, _ := audittest.Context(t, d.pool, audittest.Request{Method: http.MethodPost, Pattern: path, Path: path,
			UserID: &viewed, Impersonator: &admin, SessionID: claims.SessionID, OptIn: optIn})
		return ctx
	}
	if err := d.svc.EndImpersonation(end(true, "/api/v2/auth/impersonation/end"), claims.SessionID, admin+1000000); !errors.Is(err, ErrImpersonationNotAllowed) {
		t.Fatalf("foreign impersonator: %v", err)
	}
	noRows(t, d.pool, auditmutation.ActionImpersonationEnded, auditmutation.TargetUser, viewedID)
	if err := d.svc.EndImpersonation(end(true, "/api/v2/auth/impersonation/end"), claims.SessionID, admin); err != nil {
		t.Fatal(err)
	}
	ended := audittest.One(t, d.pool, auditmutation.ActionImpersonationEnded, auditmutation.TargetUser, viewedID)
	if ended.ImpersonatorUserID == nil || *ended.ImpersonatorUserID != admin || *ended.UserID != viewed || ended.StatusCode != http.StatusNoContent {
		t.Fatalf("end actor/status: %s", ended.Raw)
	}
	if got := audittest.Value(ended.Change(t, "impersonation_session_id").Before); got != `"`+claims.SessionID+`"` {
		t.Fatalf("ended session %s", got)
	}
	// Ending an ended session succeeds and records nothing more.
	if err := d.svc.EndImpersonation(end(true, "/api/v2/auth/impersonation/end"), claims.SessionID, admin); err != nil {
		t.Fatal(err)
	}
	audittest.One(t, d.pool, auditmutation.ActionImpersonationEnded, auditmutation.TargetUser, viewedID)

	// The frozen v1 route and a v2 route that did not opt in record no
	// domain rows.
	for _, ctx := range []context.Context{end(false, "/api/v1/auth/impersonation/end"), end(false, "/api/v2/auth/impersonation/end")} {
		pair, _, _, err := d.svc.StartImpersonation(context.Background(), admin, viewed, "audit", "")
		if err != nil {
			t.Fatal(err)
		}
		next, err := d.svc.jwt.ValidateToken(pair.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.svc.EndImpersonation(ctx, next.SessionID, admin); err != nil {
			t.Fatal(err)
		}
	}
	audittest.One(t, d.pool, auditmutation.ActionImpersonationEnded, auditmutation.TargetUser, viewedID)
}

func TestOwnershipTransferAuditDB(t *testing.T) {
	d := newAuditActionsDB(t)
	// The installation has at most one Owner. Borrow an existing one and hand
	// ownership back afterwards.
	var owner int
	err := d.pool.QueryRow(t.Context(), `SELECT id FROM users WHERE is_owner`).Scan(&owner)
	borrowed := err == nil
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		owner = d.account(t, "admin", true)
	case err != nil:
		t.Fatal(err)
	}
	next := d.account(t, "admin", false)
	if borrowed {
		t.Cleanup(func() { _ = d.users.TransferOwnership(context.Background(), next, owner) })
	}
	nextID := strconv.Itoa(next)
	path := "/api/v2/admin/users/" + nextID + "/transfer-ownership"
	pattern := "/api/v2/admin/users/{id}/transfer-ownership"
	if err := d.users.TransferOwnership(d.ctx(t, http.MethodPost, pattern, path, next, false), next, owner); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("transfer by a non-owner: %v", err)
	}
	noRows(t, d.pool, auditmutation.ActionOwnershipTransferred, auditmutation.TargetUser, strconv.Itoa(owner))
	if err := d.users.TransferOwnership(d.ctx(t, http.MethodPost, pattern, path, owner, false), owner, next); err != nil {
		t.Fatal(err)
	}
	row := audittest.One(t, d.pool, auditmutation.ActionOwnershipTransferred, auditmutation.TargetUser, nextID)
	change := row.Change(t, "owner_user_id")
	if *row.UserID != owner || row.StatusCode != http.StatusNoContent || audittest.Value(change.Before) != strconv.Itoa(owner) || audittest.Value(change.After) != nextID {
		t.Fatalf("transfer row: %s", row.Raw)
	}
}

func TestPasswordChangeAuditDB(t *testing.T) {
	d := newAuditActionsDB(t)
	user := d.account(t, "user", false)
	userID := strconv.Itoa(user)
	session := d.session(t, user)
	change := func() context.Context {
		return d.ctx(t, http.MethodPost, "/api/v2/account/password", "/api/v2/account/password", user, true)
	}
	if err := d.svc.ChangePassword(change(), user, session, "wrong-password", "margin fossil quench"); !errors.Is(err, ErrCurrentPasswordInvalid) {
		t.Fatalf("wrong current password: %v", err)
	}
	noRows(t, d.pool, auditmutation.ActionPasswordChanged, auditmutation.TargetUser, userID)
	if err := d.svc.ChangePassword(change(), user, session, "password", "margin fossil quench"); err != nil {
		t.Fatal(err)
	}
	row := audittest.One(t, d.pool, auditmutation.ActionPasswordChanged, auditmutation.TargetUser, userID)
	if len(row.Changes) != 1 || row.Changes[0].Field != "password" || row.Changes[0].Before != nil || row.Changes[0].After != nil {
		t.Fatalf("password change details: %s", row.Raw)
	}
	var hash string
	if err := d.pool.QueryRow(t.Context(), `SELECT password_hash FROM users WHERE id = $1`, user).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	row.AssertNoSecrets(t, "margin fossil quench", hash, `"password"`+":")
	if *row.UserID != user || row.StatusCode != http.StatusNoContent {
		t.Fatalf("password change actor/status: %s", row.Raw)
	}
	// The frozen v1 route keeps request rows only.
	v1 := d.ctx(t, http.MethodPost, "/api/v1/auth/account/password", "/api/v1/auth/account/password", user, false)
	if err := d.svc.ChangePassword(v1, user, session, "margin fossil quench", "hollow margin fossil"); err != nil {
		t.Fatal(err)
	}
	audittest.One(t, d.pool, auditmutation.ActionPasswordChanged, auditmutation.TargetUser, userID)
}

func TestLoginSessionRevocationAuditDB(t *testing.T) {
	d := newAuditActionsDB(t)
	admin, user, other := d.account(t, "admin", false), d.account(t, "user", false), d.account(t, "user", false)
	userID := strconv.Itoa(user)

	own := d.session(t, user)
	self := func(id string) context.Context {
		return d.ctx(t, http.MethodDelete, "/api/v2/auth/sessions/{id}", "/api/v2/auth/sessions/"+id, user, true)
	}
	if err := d.svc.RevokeSession(self(own), d.session(t, other), user); !IsSessionNotFound(err) {
		t.Fatalf("revoking another account's session: %v", err)
	}
	noRows(t, d.pool, auditmutation.ActionLoginSessionRevoked, auditmutation.TargetUser, strconv.Itoa(other))
	for range 2 {
		if err := d.svc.RevokeSession(self(own), own, user); err != nil {
			t.Fatal(err)
		}
	}
	row := audittest.One(t, d.pool, auditmutation.ActionLoginSessionRevoked, auditmutation.TargetUser, userID)
	if audittest.Value(row.Change(t, "login_session_id").Before) != `"`+own+`"` || *row.UserID != user || row.StatusCode != http.StatusNoContent {
		t.Fatalf("own revocation: %s", row.Raw)
	}

	one := d.session(t, user)
	adminCtx := func(path string) context.Context {
		pattern := "/api/v2/admin/users/{user_id}/login-sessions"
		if path != "/api/v2/admin/users/"+userID+"/login-sessions" {
			pattern += "/{session_id}"
		}
		return d.ctx(t, http.MethodDelete, pattern, path, admin, false)
	}
	for range 2 {
		if _, err := d.sessions.RevokeAsAdmin(adminCtx("/api/v2/admin/users/"+userID+"/login-sessions/"+one), admin, user, &one); err != nil {
			t.Fatal(err)
		}
	}
	rows := audittest.Rows(t, d.pool, auditmutation.ActionLoginSessionRevoked, auditmutation.TargetUser, userID)
	if len(rows) != 2 || *rows[1].UserID != admin || audittest.Value(rows[1].Change(t, "login_session_id").Before) != `"`+one+`"` {
		t.Fatalf("admin single revocation: %+v", rows)
	}

	d.session(t, user)
	d.session(t, user)
	all := adminCtx("/api/v2/admin/users/" + userID + "/login-sessions")
	if n, err := d.sessions.RevokeAsAdmin(all, admin, user, nil); err != nil || n != 2 {
		t.Fatalf("revoke all: %d, %v", n, err)
	}
	if n, err := d.sessions.RevokeAsAdmin(all, admin, user, nil); err != nil || n != 0 {
		t.Fatalf("revoke all again: %d, %v", n, err)
	}
	everywhere := audittest.One(t, d.pool, auditmutation.ActionLoginSessionsRevoked, auditmutation.TargetUser, userID)
	if audittest.Value(everywhere.Change(t, "revoked_login_sessions").After) != "2" || everywhere.StatusCode != http.StatusOK {
		t.Fatalf("revoke all row: %s", everywhere.Raw)
	}
	// A Jellyfin-compatible sign-in alone is still a sign-out worth recording.
	if _, err := d.pool.Exec(t.Context(), `INSERT INTO jellycompat_sessions(token, username, account_username, profile_id, profile_name, pseudo_user_id,
		streamapp_user_id, streamapp_access_token, streamapp_refresh_token, streamapp_token_expiry, expires_at)
		VALUES ($1, 'audit', 'audit', 'primary', 'audit', gen_random_uuid(), $2, 'access', 'refresh', now() + interval '1 hour', now() + interval '1 day')`,
		"audit-jf-"+d.suffix, user); err != nil {
		t.Fatal(err)
	}
	if _, err := d.sessions.RevokeAsAdmin(all, admin, user, nil); err != nil {
		t.Fatal(err)
	}
	if rows := audittest.Rows(t, d.pool, auditmutation.ActionLoginSessionsRevoked, auditmutation.TargetUser, userID); len(rows) != 2 {
		t.Fatalf("Jellyfin-only sign out everywhere: %+v", rows)
	}
}

func TestAPIKeyAuditDB(t *testing.T) {
	d := newAuditActionsDB(t)
	admin, owner := d.account(t, "admin", false), d.account(t, "admin", false)
	adminCtx := func(method, pattern, path string) context.Context {
		return d.ctx(t, method, pattern, path, admin, false)
	}

	key, err := d.keys.Create(adminCtx(http.MethodPost, "/api/v2/admin/api-keys", "/api/v2/admin/api-keys"), owner, "Backup job", []string{"library:read"})
	if err != nil {
		t.Fatal(err)
	}
	keyID := strconv.FormatInt(key.ID, 10)
	created := audittest.One(t, d.pool, auditmutation.ActionAPIKeyCreated, auditmutation.TargetAPIKey, keyID)
	created.AssertNoSecrets(t, key.Key, key.Key[:11], key.Key[3:])
	for field, want := range map[string]string{"label": `"Backup job"`, "user_id": strconv.Itoa(owner), "rate_tier": `"standard"`, "scopes": `["library:read"]`} {
		if got := audittest.Value(created.Change(t, field).After); got != want {
			t.Fatalf("created %s = %s, want %s", field, got, want)
		}
	}
	if created.StatusCode != http.StatusCreated || *created.UserID != admin {
		t.Fatalf("created actor/status: %s", created.Raw)
	}

	tierPath := "/api/v2/admin/api-keys/" + keyID + "/tier"
	tier := adminCtx(http.MethodPut, "/api/v2/admin/api-keys/{id}/tier", tierPath)
	if _, err := d.keys.UpdateTierConditional(tier, key.ID, "elevated", APIKeyPrecondition{Revision: 999999}); !errors.Is(err, ErrAPIKeyRevisionConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	for range 2 {
		if _, err := d.keys.UpdateTierConditional(tier, key.ID, "elevated", APIKeyPrecondition{Any: true}); err != nil {
			t.Fatal(err)
		}
	}
	updated := audittest.One(t, d.pool, auditmutation.ActionAPIKeyUpdated, auditmutation.TargetAPIKey, keyID)
	if change := updated.Change(t, "rate_tier"); len(updated.Changes) != 1 || audittest.Value(change.Before) != `"standard"` || audittest.Value(change.After) != `"elevated"` || updated.StatusCode != http.StatusOK {
		t.Fatalf("tier update: %s", updated.Raw)
	}

	if err := d.keys.DeleteByAdminConditional(adminCtx(http.MethodDelete, "/api/v2/admin/api-keys/{id}", "/api/v2/admin/api-keys/"+keyID), key.ID, APIKeyPrecondition{Any: true}); err != nil {
		t.Fatal(err)
	}
	deleted := audittest.One(t, d.pool, auditmutation.ActionAPIKeyDeleted, auditmutation.TargetAPIKey, keyID)
	if audittest.Value(deleted.Change(t, "label").Before) != `"Backup job"` || deleted.Changes[0].After != nil || deleted.StatusCode != http.StatusNoContent {
		t.Fatalf("admin delete: %s", deleted.Raw)
	}
	deleted.AssertNoSecrets(t, key.Key, key.Key[:11])

	// Personal keys: the owner creates and revokes its own key.
	personal := func(method, pattern, path string) context.Context {
		return d.ctx(t, method, pattern, path, owner, true)
	}
	own, err := d.keys.Create(personal(http.MethodPost, "/api/v2/api-keys", "/api/v2/api-keys"), owner, "Laptop", nil)
	if err != nil {
		t.Fatal(err)
	}
	ownID := strconv.FormatInt(own.ID, 10)
	if row := audittest.One(t, d.pool, auditmutation.ActionAPIKeyCreated, auditmutation.TargetAPIKey, ownID); *row.UserID != owner {
		t.Fatalf("personal create actor: %s", row.Raw)
	}
	revoke := personal(http.MethodDelete, "/api/v2/api-keys/{id}", "/api/v2/api-keys/"+ownID)
	if err := d.keys.Delete(revoke, own.ID, admin); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("revoking a key another account owns: %v", err)
	}
	noRows(t, d.pool, auditmutation.ActionAPIKeyDeleted, auditmutation.TargetAPIKey, ownID)
	if err := d.keys.Delete(revoke, own.ID, owner); err != nil {
		t.Fatal(err)
	}
	audittest.One(t, d.pool, auditmutation.ActionAPIKeyDeleted, auditmutation.TargetAPIKey, ownID).AssertNoSecrets(t, own.Key)

	// The frozen v1 routes record no domain rows.
	v1, err := d.keys.Create(d.ctx(t, http.MethodPost, "/api/v1/admin/api-keys", "/api/v1/admin/api-keys", admin, false), owner, "Legacy", nil)
	if err != nil {
		t.Fatal(err)
	}
	noRows(t, d.pool, auditmutation.ActionAPIKeyCreated, auditmutation.TargetAPIKey, strconv.FormatInt(v1.ID, 10))
}

// TestSessionRevocationQueuesBehindAccountLockDB holds the account lock that
// RevokeAsAdmin takes before it revokes the account's sessions, and checks
// that an audited revocation of one of those sessions waits for it instead of
// locking the session first and deadlocking on its audit row's account key.
func TestSessionRevocationQueuesBehindAccountLockDB(t *testing.T) {
	d := newAuditActionsDB(t)
	user := d.account(t, "user", false)
	own := d.session(t, user)
	ctx := d.ctx(t, http.MethodDelete, "/api/v2/auth/sessions/{id}", "/api/v2/auth/sessions/"+own, user, true)

	signOut, err := d.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = signOut.Rollback(context.WithoutCancel(t.Context())) }()
	var holder int
	if err := signOut.QueryRow(t.Context(), `SELECT pg_backend_pid() FROM users WHERE id = $1 FOR UPDATE`, user).Scan(&holder); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.svc.RevokeSession(ctx, own, user) }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for waiting := false; !waiting; {
		if err := d.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, holder).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			t.Fatalf("revocation finished while the account was locked: %v", err)
		case <-deadline.C:
			t.Fatal("revocation never waited for the account")
		case <-tick.C:
		}
	}
	if _, err := signOut.Exec(t.Context(), `UPDATE auth_sessions SET revoked_at = NOW() WHERE user_id = $1 AND revoked_at IS NULL`, user); err != nil {
		t.Fatalf("sign out everywhere behind a revocation: %v", err)
	}
	if err := signOut.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("revocation behind sign out everywhere: %v", err)
	}
	noRows(t, d.pool, auditmutation.ActionLoginSessionRevoked, auditmutation.TargetUser, strconv.Itoa(user))
}
