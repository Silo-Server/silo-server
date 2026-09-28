package executor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestRequiredNewItemCuration(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-new-item-curation for required NEW acceptance")
	}
	if os.Getenv(DatabaseEnv) == "" {
		t.Fatal(DatabaseEnv + " is required; NEW acceptance cannot skip")
	}
	e := New(t)
	cipher, err := secret.New([]byte(masterKey))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.NewRouter(api.Dependencies{
		Config: e.config(), AppContext: t.Context(), DB: e.pool, SecretCipher: cipher,
		ClientIPResolver: clientip.NewResolver(nil), NodeID: "fixture-node", PublicURL: publicURL,
		UserStoreProvider: e.stores, PolicySystem: e.policy,
		FileRepo: scanner.NewFileRepository(e.pool), FolderRepo: catalog.NewFolderRepository(e.pool), PersonRepo: catalog.NewPersonRepository(e.pool),
	}))
	defer server.Close()
	var fixture catalogMediaFixture
	defer func() { fixture.cleanup(t, e); e.Reseed(); e.guardScratchDatabase() }()
	var results []Result
	requests, effects := 0, 0
	for _, id := range []string{"null_preserves", "empty_clears", "exact_item", "invalid_preflight", "acting_admin_gate", "missing_identity"} {
		fixture.cleanup(t, e)
		e.Reseed()
		fixture.seed(t, e)
		e.mustExec(`UPDATE media_items SET overview='Synthetic overview',genres=ARRAY['Drama'],air_timezone='Etc/UTC',year=2001,runtime=120 WHERE content_id=$1`, catalogAlpha)
		t.Run(id, func(t *testing.T) {
			result := Result{ID: "new_item_curation." + id + "/v2", Scenario: "new_item_curation." + id, Transport: "v2"}
			defer func() {
				if t.Failed() && len(result.Failures) == 0 {
					result.Failures = append(result.Failures, "scenario assertion failed; see test log")
				}
				results = append(results, result)
			}()
			snapshot := func() map[string]map[string]any {
				t.Helper()
				effects++
				var raw []byte
				if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_object_agg(content_id,to_jsonb(m)) FROM media_items m`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var rows map[string]map[string]any
				if err := json.Unmarshal(raw, &rows); err != nil {
					t.Fatal(err)
				}
				if len(rows) != 3 || rows[catalogAlpha] == nil || rows[catalogBeta] == nil || rows[catalogHidden] == nil {
					t.Fatalf("unexpected fixture identities: %v", rows)
				}
				return rows
			}
			want := snapshot()
			patch := func(target, body string, principal scenariocatalog.Principal, expect scenariocatalog.Expect) {
				t.Helper()
				requests++
				_, failures, err := e.exchange(server.URL, http.MethodPatch, scenariocatalog.Request{Path: "/api/v2/admin/items/" + target + "/metadata", Body: json.RawMessage(body)}, principal, expect, nil, nil, nil)
				if err != nil {
					failures = append(failures, err.Error())
				}
				result.Failures = append(result.Failures, failures...)
				if len(failures) > 0 {
					t.Fatalf("item PATCH: %v", failures)
				}
			}
			admin := scenariocatalog.Principal{Class: "acting_admin"}
			ok := func(checks ...scenariocatalog.BodyAssertion) scenariocatalog.Expect {
				return catalogOK(append([]scenariocatalog.BodyAssertion{catalogAssertion("/content_id", "equals", catalogAlpha)}, checks...)...)
			}
			changed := false
			switch id {
			case "null_preserves":
				patch(catalogAlpha, `{"title":null,"overview":null,"genres":null,"year":null,"runtime":null,"air_timezone":null}`, admin, ok(catalogAssertion("/title", "equals", "Alpha"), catalogAssertion("/overview", "equals", "Synthetic overview"), catalogAssertion("/genres", "equals", []string{"Drama"}), catalogAssertion("/year", "equals", 2001)))
				changed = true // Successful all-null edits still touch updated_at.
			case "empty_clears":
				patch(catalogAlpha, `{"overview":"","genres":[],"air_timezone":""}`, admin, ok(catalogAssertion("/overview", "absent", nil), catalogAssertion("/genres", "equals", []string{}), catalogAssertion("/air_timezone", "absent", nil)))
				want[catalogAlpha]["overview"] = ""
				want[catalogAlpha]["genres"] = []any{}
				want[catalogAlpha]["air_timezone"] = nil
				changed = true
			case "exact_item":
				patch(catalogAlpha, `{"title":"Synthetic renamed item","year":2024,"runtime":95,"genres":["Comedy","Drama"]}`, admin, ok(catalogAssertion("/title", "equals", "Synthetic renamed item"), catalogAssertion("/year", "equals", 2024), catalogAssertion("/runtime", "equals", 95), catalogAssertion("/genres", "equals", []string{"Comedy", "Drama"})))
				want[catalogAlpha]["title"] = "Synthetic renamed item"
				want[catalogAlpha]["title_normalized"] = "synthetic renamed item"
				want[catalogAlpha]["year"] = float64(2024)
				want[catalogAlpha]["runtime"] = float64(95)
				want[catalogAlpha]["genres"] = []any{"Comedy", "Drama"}
				changed = true
			case "invalid_preflight":
				patch(catalogAlpha, `{"title":"Must not persist","overview":"Must not persist","air_timezone":"Invalid/SyntheticTimezone"}`, admin, catalogProblem(422, "validation_failed"))
			case "acting_admin_gate":
				for _, principal := range []scenariocatalog.Principal{{Class: "primary_profile"}, {Class: "acting_admin", Profile: "admin_secondary"}} {
					patch(catalogAlpha, `{"title":"Must not persist"}`, principal, catalogProblem(403, "permission_denied"))
				}
			case "missing_identity":
				patch("movie:acceptance-missing", `{"title":"Must not persist"}`, admin, catalogProblem(404, "not_found"))
			default:
				t.Fatal("unimplemented case")
			}
			got := snapshot()
			// Only the edited item's successful mutation may change its update timestamp.
			if changed {
				delete(want[catalogAlpha], "updated_at")
				delete(got[catalogAlpha], "updated_at")
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("persisted item rows differ: want%v got%v", want, got)
			}
		})
	}
	if len(results) != 6 || requests != 7 || effects != 12 {
		t.Errorf("NEW item evidence%d/%d/%d, want6/7/12", len(results), requests, effects)
	}
	seen := map[string]bool{}
	for _, r := range results {
		if seen[r.ID] || !r.Passed() {
			t.Errorf("duplicate or failed result%s: %v", r.ID, r.Failures)
		}
		seen[r.ID] = true
	}
	if path := os.Getenv("SILO_SCENARIO_REPORT"); path != "" {
		report := struct {
			Scope                                       string
			NewScenarios, PhysicalRequests, EffectReads int
			Results                                     []Result
		}{"NEW item curation (outside frozen 598)", len(results), requests, effects, results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
