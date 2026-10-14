package auth

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/migrations"
)

const remotePlaybackMigration = "20261012171715_bind_remote_playback_sessions"

// TestBindRemotePlaybackSessionsMigrationDB runs the migration in a scratch
// schema holding the two tables it reads, as an earlier release left them:
// handoff sessions still recorded on their device requests are bound and
// ended 24 hours after creation, ordinary sign-ins are untouched, and a node
// from the earlier release (which collects and refreshes handoffs as ordinary
// sign-ins) still produces a bound session that cannot outlive its limit.
func TestBindRemotePlaybackSessionsMigrationDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	schema := fmt.Sprintf("remote_playback_%d", time.Now().UnixNano())
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), "DROP SCHEMA "+schema+" CASCADE")
		pool.Close()
	})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	apply := func(down bool) {
		t.Helper()
		raw, err := migrations.FS.ReadFile("sql/" + remotePlaybackMigration + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		up, rollback, _ := strings.Cut(string(raw), "-- +goose Down")
		sql := up
		if down {
			sql = rollback
		}
		exec("BEGIN;" + strings.ReplaceAll(sql, "public.", schema+".") + ";COMMIT")
	}
	exec("CREATE SCHEMA " + schema)
	exec(`CREATE TABLE auth_sessions (
		id text PRIMARY KEY, user_id integer NOT NULL, created_at timestamptz NOT NULL DEFAULT NOW(),
		expires_at timestamptz NOT NULL, revoked_at timestamptz)`)
	exec(`CREATE TABLE device_login_requests (
		id text PRIMARY KEY, client_purpose text NOT NULL, approved_by_user_id integer,
		approved_profile_id text, auth_session_id text REFERENCES auth_sessions(id) ON DELETE SET NULL)`)
	// Handoffs an earlier release collected: one an hour old that a refresh
	// already moved to 30 days, one three days old that is still signed in,
	// and one whose request somehow lost its profile. Plus an ordinary
	// device sign-in and an ordinary session with no request.
	exec(`INSERT INTO auth_sessions (id, user_id, created_at, expires_at) VALUES
		('recent', 1, NOW() - INTERVAL '1 hour', NOW() + INTERVAL '30 days'),
		('escaped', 1, NOW() - INTERVAL '3 days', NOW() + INTERVAL '27 days'),
		('unbound', 1, NOW() - INTERVAL '1 hour', NOW() + INTERVAL '23 hours'),
		('device', 1, NOW() - INTERVAL '1 hour', NOW() + INTERVAL '30 days'),
		('browser', 1, NOW() - INTERVAL '1 hour', NOW() + INTERVAL '30 days')`)
	exec(`INSERT INTO device_login_requests (id, client_purpose, approved_by_user_id, approved_profile_id, auth_session_id) VALUES
		('r1', 'remote_playback', 1, 'kid', 'recent'),
		('r2', 'remote_playback', 1, 'kid', 'escaped'),
		('r3', 'remote_playback', 1, ' ', 'unbound'),
		('r4', 'device_login', 1, NULL, 'device')`)

	apply(false)

	type row struct {
		purpose, profile  string
		expires, absolute *time.Time
		revoked           bool
		createdPlusOneDay time.Time
	}
	read := func(id string) row {
		t.Helper()
		var r row
		if err := pool.QueryRow(t.Context(), `SELECT purpose, COALESCE(bound_profile_id, ''), expires_at,
			absolute_expires_at, revoked_at IS NOT NULL, created_at + INTERVAL '24 hours'
			FROM auth_sessions WHERE id = $1`, id).
			Scan(&r.purpose, &r.profile, &r.expires, &r.absolute, &r.revoked, &r.createdPlusOneDay); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return r
	}
	for _, id := range []string{"recent", "escaped"} {
		got := read(id)
		if got.purpose != "remote_playback" || got.profile != "kid" || got.absolute == nil ||
			!got.absolute.Equal(got.createdPlusOneDay) || !got.expires.Equal(*got.absolute) {
			t.Fatalf("backfilled handoff %s = %+v", id, got)
		}
	}
	if got := read("escaped"); got.expires.After(time.Now()) {
		t.Fatalf("a handoff collected three days ago is still signed in until %v", got.expires)
	}
	if got := read("unbound"); !got.revoked {
		t.Fatalf("a handoff with no profile was not ended: %+v", got)
	}
	for _, id := range []string{"device", "browser"} {
		if got := read(id); got.purpose != "account" || got.profile != "" || got.absolute != nil || got.revoked ||
			got.expires.Before(time.Now().Add(29*24*time.Hour)) {
			t.Fatalf("ordinary session %s changed: %+v", id, got)
		}
	}

	// An earlier release collecting a handoff during a rolling deploy creates
	// an ordinary session, then records it on the request.
	exec(`INSERT INTO auth_sessions (id, user_id, expires_at) VALUES ('rolling', 1, NOW() + INTERVAL '24 hours')`)
	exec(`INSERT INTO device_login_requests (id, client_purpose, approved_by_user_id, approved_profile_id) VALUES ('r5', 'remote_playback', 1, 'kid')`)
	exec(`UPDATE device_login_requests SET auth_session_id = 'rolling' WHERE id = 'r5'`)
	rolling := read("rolling")
	if rolling.purpose != "remote_playback" || rolling.profile != "kid" || rolling.absolute == nil || !rolling.absolute.Equal(*rolling.expires) {
		t.Fatalf("handoff collected by an earlier release = %+v", rolling)
	}
	// Its refresh then sets the full refresh lifetime; the limit holds.
	exec(`UPDATE auth_sessions SET expires_at = NOW() + INTERVAL '30 days' WHERE id = 'rolling'`)
	if got := read("rolling"); !got.expires.Equal(*rolling.absolute) {
		t.Fatalf("an earlier release's refresh moved the handoff to %v, past %v", got.expires, *rolling.absolute)
	}
	// An earlier release collecting an ordinary device sign-in is untouched.
	exec(`INSERT INTO auth_sessions (id, user_id, expires_at) VALUES ('rolling-device', 1, NOW() + INTERVAL '30 days')`)
	exec(`INSERT INTO device_login_requests (id, client_purpose, approved_by_user_id) VALUES ('r6', 'device_login', 1)`)
	exec(`UPDATE device_login_requests SET auth_session_id = 'rolling-device' WHERE id = 'r6'`)
	if got := read("rolling-device"); got.purpose != "account" || got.absolute != nil {
		t.Fatalf("ordinary device sign-in collected by an earlier release = %+v", got)
	}

	// Rolling back cannot leave a handoff readable as an ordinary sign-in.
	apply(true)
	var live int
	if err := pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM auth_sessions
		WHERE id IN ('recent', 'rolling') AND revoked_at IS NULL`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("%d handoff sessions survived the rollback", live)
	}
}
