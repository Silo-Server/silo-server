package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestRequiredNewViewerLibraries(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-new-viewer-libraries for required NEW acceptance")
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
		UserStoreProvider: e.stores, PolicySystem: e.policy, FolderRepo: catalog.NewFolderRepository(e.pool),
	}))
	defer server.Close()
	var libraryIDs []int
	cleanup := func() { e.mustExec(`DELETE FROM media_folders WHERE id=ANY($1)`, libraryIDs); libraryIDs = nil }
	defer func() { cleanup(); e.Reseed(); e.guardScratchDatabase() }()
	var results []Result
	requests, effects := 0, 0
	for _, id := range []string{"enabled_order", "account_and_profile", "empty_access", "current_access", "authentication"} {
		cleanup()
		e.Reseed()
		for _, f := range []struct {
			name, kind string
			order      int
			enabled    bool
		}{{"Synthetic Zeta", "movies", 20, true}, {"Synthetic Alpha", "ebooks", 10, true}, {"Synthetic Disabled", "movies", 0, false}} {
			var key int
			if err := e.pool.QueryRow(e.ctx, `INSERT INTO media_folders(name,type,sort_order,enabled,poster_path) VALUES($1,$2,$3,$4,'synthetic-private-poster') RETURNING id`, f.name, f.kind, f.order, f.enabled).Scan(&key); err != nil {
				t.Fatal(err)
			}
			libraryIDs = append(libraryIDs, key)
		}
		e.mustExec(`UPDATE access_groups SET library_ids=$1 WHERE is_default`, []int{libraryIDs[0], libraryIDs[2]})
		t.Run(id, func(t *testing.T) {
			result := Result{ID: "new_viewer_libraries." + id + "/v2", Scenario: "new_viewer_libraries." + id, Transport: "v2"}
			defer func() {
				if t.Failed() && len(result.Failures) == 0 {
					result.Failures = append(result.Failures, "scenario assertion failed; see test log")
				}
				results = append(results, result)
			}()
			snapshot := func() []byte {
				t.Helper()
				effects++
				var raw []byte
				if err := e.pool.QueryRow(e.ctx, `SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM media_folders f`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var rows []json.RawMessage
				if err := json.Unmarshal(raw, &rows); err != nil {
					t.Fatal(err)
				}
				if len(rows) != 3 {
					t.Fatalf("library count%d,want3", len(rows))
				}
				return raw
			}
			before := snapshot()
			get := func(suffix string, p scenariocatalog.Principal, expect scenariocatalog.Expect) {
				t.Helper()
				requests++
				resp, failures, err := e.exchange(server.URL, http.MethodGet, scenariocatalog.Request{Path: "/api/v2/user/libraries" + suffix}, p, expect, nil, nil, nil)
				if err != nil {
					failures = append(failures, err.Error())
				}
				result.Failures = append(result.Failures, failures...)
				if len(failures) > 0 {
					t.Fatalf("library GET: %v", failures)
				}
				if bytes.Contains(resp.Raw, []byte("synthetic-private-poster")) {
					t.Fatal("private poster key leaked")
				}
			}
			list := func(indices ...int) scenariocatalog.Expect {
				checks := []scenariocatalog.BodyAssertion{catalogAssertion("/items", "length", len(indices))}
				for i, index := range indices {
					prefix := fmt.Sprintf("/items/%d", i)
					checks = append(checks, catalogAssertion(prefix+"/id", "equals", strconv.Itoa(libraryIDs[index])), catalogAssertion(prefix+"/poster_url", "absent", nil), catalogAssertion(prefix+"/paths", "absent", nil), catalogAssertion(prefix+"/enabled", "absent", nil))
				}
				return catalogOK(checks...)
			}
			switch id {
			case "enabled_order":
				get("/capabilities", scenariocatalog.Principal{Class: "acting_admin"}, catalogOK(catalogAssertion("/available", "equals", true)))
				get("", scenariocatalog.Principal{Class: "acting_admin"}, list(1, 0))
			case "account_and_profile":
				for _, class := range []string{"authenticated", "profile"} {
					get("", scenariocatalog.Principal{Class: class}, list(0))
				}
			case "empty_access":
				e.mustExec(`UPDATE access_groups SET library_ids=$1 WHERE is_default`, []int{})
				get("", scenariocatalog.Principal{Class: "profile"}, catalogOK(catalogAssertion("/items", "equals", []any{})))
			case "current_access":
				get("", scenariocatalog.Principal{Class: "profile"}, list(0))
				e.mustExec(`UPDATE access_groups SET library_ids=$1 WHERE is_default`, []int{libraryIDs[1]})
				get("", scenariocatalog.Principal{Class: "profile"}, list(1))
			case "authentication":
				for _, suffix := range []string{"", "/capabilities"} {
					get(suffix, scenariocatalog.Principal{Class: "public"}, catalogProblem(401, "authentication_required"))
				}
			default:
				t.Fatal("unimplemented case")
			}
			if !bytes.Equal(before, snapshot()) {
				t.Fatal("library reads changed stored folders")
			}
		})
	}
	if len(results) != 5 || requests != 9 || effects != 10 {
		t.Errorf("NEW library evidence%d/%d/%d,want5/9/10", len(results), requests, effects)
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
		}{"NEW viewer library discovery (outside frozen 598)", len(results), requests, effects, results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
