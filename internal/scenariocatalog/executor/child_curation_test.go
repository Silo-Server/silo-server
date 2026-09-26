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

func TestRequiredNewChildCuration(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-new-child-curation for required NEW acceptance")
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

	const seasonID = "season:acceptance-child"
	const episodeID = "episode:acceptance-child"
	var fixture catalogMediaFixture
	// Existing media guard also protects children: both require a parent media_items FK.
	cleanup := func() {
		e.mustExec(`DELETE FROM episodes WHERE content_id=$1`, episodeID)
		e.mustExec(`DELETE FROM seasons WHERE content_id=$1`, seasonID)
		fixture.cleanup(t, e)
	}
	defer func() { cleanup(); e.Reseed(); e.guardScratchDatabase() }()
	var results []Result
	requests, effects := 0, 0
	for _, id := range []string{"season_identity", "episode_identity", "child_nulls", "child_permissions"} {
		cleanup()
		e.Reseed()
		fixture.seed(t, e)
		e.mustExec(`UPDATE media_items SET type='series' WHERE content_id=$1`, catalogAlpha)
		e.mustExec(`INSERT INTO seasons(content_id,series_id,season_number,title,overview,air_date,poster_path,poster_thumbhash,metadata_s3_path,metadata_etag) VALUES($1,$2,1,'Synthetic season','Season overview','2020-01-02','','','','')`, seasonID, catalogAlpha)
		e.mustExec(`INSERT INTO episodes(content_id,series_id,season_id,season_number,episode_number,title,overview,air_date,runtime) VALUES($1,$2,$3,1,2,'Synthetic episode','Episode overview','2020-01-03',45)`, episodeID, catalogAlpha, seasonID)
		t.Run(id, func(t *testing.T) {
			result := Result{ID: "new_child_curation." + id + "/v2", Scenario: "new_child_curation." + id, Transport: "v2"}
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
				if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_object_agg(content_id,row) FROM (SELECT content_id,to_jsonb(m) row FROM media_items m UNION ALL SELECT content_id,to_jsonb(s) FROM seasons s UNION ALL SELECT content_id,to_jsonb(e) FROM episodes e) all_rows`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var rows map[string]map[string]any
				if err := json.Unmarshal(raw, &rows); err != nil {
					t.Fatal(err)
				}
				if len(rows) != 5 {
					t.Fatalf("fixture count%d,want5", len(rows))
				}
				for _, key := range []string{catalogAlpha, catalogBeta, catalogHidden, seasonID, episodeID} {
					if rows[key] == nil {
						t.Fatalf("missing fixture%s", key)
					}
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
					t.Fatalf("child PATCH: %v", failures)
				}
			}
			admin := scenariocatalog.Principal{Class: "acting_admin"}
			ok := func(target, kind, title string) scenariocatalog.Expect {
				return catalogOK(catalogAssertion("/content_id", "equals", target), catalogAssertion("/type", "equals", kind), catalogAssertion("/title", "equals", title), catalogAssertion("/series_id", "equals", catalogAlpha))
			}
			var changed []string
			switch id {
			case "season_identity":
				patch(seasonID, `{"title":"Renamed season","overview":"Edited season"}`, admin, ok(seasonID, "season", "Renamed season"))
				want[seasonID]["title"] = "Renamed season"
				want[seasonID]["overview"] = "Edited season"
				changed = []string{seasonID}
			case "episode_identity":
				patch(episodeID, `{"title":"Renamed episode","runtime":52,"air_date":"2024-02-29"}`, admin, ok(episodeID, "episode", "Renamed episode"))
				want[episodeID]["title"] = "Renamed episode"
				want[episodeID]["runtime"] = float64(52)
				want[episodeID]["air_date"] = "2024-02-29"
				changed = []string{episodeID}
			case "child_nulls":
				patch(seasonID, `{"title":null,"overview":null,"air_date":null}`, admin, ok(seasonID, "season", "Synthetic season"))
				patch(episodeID, `{"title":null,"runtime":null,"air_date":null}`, admin, ok(episodeID, "episode", "Synthetic episode"))
				changed = []string{seasonID, episodeID}
			case "child_permissions":
				patch(seasonID, `{"title":"Must not persist"}`, scenariocatalog.Principal{Class: "primary_profile"}, catalogProblem(403, "permission_denied"))
				patch(episodeID, `{"title":"Must not persist"}`, scenariocatalog.Principal{Class: "acting_admin", Profile: "admin_secondary"}, catalogProblem(403, "permission_denied"))
			default:
				t.Fatal("unimplemented case")
			}
			got := snapshot()
			for _, target := range changed {
				delete(want[target], "updated_at")
				delete(got[target], "updated_at")
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("parent/child rows differ: want%v got%v", want, got)
			}
		})
	}
	if len(results) != 4 || requests != 6 || effects != 8 {
		t.Errorf("NEW child evidence%d/%d/%d,want4/6/8", len(results), requests, effects)
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
		}{"NEW season/episode curation (outside frozen 598)", len(results), requests, effects, results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
