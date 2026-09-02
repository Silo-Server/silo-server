package executor

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

// TestScenarioCatalogs runs every scenario against the real router. Public
// and database-unavailable scenarios run in plain CI; the rest run when
// SILO_SCENARIO_DATABASE_URL points at an empty database the executor owns
// (never SILO_TEST_DATABASE_URL: the executor truncates) and skip otherwise.
func TestScenarioCatalogs(t *testing.T) {
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	results := RunAll(t, catalogs)
	var passed, skipped, failed int
	for _, r := range results {
		switch {
		case r.Skipped != "":
			skipped++
		case len(r.Failures) > 0:
			failed++
		default:
			passed++
		}
	}
	t.Logf("scenarios: passed=%d failed=%d skipped=%d", passed, failed, skipped)
	if err := WriteReport(results); err != nil {
		t.Fatalf("write report: %v", err)
	}
}

// TestReseedRestoresDefaultGroup pins that Reseed leaves the scratch
// database shaped like a real install: exactly one default access group
// (the migration-seeded one, separate from the fixture group), and an
// account created through the real signup path lands in it. Runs only when
// SILO_SCENARIO_DATABASE_URL is set.
func TestReseedRestoresDefaultGroup(t *testing.T) {
	env := New(t)
	if !env.HasDatabase() {
		t.Skip(DatabaseEnv + " not set")
	}
	ctx := context.Background()
	env.Reseed()

	var defaults int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM access_groups WHERE is_default`).Scan(&defaults); err != nil {
		t.Fatal(err)
	}
	if defaults != 1 {
		t.Fatalf("default access groups after Reseed = %d, want 1", defaults)
	}
	var defaultID int64
	var name string
	if err := env.pool.QueryRow(ctx, `SELECT id, name FROM access_groups WHERE is_default`).Scan(&defaultID, &name); err != nil {
		t.Fatal(err)
	}
	if name != "Default Group" {
		t.Fatalf("default access group is %q, want the migration-seeded \"Default Group\"", name)
	}
	var fixtureDefault bool
	if err := env.pool.QueryRow(ctx, `SELECT is_default FROM access_groups WHERE name = 'Fixture Group'`).Scan(&fixtureDefault); err != nil {
		t.Fatal(err)
	}
	if fixtureDefault {
		t.Fatal("Fixture Group must not be the default group")
	}

	// The seeded member is a non-admin created through users.Create, so it
	// inherits the default group; the admin stays ungrouped.
	var memberGroup *int64
	if err := env.pool.QueryRow(ctx, `SELECT access_group_id FROM users WHERE username = $1`, memberUser).Scan(&memberGroup); err != nil {
		t.Fatal(err)
	}
	if memberGroup == nil || *memberGroup != defaultID {
		t.Fatalf("fixture member access_group_id = %v, want default group %d", memberGroup, defaultID)
	}

	// A real signup lands in the default group too. The email uses the
	// fixture domain so the next reseed's guard still accepts the database.
	_, u, err := env.auth.Signup(ctx, "fixture-signup-probe", "fixture-signup-probe@silo.example.test", "fixture-signup-probe-password", inviteCode, false, "", "fixture", "127.0.0.1")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	var signupGroup *int64
	if err := env.pool.QueryRow(ctx, `SELECT access_group_id FROM users WHERE id = $1`, u.ID).Scan(&signupGroup); err != nil {
		t.Fatal(err)
	}
	if signupGroup == nil || *signupGroup != defaultID {
		t.Fatalf("signup access_group_id = %v, want default group %d", signupGroup, defaultID)
	}
	env.Reseed()
}
