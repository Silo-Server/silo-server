package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

func viaDeviceSignIn(t *testing.T, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, sessionID string) *bool {
	t.Helper()
	var via *bool
	if err := pool.QueryRow(t.Context(), `SELECT via_device_sign_in FROM auth_sessions WHERE id = $1`, sessionID).Scan(&via); err != nil {
		t.Fatal(err)
	}
	return via
}

func requireVia(t *testing.T, label string, got *bool, want bool) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s session via_device_sign_in = %v, want %v", label, got, want)
	}
}

// TestSessionsRecordDeviceSignInDB: device sign-in, Quick Connect and the
// remote-playback handoff record their session as opened by device sign-in;
// password, provider and acting-as-an-account sign-ins record it as not.
// Regression: without the mark a session on a shared TV is read as an
// ordinary sign-in of the account and may see and end its other sessions.
func TestSessionsRecordDeviceSignInDB(t *testing.T) {
	env := newRecheckEnv(t, "session_reach", "")
	ctx := t.Context()
	approver, _ := env.session(t, false)
	approving := WithClaims(ctx, &Claims{UserID: env.user.ID, SessionID: approver, TokenType: TokenTypeAccess})

	for _, purpose := range []string{DeviceLoginPurposeLogin, DeviceLoginPurposeRemote} {
		devices, start, profileID := revocationDevice(t, env, purpose)
		if err := approveRevocationDevice(approving, devices, start, env.user.ID, profileID); err != nil {
			t.Fatal(err)
		}
		poll, err := devices.Poll(ctx, start.DeviceCode, "")
		if err != nil || poll.TokenPair == nil {
			t.Fatalf("%s poll = %+v, %v", purpose, poll, err)
		}
		requireVia(t, purpose, viaDeviceSignIn(t, env.pool, poll.TokenPair.SessionID), true)
	}

	devices, _, profileID := revocationDevice(t, env, DeviceLoginPurposeRemote)
	quick, err := devices.StartQuickConnect(ctx, QuickConnectStartInput{DeviceName: "device-revocation-" + env.suffix, AppName: "Jellyfin Web"})
	if err != nil {
		t.Fatal(err)
	}
	if err := devices.ApproveForProfile(approving, DeviceLoginLookupInput{UserCode: quick.Code, AccountID: env.user.ID}, env.user.ID, profileID); err != nil {
		t.Fatal(err)
	}
	collected, err := devices.CollectQuickConnect(ctx, quick.Secret, nil)
	if err != nil || collected.TokenPair == nil {
		t.Fatalf("quick connect = %+v, %v", collected, err)
	}
	requireVia(t, "quick connect", viaDeviceSignIn(t, env.pool, collected.TokenPair.SessionID), true)

	provider, err := env.svc.OpenIdentitySession(ctx, nil, IdentitySession{UserID: env.user.ID, IdentityID: env.identityID, DeviceName: "provider"})
	if err != nil {
		t.Fatal(err)
	}
	requireVia(t, "provider", viaDeviceSignIn(t, env.pool, provider.SessionID), false)

	hash, err := bcrypt.GenerateFromPassword([]byte("synthetic-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	account := func(role string) int {
		name := fmt.Sprintf("reach-%s-%s", role, env.suffix)
		var id int
		if err := env.pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled)
			VALUES ($1, $2, $3, $4, true) RETURNING id`, name, name+"@example.test", string(hash), role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = env.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM auth_sessions WHERE user_id = $1 OR impersonator_user_id = $1`, id)
			_, _ = env.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = $1`, id)
		})
		return id
	}
	viewed, admin := account("user"), account("admin")
	password, _, err := env.svc.Login(ctx, "reach-user-"+env.suffix, "synthetic-password", "password", "")
	if err != nil {
		t.Fatal(err)
	}
	requireVia(t, "password", viaDeviceSignIn(t, env.pool, password.SessionID), false)
	actingAs, _, _, err := env.svc.StartImpersonation(ctx, admin, viewed, "acting as", "")
	if err != nil {
		t.Fatal(err)
	}
	requireVia(t, "acting as an account", viaDeviceSignIn(t, env.pool, actingAs.SessionID), false)
}

// reachScratchSchema opens a pool on a new schema holding empty copies of
// the named public tables, dropped when the test ends.
func reachScratchSchema(t *testing.T, tables ...string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "session_reach_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.WithoutCancel(ctx), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, table := range tables {
		if _, err := pool.Exec(ctx, "CREATE TABLE "+table+" (LIKE public."+table+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

// deviceSignInMigration returns a migration's Up and Down SQL, unqualified
// so they apply to the test schema.
func deviceSignInMigration(t *testing.T, name string) (string, string) {
	t.Helper()
	data, err := os.ReadFile("../../migrations/sql/" + name)
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(strings.ReplaceAll(string(data), "public.", ""), "-- +goose Down")
	if !ok {
		t.Fatalf("%s has no Down section", name)
	}
	return up, down
}

// TestDeviceSignInMigrationMarksKeptRequestsDB applies the two migrations'
// Up, in order, to sessions stored before them: a session a kept device
// sign-in request points at is marked, every other keeps no value, and the
// Downs, in reverse order, drop the column.
func TestDeviceSignInMigrationMarksKeptRequestsDB(t *testing.T) {
	pool := reachScratchSchema(t, "auth_sessions")
	ctx := t.Context()
	// The marking step reads only the request's session link.
	if _, err := pool.Exec(ctx, `ALTER TABLE auth_sessions DROP COLUMN via_device_sign_in;
		CREATE TABLE device_login_requests AS SELECT auth_session_id FROM public.device_login_requests WITH NO DATA`); err != nil {
		t.Fatal(err)
	}
	device, other := uuid.NewString(), uuid.NewString()
	for _, id := range []string{device, other} {
		if _, err := pool.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, device_name, expires_at) VALUES ($1, 1, 'TV', NOW() + INTERVAL '1 day')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_login_requests (auth_session_id) VALUES ($1)`, device); err != nil {
		t.Fatal(err)
	}
	addUp, addDown := deviceSignInMigration(t, "20261125210607_record_device_sign_in_sessions.sql")
	markUp, markDown := deviceSignInMigration(t, "20261125224536_mark_device_sign_in_sessions.sql")
	for _, step := range []string{addUp, markUp} {
		if _, err := pool.Exec(ctx, step); err != nil {
			t.Fatal(err)
		}
	}
	requireVia(t, "kept request's", viaDeviceSignIn(t, pool, device), true)
	if via := viaDeviceSignIn(t, pool, other); via != nil {
		t.Fatalf("unmarked session via_device_sign_in = %v, want no value", *via)
	}
	for _, step := range []string{markDown, addDown} {
		if _, err := pool.Exec(ctx, step); err != nil {
			t.Fatal(err)
		}
	}
	var columns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'auth_sessions' AND column_name = 'via_device_sign_in'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("Down left the column: %d, %v", columns, err)
	}
}
