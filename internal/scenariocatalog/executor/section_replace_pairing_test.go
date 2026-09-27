package executor

import (
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func sectionReplaceOverlay(t *testing.T, e *Env) {
	t.Helper()
	for _, target := range []struct{ user, profile, label string }{
		{fixtureMember, profileSecondary, "secondary"}, {fixtureMember, profilePrimary, "primary"}, {fixtureAdmin, profileAdminPrimary, "admin"},
	} {
		store, err := e.stores.ForUser(e.ctx, e.users[target.user].ID)
		if err != nil {
			t.Fatal(err)
		}
		rows := []userstore.SectionOverride{{ID: target.label + "-replacement-original", SectionID: "fixture-original", Hidden: true}}
		if err := store.SaveSectionOverrides(e.ctx, target.profile, "home", "", rows); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRequiredSectionReplaceAcceptance(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-section-replacements for required acceptance")
	}
	catalogs, err := scenariocatalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := scenariocatalog.SectionReplaceAcceptance(catalogs)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; acceptance cannot skip its database")
	}
	e := New(t)
	e.afterReseed = func() { sectionReplaceOverlay(t, e) }
	defer func() {
		e.afterReseed = nil
		e.Reseed()
		var count int
		if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM user_settings WHERE key LIKE 'section_overrides:%'`).Scan(&count); err != nil {
			t.Error(err)
		} else if count != 0 {
			t.Errorf("replacement overlay leaked %d rows", count)
		}
	}()
	results := runAll(t, selected, e)
	if err := requiredPairedResults(results, scenariocatalog.RequiredSectionReplaceScenarios); err != nil {
		t.Error(err)
	}
	if err := WriteReport(results); err != nil {
		t.Fatal(err)
	}
}
