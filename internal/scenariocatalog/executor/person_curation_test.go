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
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestRequiredNewPersonCuration(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-new-person-curation for required NEW acceptance")
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
		Config: e.config(), AppContext: t.Context(), DB: e.pool, SecretCipher: cipher, ClientIPResolver: clientip.NewResolver(nil),
		NodeID: "fixture-node", PublicURL: publicURL, UserStoreProvider: e.stores, PolicySystem: e.policy, PersonRepo: catalog.NewPersonRepository(e.pool),
	}))
	defer server.Close()
	// People have no user FK and survive household reseeding. Take no ownership of existing rows.
	var count int
	if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM people`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("NEW person fixture requires empty people")
	}
	const personID = "900000091"
	cleanup := func() { e.mustExec(`DELETE FROM people WHERE id=$1`, int64(900000091)) }
	defer func() { cleanup(); e.Reseed(); e.guardScratchDatabase() }()
	var results []Result
	requests, effects := 0, 0
	for _, id := range []string{"null_preserves", "empty_clears", "calendar_dates", "invalid_atomic", "acting_admin_gate", "missing_identity"} {
		cleanup()
		e.Reseed()
		e.mustExec(`INSERT INTO people(id,name,sort_name,bio,birth_date,death_date,birthplace,homepage,tmdb_id,imdb_id,tvdb_id,plex_guid) VALUES($1,'Synthetic Person','Synthetic Sort','Synthetic biography','1960-02-29','2020-03-04','Synthetic place','https://example.invalid/person','synthetic-tmdb','synthetic-imdb','synthetic-tvdb','synthetic-guid')`, int64(900000091))
		t.Run(id, func(t *testing.T) {
			result := Result{ID: "new_person_curation." + id + "/v2", Scenario: "new_person_curation." + id, Transport: "v2"}
			defer func() {
				if t.Failed() && len(result.Failures) == 0 {
					result.Failures = append(result.Failures, "scenario assertion failed; see test log")
				}
				results = append(results, result)
			}()
			snapshot := func() map[string]any {
				t.Helper()
				effects++
				var raw []byte
				if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM people p`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var rows []map[string]any
				if err := json.Unmarshal(raw, &rows); err != nil {
					t.Fatal(err)
				}
				if len(rows) != 1 {
					t.Fatalf("person rows=%d, want1", len(rows))
				}
				return rows[0]
			}
			want := snapshot()
			patch := func(target, body string, principal scenariocatalog.Principal, expect scenariocatalog.Expect) {
				t.Helper()
				requests++
				_, failures, err := e.exchange(server.URL, http.MethodPatch, scenariocatalog.Request{Path: "/api/v2/admin/people/" + target, Body: json.RawMessage(body)}, principal, expect, nil, nil, nil)
				if err != nil {
					failures = append(failures, err.Error())
				}
				result.Failures = append(result.Failures, failures...)
				if len(failures) > 0 {
					t.Fatalf("person PATCH: %v", failures)
				}
			}
			admin := scenariocatalog.Principal{Class: "acting_admin"}
			ok := func(checks ...scenariocatalog.BodyAssertion) scenariocatalog.Expect {
				return catalogOK(append([]scenariocatalog.BodyAssertion{catalogAssertion("/id", "equals", personID)}, checks...)...)
			}
			changed := false
			switch id {
			case "null_preserves":
				patch(personID, `{"name":null,"bio":null,"birth_date":null,"death_date":null,"tmdb_id":null}`, admin, ok(catalogAssertion("/name", "equals", "Synthetic Person"), catalogAssertion("/bio", "equals", "Synthetic biography"), catalogAssertion("/birth_date", "equals", "1960-02-29"), catalogAssertion("/death_date", "equals", "2020-03-04"), catalogAssertion("/tmdb_id", "equals", "synthetic-tmdb")))
				changed = true
			case "empty_clears":
				patch(personID, `{"name":"Renamed synthetic person","bio":"","birth_date":"","death_date":"","tmdb_id":""}`, admin, ok(catalogAssertion("/name", "equals", "Renamed synthetic person"), catalogAssertion("/bio", "absent", nil), catalogAssertion("/birth_date", "absent", nil), catalogAssertion("/death_date", "absent", nil), catalogAssertion("/tmdb_id", "absent", nil), catalogAssertion("/imdb_id", "equals", "synthetic-imdb")))
				want["name"] = "Renamed synthetic person"
				want["sort_name"] = "Renamed synthetic person"
				want["bio"] = ""
				want["birth_date"] = nil
				want["death_date"] = nil
				want["tmdb_id"] = ""
				changed = true
			case "calendar_dates":
				patch(personID, `{"birth_date":"2000-02-29","death_date":"2024-02-29"}`, admin, ok(catalogAssertion("/birth_date", "equals", "2000-02-29"), catalogAssertion("/death_date", "equals", "2024-02-29")))
				want["birth_date"] = "2000-02-29"
				want["death_date"] = "2024-02-29"
				changed = true
			case "invalid_atomic":
				patch(personID, `{"name":"Must not persist","bio":"Must not persist","birth_date":"2000-02-29","death_date":"2023-02-29"}`, admin, catalogProblem(422, "validation_failed"))
			case "acting_admin_gate":
				for _, principal := range []scenariocatalog.Principal{{Class: "primary_profile"}, {Class: "acting_admin", Profile: "admin_secondary"}} {
					patch(personID, `{"name":"Must not persist"}`, principal, catalogProblem(403, "permission_denied"))
				}
			case "missing_identity":
				patch("900000092", `{"name":"Must not persist"}`, admin, catalogProblem(404, "not_found"))
			default:
				t.Fatal("unimplemented case")
			}
			got := snapshot()
			// Successful shared updates touch updated_at even if every nullable field preserves its value.
			if changed {
				delete(want, "updated_at")
				delete(got, "updated_at")
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("persisted person differs: want%v got%v", want, got)
			}
		})
	}
	if len(results) != 6 || requests != 7 || effects != 12 {
		t.Errorf("NEW person evidence%d/%d/%d, want6/7/12", len(results), requests, effects)
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
		}{"NEW person curation (outside frozen 598)", len(results), requests, effects, results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
