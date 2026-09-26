package executor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/scenariocatalog"
	"github.com/Silo-Server/silo-server/internal/secret"
)

const (
	catalogAlpha  = "movie:acceptance-alpha"
	catalogBeta   = "movie:acceptance-beta"
	catalogHidden = "movie:acceptance-hidden"
)

// This fixture is installed only after New has guarded/migrated the dedicated
// database. Exact inserted IDs are removed before every ordinary guarded reseed;
// the existing refusal of unexpected media is never relaxed.
type catalogMediaFixture struct {
	libraries []int
	items     []string
	files     []int
}

func (f *catalogMediaFixture) cleanup(t *testing.T, e *Env) {
	t.Helper()
	for _, stmt := range []struct {
		sql string
		ids any
	}{
		{`DELETE FROM media_files WHERE id=ANY($1)`, f.files},
		{`DELETE FROM media_items WHERE content_id=ANY($1)`, f.items},
		{`DELETE FROM media_folders WHERE id=ANY($1)`, f.libraries},
	} {
		if _, err := e.pool.Exec(e.ctx, stmt.sql, stmt.ids); err != nil {
			t.Fatalf("catalog fixture cleanup: %v", err)
		}
	}
	*f = catalogMediaFixture{}
}

func (f *catalogMediaFixture) seed(t *testing.T, e *Env) {
	t.Helper()
	for _, name := range []string{"Acceptance allowed", "Acceptance denied"} {
		var id int
		if err := e.pool.QueryRow(e.ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		f.libraries = append(f.libraries, id)
	}
	e.mustExec(`UPDATE access_groups SET library_ids=$1 WHERE is_default`, []int{f.libraries[0]})
	for _, item := range []struct {
		id, title, rating string
		library           int
	}{
		{catalogAlpha, "Alpha", "PG", f.libraries[0]}, {catalogBeta, "Beta", "R", f.libraries[0]}, {catalogHidden, "Hidden", "PG", f.libraries[1]},
	} {
		e.mustExec(`INSERT INTO media_items(content_id,type,title,sort_title,status,content_rating,genres) VALUES($1,'movie',$2,$2,'matched',$3,'{}')`, item.id, item.title, item.rating)
		f.items = append(f.items, item.id)
		e.mustExec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, item.id, item.library)
	}
	// Alpha has two permitted versions and a third version in the denied library.
	e.mustExec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, catalogAlpha, f.libraries[1])
	for i, file := range []struct {
		item    string
		library int
	}{
		{catalogAlpha, f.libraries[0]}, {catalogAlpha, f.libraries[0]}, {catalogAlpha, f.libraries[1]}, {catalogBeta, f.libraries[0]}, {catalogHidden, f.libraries[1]},
	} {
		var id int
		if err := e.pool.QueryRow(e.ctx, `INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,resolution,created_at) VALUES($1,$2,$3,1024,'1080p','2026-01-02T03:04:05Z') RETURNING id`, file.item, file.library, fmt.Sprintf("/synthetic/acceptance/catalog-%d.mkv", i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		f.files = append(f.files, id)
	}
}

func catalogAssertion(pointer, op string, value any) scenariocatalog.BodyAssertion {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	} // All callers supply literal JSON-compatible fixture values.
	return scenariocatalog.BodyAssertion{Pointer: pointer, Op: op, Value: raw}
}

func catalogOK(checks ...scenariocatalog.BodyAssertion) scenariocatalog.Expect {
	return scenariocatalog.Expect{Status: 200, Headers: []scenariocatalog.HeaderAssertion{
		{Name: "Content-Type", Op: "equals", Value: "application/json"}, {Name: "Cache-Control", Op: "equals", Value: "no-store"},
	}, Body: checks}
}

func catalogProblem(status int, code string) scenariocatalog.Expect {
	return scenariocatalog.Expect{Status: status, Headers: []scenariocatalog.HeaderAssertion{{Name: "Content-Type", Op: "equals", Value: "application/problem+json"}}, Body: []scenariocatalog.BodyAssertion{
		catalogAssertion("/status", "equals", status), catalogAssertion("/type", "matches", "/docs/api/v2/problems/"+code+"$"),
	}}
}

func TestRequiredNewCatalogMediaReads(t *testing.T) {
	if os.Getenv("SILO_SCENARIO_REQUIRED") != "1" {
		t.Skip("run make test-scenario-new-catalog-reads for required NEW acceptance")
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
		FileRepo: scanner.NewFileRepository(e.pool), FolderRepo: catalog.NewFolderRepository(e.pool),
	}))
	defer server.Close()
	var fixture catalogMediaFixture
	defer func() { fixture.cleanup(t, e); e.Reseed(); e.guardScratchDatabase() }()
	var results []Result
	requests := 0
	// Fixed IDs are NEW regression cases, never additions to the frozen 598 oracle.
	ids := []string{"browse_visible", "child_rating", "cursor_traversal", "cursor_profile_binding", "detail_identity", "versions_visibility", "denied_item", "denied_versions", "file_id_is_not_item_id", "foreign_file_hint", "foreign_profile", "missing_profile", "anonymous", "unknown_item"}
	for _, id := range ids {
		fixture.cleanup(t, e)
		e.Reseed()
		fixture.seed(t, e)
		t.Run(id, func(t *testing.T) {
			result := Result{ID: "new_catalog_media." + id + "/v2", Scenario: "new_catalog_media." + id, Transport: "v2"}
			defer func() { results = append(results, result) }()
			get := func(path string, query map[string]string, principal scenariocatalog.Principal, expect scenariocatalog.Expect) response {
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
			member := scenariocatalog.Principal{Class: "profile"}
			detail := "/api/v2/catalog/items/" + catalogAlpha
			versionChecks := func(pointer string) []scenariocatalog.BodyAssertion {
				return []scenariocatalog.BodyAssertion{
					catalogAssertion(pointer, "length", 2),
					catalogAssertion(pointer+"/0/file_id", "type", "string"), catalogAssertion(pointer+"/1/file_id", "type", "string"),
					catalogAssertion(pointer+"/0/file_path", "absent", nil), catalogAssertion(pointer+"/1/file_path", "absent", nil),
				}
			}
			switch id {
			case "browse_visible":
				get("/api/v2/catalog", map[string]string{"sort": "title"}, member, catalogOK(catalogAssertion("/items", "length", 2), catalogAssertion("/items/0/content_id", "equals", catalogAlpha), catalogAssertion("/items/1/content_id", "equals", catalogBeta), catalogAssertion("/total", "equals", 2), catalogAssertion("/total_exact", "equals", true), catalogAssertion("/page/has_more", "equals", false)))
			case "child_rating":
				get("/api/v2/catalog", map[string]string{"sort": "title"}, scenariocatalog.Principal{Class: "child_profile"}, catalogOK(catalogAssertion("/items", "length", 1), catalogAssertion("/items/0/content_id", "equals", catalogAlpha), catalogAssertion("/total", "equals", 1)))
			case "cursor_traversal", "cursor_profile_binding":
				query := map[string]string{"sort": "title", "limit": "1"}
				first := get("/api/v2/catalog", query, member, catalogOK(catalogAssertion("/items", "length", 1), catalogAssertion("/items/0/content_id", "equals", catalogAlpha), catalogAssertion("/page/has_more", "equals", true)))
				var page struct {
					Page struct {
						Next string `json:"next_cursor"`
					} `json:"page"`
				}
				if err := json.Unmarshal(first.Raw, &page); err != nil || page.Page.Next == "" {
					result.Failures = append(result.Failures, "missing continuation")
					t.Fatal("missing continuation")
				}
				query["cursor"] = page.Page.Next
				if id == "cursor_profile_binding" {
					get("/api/v2/catalog", query, scenariocatalog.Principal{Class: "primary_profile"}, catalogProblem(400, "invalid_cursor"))
					break
				}
				get("/api/v2/catalog", query, member, catalogOK(catalogAssertion("/items", "length", 1), catalogAssertion("/items/0/content_id", "equals", catalogBeta), catalogAssertion("/page/has_more", "equals", false), catalogAssertion("/page/next_cursor", "absent", nil)))
			case "detail_identity", "foreign_file_hint":
				query := map[string]string{}
				if id == "foreign_file_hint" {
					query["file_id"] = strconv.Itoa(fixture.files[3])
				}
				checks := append(versionChecks("/versions"), catalogAssertion("/content_id", "equals", catalogAlpha), catalogAssertion("/title", "equals", "Alpha"), catalogAssertion("/genres", "equals", []string{}))
				resp := get(detail, query, member, catalogOK(checks...))
				assertCatalogFileIDs(t, &result, resp.Raw, "versions", fixture.files[:2])
			case "versions_visibility":
				resp := get(detail+"/versions", nil, member, catalogOK(versionChecks("/items")...))
				assertCatalogFileIDs(t, &result, resp.Raw, "items", fixture.files[:2])
			case "denied_item":
				get("/api/v2/catalog/items/"+catalogHidden, nil, member, catalogProblem(404, "not_found"))
			case "denied_versions":
				get("/api/v2/catalog/items/"+catalogHidden+"/versions", nil, member, catalogProblem(404, "not_found"))
			case "file_id_is_not_item_id":
				get("/api/v2/catalog/items/"+strconv.Itoa(fixture.files[0]), nil, member, catalogProblem(404, "not_found"))
			case "foreign_profile":
				get(detail, nil, scenariocatalog.Principal{Class: "profile", Profile: "admin_primary"}, catalogProblem(404, "not_found"))
			case "missing_profile":
				get(detail, nil, scenariocatalog.Principal{Class: "authenticated"}, catalogProblem(422, "validation_failed"))
			case "anonymous":
				get(detail, nil, scenariocatalog.Principal{Class: "public"}, catalogProblem(401, "authentication_required"))
			case "unknown_item":
				get("/api/v2/catalog/items/movie:acceptance-unknown", nil, member, catalogProblem(404, "not_found"))
			default:
				result.Failures = append(result.Failures, "unimplemented required case")
				t.Fatal("unimplemented required case")
			}
		})
	}
	if len(results) != 14 || requests != 16 {
		t.Errorf("NEW catalog evidence: %d results/%d requests, want14/16", len(results), requests)
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
		}{"NEW catalog-media reads (outside frozen 598)", len(results), requests, results}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertCatalogFileIDs(t *testing.T, result *Result, raw []byte, field string, want []int) {
	t.Helper()
	var versions []struct {
		FileID string `json:"file_id"`
	}
	// Decode only the selected member: item detail contains scalar members too.
	var members map[string]json.RawMessage
	err := json.Unmarshal(raw, &members)
	if err == nil {
		err = json.Unmarshal(members[field], &versions)
	}
	var got []int
	if err == nil {
		for _, v := range versions {
			n, e := strconv.Atoi(v.FileID)
			if e != nil {
				err = e
				break
			}
			got = append(got, n)
		}
	}
	expected := slices.Clone(want)
	slices.Sort(expected)
	slices.Sort(got)
	if err != nil || !slices.Equal(got, expected) {
		message := fmt.Sprintf("visible file identities=%v, want%v (decode=%v)", got, expected, err)
		result.Failures = append(result.Failures, message)
		t.Fatal(message)
	}
}
