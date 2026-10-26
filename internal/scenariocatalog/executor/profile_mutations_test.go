package executor

import (
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
)

func TestRequiredProfileMutationAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-profile-mutations for required acceptance")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	pilot, err := scenariocatalog.ProfileMutationAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	e := New(t)
	e.afterReseed = func() { profileMutationOrder(t, e) }
	defer func() { e.afterReseed = nil; e.Reseed() }()
	results := runAll(t, pilot, e)
	if err := requiredPairedResults(results, scenariocatalog.RequiredProfileMutationScenarios); err != nil {
		t.Error(err)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}

// Distinct fixture timestamps keep positional assertions independent of database
// tie ordering. Production and ordinary scenario fixtures are unchanged.
func profileMutationOrder(t *testing.T, e *Env) {
	t.Helper()
	for i, id := range []string{profilePrimary, profileSecondary, profileChild, profileLocked, profileAdminPrimary, profileAdminSecondary, profileAdminLocked} {
		if _, err := e.pool.Exec(e.ctx, `UPDATE user_profiles SET created_at = TIMESTAMPTZ '2026-01-01 00:00:00Z' + $1 * INTERVAL '1 hour' WHERE id = $2`, i, id); err != nil {
			t.Fatal(err)
		}
	}
}
