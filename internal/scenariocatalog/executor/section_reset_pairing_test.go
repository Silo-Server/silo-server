package executor

import (
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func sectionResetOverlay(t *testing.T, e *Env) {
	t.Helper()
	store, err := e.stores.ForUser(e.ctx, e.users[fixtureMember].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ profile, label string }{
		{profilePrimary, "primary"}, {profileSecondary, "secondary"},
	} {
		for _, scope := range []string{"home", "library"} {
			library := ""
			if scope == "library" {
				library = "7"
			}
			rows := []userstore.SectionOverride{{ID: target.label + "-" + scope, SectionID: "fixture-section", Hidden: true}}
			if err := store.SaveSectionOverrides(e.ctx, target.profile, scope, library, rows); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestRequiredSectionResetAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-section-resets for required acceptance")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.SectionResetAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	e := New(t)
	e.afterReseed = func() { sectionResetOverlay(t, e) }
	defer func() {
		e.afterReseed = nil
		e.Reseed()
		var count int
		if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM user_settings WHERE key LIKE 'section_overrides:%'`).Scan(&count); err != nil {
			t.Error(err)
		} else if count != 0 {
			t.Errorf("reset overlay leaked %d rows", count)
		}
	}()
	results := runAll(t, selected, e)
	if err := requiredPairedResults(results, scenariocatalog.RequiredSectionResetScenarios); err != nil {
		t.Error(err)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}
