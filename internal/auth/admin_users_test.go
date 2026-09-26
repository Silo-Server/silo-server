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
	_, err = pool.Exec(t.Context(), `CREATE TABLE access_groups (LIKE public.access_groups INCLUDING ALL); CREATE TABLE users (LIKE public.users INCLUDING ALL EXCLUDING IDENTITY); ALTER TABLE users DROP COLUMN IF EXISTS admin_revision; CREATE SEQUENCE test_user_ids; ALTER TABLE users ALTER COLUMN id SET DEFAULT nextval('test_user_ids'); CREATE TABLE auth_sessions (LIKE public.auth_sessions INCLUDING ALL); CREATE TABLE abs_sessions (LIKE public.abs_sessions INCLUDING ALL); CREATE TABLE device_login_requests (LIKE public.device_login_requests INCLUDING ALL)`)
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
