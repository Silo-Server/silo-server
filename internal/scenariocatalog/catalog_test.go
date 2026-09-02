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

// TestGateRejectsMisfiledRow moves POST /auth/login into the devices catalog
// and expects the gate to name the row and the catalog it belongs to.
func TestGateRejectsMisfiledRow(t *testing.T) {
	catalogs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := LoadLedger()
	if err != nil {
		t.Fatal(err)
	}
	var auth, devices *Catalog
	for _, c := range catalogs {
		switch c.RouteGroup {
		case GroupAuth:
			auth = c
		case GroupDevices:
			devices = c
		}
	}
	if auth == nil || devices == nil {
		t.Fatal("auth or devices catalog not found")
	}
	var moved *Row
	for i := range auth.Rows {
		if auth.Rows[i].Method == "POST" && auth.Rows[i].Path == "/api/v1/auth/login" && auth.Rows[i].RegistrationIndex == 0 {
			moved = &auth.Rows[i]
			auth.Rows = append(auth.Rows[:i:i], auth.Rows[i+1:]...)
			break
		}
	}
	if moved == nil {
		t.Fatal("login row not found")
	}
	devices.Rows = append(devices.Rows, *moved)
	err = verify(catalogs, ledger)
	if err == nil {
		t.Fatal("gate accepted a row filed under the wrong route group")
	}
	want := moved.Key().String() + " belongs to ledger route group /api/v1/auth, not /api/v1/devices"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("gate did not report the misfiled row; want %q in:\n%v", want, err)
	}
}

// TestGateRejectsRowWithoutExecutableAssertion empties a tier-1 row and
// marks every category not_applicable; the gate must still refuse it.
func TestGateRejectsRowWithoutExecutableAssertion(t *testing.T) {
	catalogs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := LoadLedger()
	if err != nil {
		t.Fatal(err)
	}
	var victim *Row
	for _, c := range catalogs {
		if c.RouteGroup == GroupDevices && len(c.Rows) > 1 {
			victim = &c.Rows[1]
		}
	}
	if victim == nil {
		t.Fatal("devices catalog row not found")
	}
	victim.Scenarios = nil
	victim.NotApplicable = map[Category]string{}
	for _, cat := range Categories {
		victim.NotApplicable[cat] = "not applicable to this row"
	}
	err = verify(catalogs, ledger)
	if err == nil || !strings.Contains(err.Error(), "has no status_headers or authorization scenario") {
		t.Fatalf("gate accepted a tier-1 row with zero scenarios: %v", err)
	}
}

// TestSchemaRejectsEmptyScenarios pins minItems: 1 on row.scenarios.
func TestSchemaRejectsEmptyScenarios(t *testing.T) {
	schemaBytes, err := scenarios.FS.ReadFile(scenarios.SchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	na := map[string]any{}
	for _, cat := range Categories {
		na[string(cat)] = "not applicable to this row"
	}
	catalog := map[string]any{
		"schema_version": 1,
		"listener":       "api",
		"route_group":    GroupAuth,
		"wave":           1,
		"description":    "test",
		"rows": []any{map[string]any{
			"listener": "api", "method": "GET", "path": "/api/v1/auth/me", "registration_index": 0,
			"not_applicable": na,
			"scenarios":      []any{},
		}},
	}
	raw, _ := json.Marshal(catalog)
	fsys := fstest.MapFS{
		scenarios.SchemaPath:   {Data: schemaBytes},
		"api/api-v1-auth.json": {Data: raw},
	}
	_, err = load(fsys)
	if err == nil || !strings.Contains(err.Error(), "minItems") {
		t.Fatalf("expected a minItems violation, got %v", err)
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
