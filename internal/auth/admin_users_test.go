package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func adminAccountsDB(t *testing.T) *UserRepository {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "admin_accounts_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.WithoutCancel(t.Context()), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	config.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	config.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(t.Context(), `CREATE TABLE access_groups (LIKE public.access_groups INCLUDING ALL); CREATE TABLE users (LIKE public.users INCLUDING ALL EXCLUDING IDENTITY); ALTER TABLE users DROP COLUMN IF EXISTS admin_revision; CREATE SEQUENCE test_user_ids; ALTER TABLE users ALTER COLUMN id SET DEFAULT nextval('test_user_ids'); CREATE TABLE auth_sessions (LIKE public.auth_sessions INCLUDING ALL); CREATE TABLE abs_sessions (LIKE public.abs_sessions INCLUDING ALL); CREATE TABLE jellycompat_sessions (LIKE public.jellycompat_sessions INCLUDING ALL); CREATE TABLE device_login_requests (LIKE public.device_login_requests INCLUDING ALL); CREATE TABLE api_keys (LIKE public.api_keys INCLUDING ALL); CREATE TRIGGER api_key_configuration_revision BEFORE INSERT OR UPDATE ON api_keys FOR EACH ROW EXECUTE FUNCTION public.advance_api_key_configuration_revision(); CREATE TABLE password_reset_tokens (LIKE public.password_reset_tokens INCLUDING ALL); CREATE TABLE invitations (LIKE public.invitations INCLUDING ALL)`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260906001036_add_admin_user_revision.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), strings.Split(string(migration), "-- +goose Down")[0]); err != nil {
		t.Fatal(err)
	}
	return NewUserRepository(pool)
}
func testAdminAccount(t *testing.T, r *UserRepository) *models.User {
	t.Helper()
	u, err := r.Create(t.Context(), models.CreateUserInput{Username: uuid.NewString(), Email: uuid.NewString() + "@example.test", Password: "original-password", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// insertJellyfinSession stores one Jellyfin-compatible session for the
// account and returns its token.
func insertJellyfinSession(t *testing.T, db *pgxpool.Pool, userID int) string {
	t.Helper()
	token := uuid.NewString()
	if _, err := db.Exec(t.Context(), `INSERT INTO jellycompat_sessions(token, username, account_username, profile_id, profile_name, pseudo_user_id,
		streamapp_user_id, streamapp_access_token, streamapp_refresh_token, streamapp_token_expiry, expires_at)
		VALUES ($1, 'jf', 'jf', 'primary', 'jf', $2, $3, 'access', 'refresh', now() + interval '1 hour', now() + interval '1 day')`,
		token, uuid.New(), userID); err != nil {
		t.Fatal(err)
	}
	return token
}

func jellyfinSessionExists(t *testing.T, db *pgxpool.Pool, token string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM jellycompat_sessions WHERE token = $1)`, token).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestAdminUserPageExactIdentityPostgres(t *testing.T) {
	r := adminAccountsDB(t)
	_, err := r.pool.Exec(t.Context(), `INSERT INTO users(username,email,password_hash,role,enabled) VALUES
	 ('First','match@example.test','x','admin',false),
	 ('MATCH@example.test','second@example.test','x','user',true),
	 ('Other','match+tag@example.test','x','user',true)`)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.ListPage(t.Context(), 0, 1, "  MATCH@EXAMPLE.TEST  ")
	if err != nil || len(first) != 1 || first[0].Username != "First" || first[0].Enabled {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := r.ListPage(t.Context(), first[0].ID, 1, "match@example.test")
	if err != nil || len(second) != 1 || second[0].Username != "MATCH@example.test" {
		t.Fatalf("second page: %+v %v", second, err)
	}
	for _, identity := range []string{"match", "%", "missing@example.test"} {
		rows, err := r.ListPage(t.Context(), 0, 10, identity)
		if err != nil || len(rows) != 0 {
			t.Fatalf("%q was not an exact match: %+v %v", identity, rows, err)
		}
	}
}
func TestAdminAccountMutationAtomicGuardAndSessionRevocation(t *testing.T) {
	r := adminAccountsDB(t)
	u := testAdminAccount(t, r)
	other := testAdminAccount(t, r)
	direct, impersonation := uuid.NewString(), uuid.NewString()
	for _, s := range []models.AuthSession{{ID: direct, UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}, {ID: impersonation, UserID: other.ID, ImpersonatorUserID: new(u.ID), ExpiresAt: time.Now().Add(time.Hour)}} {
		if err := NewSessionRepository(r.pool).Create(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.pool.Exec(t.Context(), `INSERT INTO abs_sessions(user_id, token_hash, device_id) VALUES ($1, 'abs-token', 'abs-device')`, u.ID); err != nil {
		t.Fatal(err)
	}
	jellyfin, otherJellyfin := insertJellyfinSession(t, r.pool, u.ID), insertJellyfinSession(t, r.pool, other.ID)
	before, err := r.GetAdminSnapshot(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for n := range 4 {
		wg.Go(func() {
			_, err := r.MutateAdminAccount(t.Context(), u.ID, before.Revision, &models.UpdateUserInput{Password: new(fmt.Sprintf("new-password-%d", n))}, func(*models.User, pgx.Tx) (bool, error) { return true, nil })
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success, stale := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrAdminUserRevision) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || stale != 3 {
		t.Fatalf("success=%d stale=%d", success, stale)
	}
	for _, id := range []string{direct, impersonation} {
		valid, err := NewSessionRepository(r.pool).IsValid(t.Context(), id)
		if err != nil || valid {
			t.Fatalf("session %s valid=%v err=%v", id, valid, err)
		}
	}
	var liveABS int
	if err := r.pool.QueryRow(t.Context(), `SELECT count(*) FROM abs_sessions WHERE user_id = $1 AND revoked_at IS NULL`, u.ID).Scan(&liveABS); err != nil || liveABS != 0 {
		t.Fatalf("%d Audiobookshelf sessions survived (%v)", liveABS, err)
	}
	if jellyfinSessionExists(t, r.pool, jellyfin) || !jellyfinSessionExists(t, r.pool, otherJellyfin) {
		t.Fatal("a password change must delete only the account's own Jellyfin-compatible sessions")
	}
	fresh := uuid.NewString()
	if err := NewSessionRepository(r.pool).Create(t.Context(), models.AuthSession{ID: fresh, UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	_, err = r.MutateAdminAccount(t.Context(), u.ID, before.Revision, &models.UpdateUserInput{Password: new("replayed-password")}, func(*models.User, pgx.Tx) (bool, error) { t.Fatal("stale request reached effects"); return true, nil })
	if !errors.Is(err, ErrAdminUserRevision) {
		t.Fatal(err)
	}
	if valid, err := NewSessionRepository(r.pool).IsValid(t.Context(), fresh); err != nil || !valid {
		t.Fatalf("fresh login revoked by stale replay: %v %v", valid, err)
	}
}
func TestAdminAccountMutationRollbackAndDelete(t *testing.T) {
	r := adminAccountsDB(t)
	u := testAdminAccount(t, r)
	other := testAdminAccount(t, r)
	session := uuid.NewString()
	if err := NewSessionRepository(r.pool).Create(t.Context(), models.AuthSession{ID: session, UserID: other.ID, ImpersonatorUserID: new(u.ID), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	before, err := r.GetAdminSnapshot(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Revocation precedes the duplicate constraint failure and must roll back.
	_, err = r.MutateAdminAccount(t.Context(), u.ID, before.Revision, &models.UpdateUserInput{Username: new(other.Username)}, func(*models.User, pgx.Tx) (bool, error) { return true, nil })
	if !IsDuplicate(err) {
		t.Fatal(err)
	}
	after, err := r.GetAdminSnapshot(t.Context(), u.ID)
	if err != nil || after.Revision != before.Revision {
		t.Fatalf("revision changed after rollback: %+v %v", after, err)
	}
	if valid, err := NewSessionRepository(r.pool).IsValid(t.Context(), session); err != nil || !valid {
		t.Fatalf("session changed after rollback: %v %v", valid, err)
	}
	_, err = r.MutateAdminAccount(t.Context(), u.ID, before.Revision, nil, func(*models.User, pgx.Tx) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.GetByID(t.Context(), u.ID); !IsNotFound(err) {
		t.Fatal(err)
	}
	if valid, err := NewSessionRepository(r.pool).IsValid(t.Context(), session); err != nil || valid {
		t.Fatalf("impersonation survived deletion: %v %v", valid, err)
	}
}

// Signing an account out deletes its Jellyfin-compatible sessions in the
// transaction that revokes its login sessions. When that delete fails, the
// disable and the admin sign-out return the error and change nothing, so the
// caller never reports success while the Jellyfin session keeps working.
func TestJellyfinSessionDeleteFailureRollsBackRevocationPostgres(t *testing.T) {
	r := adminAccountsDB(t)
	ctx := t.Context()
	sessions := NewSessionRepository(r.pool)
	admin, err := r.Create(ctx, models.CreateUserInput{Username: uuid.NewString(), Email: uuid.NewString() + "@example.test", Password: "original-password", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	u := testAdminAccount(t, r)
	login := uuid.NewString()
	if err := sessions.Create(ctx, models.AuthSession{ID: login, UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	jellyfin := insertJellyfinSession(t, r.pool, u.ID)
	// The function and trigger land in this test's own schema.
	if _, err := r.pool.Exec(ctx, `CREATE FUNCTION refuse_jellycompat_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'delete refused'; END $$;
		CREATE TRIGGER refuse_jellycompat_delete BEFORE DELETE ON jellycompat_sessions FOR EACH ROW EXECUTE FUNCTION refuse_jellycompat_delete()`); err != nil {
		t.Fatal(err)
	}
	unchanged := func(step string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "delete refused") {
			t.Fatalf("%s: error = %v, want the refused delete", step, err)
		}
		account, err := r.GetByID(ctx, u.ID)
		if err != nil || !account.Enabled {
			t.Fatalf("%s: account changed: %+v %v", step, account, err)
		}
		if valid, err := sessions.IsValid(ctx, login); err != nil || !valid {
			t.Fatalf("%s: login session revoked: %v %v", step, valid, err)
		}
		if !jellyfinSessionExists(t, r.pool, jellyfin) {
			t.Fatalf("%s: Jellyfin-compatible session deleted", step)
		}
	}

	_, err = r.MutateAdminAccount(ctx, u.ID, -1, &models.UpdateUserInput{Enabled: new(false)}, func(*models.User, pgx.Tx) (bool, error) { return true, nil })
	unchanged("disable", err)
	_, err = sessions.RevokeAsAdmin(ctx, admin.ID, u.ID, nil)
	unchanged("sign out everywhere", err)

	if _, err := r.pool.Exec(ctx, `DROP TRIGGER refuse_jellycompat_delete ON jellycompat_sessions`); err != nil {
		t.Fatal(err)
	}
	if n, err := sessions.RevokeAsAdmin(ctx, admin.ID, u.ID, nil); err != nil || n != 1 {
		t.Fatalf("retried sign out everywhere = %d, %v", n, err)
	}
	if jellyfinSessionExists(t, r.pool, jellyfin) {
		t.Fatal("Jellyfin-compatible session survived the retried sign-out")
	}
}

// A role change keeps the account's own sessions, which then report the new
// role so access tokens minted under the old one must be refreshed, and ends
// every impersonation session the account started or that views as it.
func TestAdminAccountRoleChangeKeepsSessionsAndEndsImpersonationPostgres(t *testing.T) {
	r := adminAccountsDB(t)
	sessions := NewSessionRepository(r.pool)
	admin := testAdminAccount(t, r)
	if err := r.Update(t.Context(), admin.ID, models.UpdateUserInput{Role: new(models.RoleAdmin)}); err != nil {
		t.Fatal(err)
	}
	viewed := testAdminAccount(t, r)
	promoted := testAdminAccount(t, r)
	bystander := testAdminAccount(t, r)
	own, startedByAdmin, viewingPromoted, unrelated := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, s := range []models.AuthSession{
		{ID: own, UserID: admin.ID, ExpiresAt: time.Now().Add(time.Hour)},
		{ID: startedByAdmin, UserID: viewed.ID, ImpersonatorUserID: new(admin.ID), ExpiresAt: time.Now().Add(time.Hour)},
		{ID: viewingPromoted, UserID: promoted.ID, ImpersonatorUserID: new(admin.ID), ExpiresAt: time.Now().Add(time.Hour)},
		{ID: unrelated, UserID: viewed.ID, ImpersonatorUserID: new(bystander.ID), ExpiresAt: time.Now().Add(time.Hour)},
	} {
		if err := sessions.Create(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	activeRole := func(id string) (string, bool) {
		t.Helper()
		role, active, err := sessions.ActiveSessionRole(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		return role, active
	}
	if role, active := activeRole(own); !active || role != models.RoleAdmin {
		t.Fatalf("own session before demotion: role %q active %v", role, active)
	}

	// The handlers' rule no longer asks MutateAdminAccount to sign out.
	if _, err := r.MutateAdminAccount(t.Context(), admin.ID, -1, &models.UpdateUserInput{Role: new(models.RoleUser)}, func(*models.User, pgx.Tx) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	if role, active := activeRole(own); !active || role != models.RoleUser {
		t.Fatalf("own session after demotion: role %q active %v, want it kept with role user", role, active)
	}
	if _, active := activeRole(startedByAdmin); active {
		t.Fatal("the demoted admin's impersonation session survived")
	}
	if _, active := activeRole(unrelated); !active {
		t.Fatal("another admin's impersonation session was ended")
	}

	// viewingPromoted was started by the now-demoted admin and is already
	// gone; a fresh session viewing as the account shows promotion ends it.
	viewingBeforePromotion := uuid.NewString()
	if err := sessions.Create(t.Context(), models.AuthSession{ID: viewingBeforePromotion, UserID: promoted.ID, ImpersonatorUserID: new(bystander.ID), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.MutateAdminAccount(t.Context(), promoted.ID, -1, &models.UpdateUserInput{Role: new(models.RoleAdmin)}, func(*models.User, pgx.Tx) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	if _, active := activeRole(viewingBeforePromotion); active {
		t.Fatal("an impersonation session viewing as the promoted account survived")
	}

	// Resending the current role changes nothing.
	if _, err := r.MutateAdminAccount(t.Context(), bystander.ID, -1, &models.UpdateUserInput{Role: new(models.RoleUser)}, func(*models.User, pgx.Tx) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	if _, active := activeRole(unrelated); !active {
		t.Fatal("an unchanged role ended impersonation sessions")
	}
}
