package executor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// These NEW cases exercise the actual router and filesystem provider. They do
// not add or alter scenarios in the frozen paired acceptance oracle.
func TestRequiredNewCatalogSourceBrowse(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-new-catalog-sources for required NEW acceptance")
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
	}))
	defer server.Close()
	defer func() { e.Reseed(); e.guardScratchDatabase() }()
	var results []Result
	requests := 0
	ids := []string{"directory_pages", "prefix", "empty", "cursor_path", "cursor_prefix", "cursor_profile", "cursor_operation", "member_primary", "admin_secondary", "anonymous", "foreign_profile", "missing_directory", "invalid_limit"}
	for _, id := range ids {
		e.Reseed()
		t.Run(id, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"Gamma", "Beta", "Alpha"} {
				if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "ordinary-file"), []byte("synthetic"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "Alpha"), filepath.Join(root, "Link")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken")); err != nil {
				t.Fatal(err)
			}
			result := Result{ID: "new_catalog_sources." + id + "/v2", Scenario: "new_catalog_sources." + id, Transport: "v2"}
			defer func() { results = append(results, result) }()
			admin := scenariocatalog.Principal{Class: "acting_admin"}
			const browse = "/api/v2/admin/filesystem/browse"
			get := func(path string, query map[string]string, principal scenariocatalog.Principal, expect scenariocatalog.Expect) response {
				t.Helper()
				requests++
				resp, failures, err := e.exchange(server.URL, http.MethodGet, scenariocatalog.Request{Path: path, Query: query}, principal, expect, nil, nil, nil)
				if err != nil {
					failures = append(failures, err.Error())
				}
				result.Failures = append(result.Failures, failures...)
				if len(failures) > 0 {
					t.Fatalf("GET %s: %v", path, failures)
				}
				return resp
			}
			firstPage := func() string {
				t.Helper()
				resp := get(browse, map[string]string{"path": root, "limit": "2"}, admin, catalogOK(
					catalogAssertion("/items", "length", 2), catalogAssertion("/items/0/name", "equals", "Alpha"),
					catalogAssertion("/items/1/name", "equals", "Beta"), catalogAssertion("/page/has_more", "equals", true)))
				var wire struct {
					Page struct {
						NextCursor string `json:"next_cursor"`
					} `json:"page"`
				}
				if err := json.Unmarshal(resp.Raw, &wire); err != nil || wire.Page.NextCursor == "" {
					result.Failures = append(result.Failures, "missing or undecodable continuation")
					t.Fatalf("cursor decode: %v", err)
				}
				return wire.Page.NextCursor
			}
			switch id {
			case "directory_pages":
				cursor := firstPage()
				get(browse, map[string]string{"path": root, "limit": "2", "cursor": cursor}, admin, catalogOK(
					catalogAssertion("/items", "length", 2), catalogAssertion("/items/0/name", "equals", "Gamma"),
					catalogAssertion("/items/1/name", "equals", "Link"), catalogAssertion("/items/1/path", "equals", filepath.Join(root, "Link")),
					catalogAssertion("/path", "equals", root), catalogAssertion("/parent", "equals", filepath.Dir(root)),
					catalogAssertion("/page/has_more", "equals", false), catalogAssertion("/page/next_cursor", "absent", nil)))
			case "prefix":
				get(browse, map[string]string{"path": root, "name_prefix": "aL"}, admin, catalogOK(catalogAssertion("/items", "length", 1), catalogAssertion("/items/0/name", "equals", "Alpha"), catalogAssertion("/page/has_more", "equals", false)))
			case "empty":
				get(browse, map[string]string{"path": root, "name_prefix": "absent"}, admin, catalogOK(catalogAssertion("/items", "length", 0), catalogAssertion("/page/has_more", "equals", false)))
			case "cursor_path", "cursor_prefix", "cursor_profile", "cursor_operation":
				query := map[string]string{"path": root, "cursor": firstPage()}
				path := browse
				principal := admin
				switch id {
				case "cursor_path":
					query["path"] = filepath.Join(root, "Alpha")
				case "cursor_prefix":
					query["name_prefix"] = "Alpha"
				case "cursor_profile":
					principal = scenariocatalog.Principal{Class: "admin"}
				case "cursor_operation":
					path = "/api/v2/admin/catalog/local-import-sources"
					delete(query, "path")
				}
				get(path, query, principal, catalogProblem(400, "invalid_cursor"))
			case "member_primary":
				get(browse, map[string]string{"path": root}, scenariocatalog.Principal{Class: "primary_profile"}, catalogProblem(403, "permission_denied"))
			case "admin_secondary":
				get(browse, map[string]string{"path": root}, scenariocatalog.Principal{Class: "acting_admin", Profile: "admin_secondary"}, catalogProblem(403, "permission_denied"))
			case "anonymous":
				get(browse, map[string]string{"path": root}, scenariocatalog.Principal{Class: "public"}, catalogProblem(401, "authentication_required"))
			case "foreign_profile":
				get(browse, map[string]string{"path": root}, scenariocatalog.Principal{Class: "acting_admin", Profile: "primary"}, catalogProblem(404, "not_found"))
			case "missing_directory":
				get(browse, map[string]string{"path": filepath.Join(root, "missing")}, admin, catalogProblem(404, "not_found"))
			case "invalid_limit":
				get(browse, map[string]string{"path": root, "limit": "201"}, admin, catalogProblem(422, "validation_failed"))
			default:
				result.Failures = append(result.Failures, "unimplemented case")
				t.Fatal("unimplemented case")
			}
		})
	}
	if len(results) != 13 || requests != 18 {
		t.Errorf("NEW source evidence: %d results/%d requests, want13/18", len(results), requests)
	}
	seen := map[string]bool{}
	for _, result := range results {
		if seen[result.ID] {
			t.Errorf("duplicate NEW result %s", result.ID)
		}
		seen[result.ID] = true
		if !result.Passed() {
			t.Errorf("required NEW case %s failed: %v", result.ID, result.Failures)
		}
	}
	if path := os.Getenv("SILO_SCENARIO_REPORT"); path != "" {
		report := struct {
			Scope                          string
			NewScenarios, PhysicalRequests int
			Results                        []Result
		}{"NEW admin catalog source browse (outside frozen 598)", len(results), requests, results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
