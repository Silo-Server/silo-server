package auth

import (
	"context"
	"os"
	"strings"
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
