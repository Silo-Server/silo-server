package scenariocatalog

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Silo-Server/silo-server/contracts/api/v2/scenarios"
)

// TestCatalogsPassGate is the CI gate behind make verify-scenario-catalogs.
func TestCatalogsPassGate(t *testing.T) {
	if err := Verify(); err != nil {
		t.Fatal(err)
	}
	catalogs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cov := Summarize(catalogs)
	t.Logf("catalogs=%d rows=%d scenarios=%d ci_runnable=%d db_gated=%d flagged=%d not_applicable=%d by_category=%v",
		cov.Catalogs, cov.Rows, cov.Scenarios, cov.CIRunnable, cov.DBGated, cov.Flagged, cov.NotApplicab, cov.ByCategory)
}

func TestGateFailsWhenARowIsMissing(t *testing.T) {
	catalogs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := LoadLedger()
	if err != nil {
		t.Fatal(err)
	}
	// Drop the first row of the auth catalog and expect the gate to name it.
	var victim *Catalog
	for _, c := range catalogs {
		if c.RouteGroup == GroupAuth {
			victim = c
		}
	}
	if victim == nil || len(victim.Rows) == 0 {
		t.Fatal("auth catalog not found")
	}
	dropped := victim.Rows[0].Key()
	victim.Rows = victim.Rows[1:]
	err = verify(catalogs, ledger)
	if err == nil || !strings.Contains(err.Error(), dropped.String()) {
		t.Fatalf("gate did not report the dropped row %s: %v", dropped, err)
	}
}

func TestSchemaRejectsUncoveredCategory(t *testing.T) {
	schemaBytes, err := scenarios.FS.ReadFile(scenarios.SchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog := map[string]any{
		"schema_version": 1,
		"listener":       "api",
		"route_group":    GroupAuth,
		"wave":           1,
		"description":    "test",
		"rows": []any{map[string]any{
			"listener": "api", "method": "GET", "path": "/api/v1/auth/me", "registration_index": 0,
			"scenarios": []any{map[string]any{
				"id": "one", "category": "status_headers", "description": "d",
				"principal":      map[string]any{"class": "public"},
				"request":        map[string]any{"path": "/api/v1/auth/me"},
				"expect":         map[string]any{"status": 401},
				"v2_expectation": nil,
			}},
		}},
	}
	raw, _ := json.Marshal(catalog)
	fsys := fstest.MapFS{
		scenarios.SchemaPath:   {Data: schemaBytes},
		"api/api-v1-auth.json": {Data: raw},
	}
	_, err = load(fsys)
	if err == nil || !strings.Contains(err.Error(), "no data_meaning scenario") {
		t.Fatalf("expected a missing-category problem, got %v", err)
	}
}

func TestGroupSlug(t *testing.T) {
	cases := map[string]string{
		"/api/v1/auth":                    "api-v1-auth",
		"/api/v1/auth/oauth/{install_id}": "api-v1-auth-oauth-install_id",
		"/api/v1":                         "api-v1",
		"/":                               "root",
	}
	for in, want := range cases {
		if got := GroupSlug(in); got != want {
			t.Errorf("GroupSlug(%q) = %q, want %q", in, got, want)
		}
	}
}
