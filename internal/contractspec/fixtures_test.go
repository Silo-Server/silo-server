package contractspec

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	contracts "github.com/Silo-Server/silo-server/contracts/api/v2"
)

// TestCommittedFixturesValidate is the fixture gate: every committed body
// satisfies the OpenAPI schema its index entry names.
func TestCommittedFixturesValidate(t *testing.T) {
	if findings := ValidateFixtures(contracts.FS, contracts.OpenAPI); len(findings) != 0 {
		t.Fatalf("committed fixtures fail validation:\n  %s", strings.Join(findings, "\n  "))
	}
	if findings := ValidateFixtures(seededFixtures(t), seedDocument(t)); len(findings) != 0 {
		t.Fatalf("seed fixtures fail validation: %v", findings)
	}
	entries, err := fs.ReadDir(contracts.FS, contracts.FixturesDir)
	if err != nil || len(entries) < 2 {
		t.Fatalf("fixtures dir: %v, %d entries", err, len(entries))
	}
}

// seededFixtures keeps the existing index entries and bodies targeted by the
// mutation matrix. The complete committed tree is validated above.
func seededFixtures(t *testing.T) fstest.MapFS {
	t.Helper()
	schema, err := contracts.FS.ReadFile(contracts.FixturesSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := contracts.FS.ReadFile("fixtures/index.json")
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Fixtures []map[string]any `json:"fixtures"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	m := fstest.MapFS{contracts.FixturesSchemaPath: {Data: schema}}
	kept := index.Fixtures[:0]
	for _, fixture := range index.Fixtures {
		switch fixture["name"] {
		case "not_found", "get_system_info_ok", "guarded_delete_ok", "rate_limited":
			kept = append(kept, fixture)
			if body, ok := fixture["body_file"].(string); ok {
				path := contracts.FixturesDir + "/" + body
				data, err := contracts.FS.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				m[path] = &fstest.MapFile{Data: data}
			}
		}
	}
	if len(kept) != 4 {
		t.Fatalf("seed fixtures missing: got %d, want 4", len(kept))
	}
	index.Fixtures = kept
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	m["fixtures/index.json"] = &fstest.MapFile{Data: data}
	return m
}

func editIndex(t *testing.T, m fstest.MapFS, edit func(fixtures []map[string]any) []map[string]any) {
	t.Helper()
	var index struct {
		Fixtures []map[string]any `json:"fixtures"`
	}
	if err := json.Unmarshal(m["fixtures/index.json"].Data, &index); err != nil {
		t.Fatal(err)
	}
	index.Fixtures = edit(index.Fixtures)
	b, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	m["fixtures/index.json"] = &fstest.MapFile{Data: b}
}

// TestFixtureValidationAcceptsHeadFallback pins that a bodyless HEAD answer
// from the router's operationless 404 fallback is a valid fixture: it names
// no operation and no probe path, like the GET fallback fixture.
func TestFixtureValidationAcceptsHeadFallback(t *testing.T) {
	m := seededFixtures(t)
	editIndex(t, m, func(f []map[string]any) []map[string]any {
		for _, e := range f {
			if e["name"] == "not_found" {
				e["request"].(map[string]any)["method"] = "HEAD"
				e["response_media_type"], e["schema"], e["body_file"] = nil, nil, nil
			}
		}
		return f
	})
	delete(m, "fixtures/not_found.json")
	if findings := ValidateFixtures(m, seedDocument(t)); len(findings) != 0 {
		t.Fatalf("a bodyless HEAD 404 fallback fixture should validate, got:\n%s", strings.Join(findings, "\n"))
	}
}

func TestFixtureValidationSeededFailures(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, m fstest.MapFS)
		want string
	}{
		{"body violates schema", func(t *testing.T, m fstest.MapFS) {
			m["fixtures/not_found.json"] = &fstest.MapFile{Data: []byte(`{"type":"x","title":"t","status":"404","detail":"d","instance":"i"}`)}
		}, "body violates #/components/schemas/Problem"},
		{"body has an undeclared member", func(t *testing.T, m fstest.MapFS) {
			m["fixtures/get_system_info_ok.json"] = &fstest.MapFile{Data: []byte(`{"server_version":"x","api_major":2,"contract_digest":"d","links":{"openapi":"/a","capabilities":"/b"},"extra":1}`)}
		}, "body violates #/components/schemas/SystemInfo"},
		{"unindexed body", func(t *testing.T, m fstest.MapFS) {
			m["fixtures/orphan.json"] = &fstest.MapFile{Data: []byte(`{}`)}
		}, "fixtures/orphan.json is committed but not indexed"},
		{"missing body file", func(t *testing.T, m fstest.MapFS) {
			delete(m, "fixtures/not_found.json")
		}, "fixtures/not_found: open fixtures/not_found.json"},
		{"unknown operation", func(t *testing.T, m fstest.MapFS) {
			editIndex(t, m, func(f []map[string]any) []map[string]any { f[0]["operation_id"] = "listNothing"; return f })
		}, `operation "listNothing" is not in openapi.json`},
		{"undocumented status", func(t *testing.T, m fstest.MapFS) {
			editIndex(t, m, func(f []map[string]any) []map[string]any {
				for _, e := range f {
					if e["name"] == "get_system_info_ok" {
						e["expected_status"] = 418
						e["response_media_type"] = "application/problem+json"
						e["response_headers"] = map[string]any{"Content-Type": "application/problem+json"}
					}
				}
				return f
			})
		}, "does not document status 418 on getSystemInfo"},
		{"status does not match the problem body", func(t *testing.T, m fstest.MapFS) {
			editIndex(t, m, func(f []map[string]any) []map[string]any {
				for _, e := range f {
					if e["name"] == "not_found" {
						e["expected_status"] = 410
					}
				}
				return f
			})
		}, "problem status 404 != expected_status 410"},
		{"index violates its schema", func(t *testing.T, m fstest.MapFS) {
			editIndex(t, m, func(f []map[string]any) []map[string]any { f[0]["schema"] = "Problem"; return f })
		}, "violates fixtures.schema.json"},
		{"unknown schema pointer", func(t *testing.T, m fstest.MapFS) {
			editIndex(t, m, func(f []map[string]any) []map[string]any { f[0]["schema"] = "#/components/schemas/Nope"; return f })
		}, "schema #/components/schemas/Nope"},
		{"guarded 204 DELETE records an ETag", func(t *testing.T, m fstest.MapFS) {
			editIndex(t, m, func(f []map[string]any) []map[string]any {
				for _, e := range f {
					if e["name"] == "guarded_delete_ok" {
						e["response_headers"].(map[string]any)["ETag"] = `"stale"`
					}
				}
				return f
			})
		}, "a guarded 204 DELETE fixture records an ETag"},
		{"response headers spell ETag twice", func(t *testing.T, m fstest.MapFS) {
			editIndex(t, m, func(f []map[string]any) []map[string]any {
				for _, e := range f {
					if e["name"] == "guarded_delete_ok" {
						h := e["response_headers"].(map[string]any)
						h["ETag"] = ""
						h["etag"] = `"stale"`
					}
				}
				return f
			})
		}, `response headers spell "ETag/etag" more than once`},
		{"HEAD fixture with a body file", func(t *testing.T, m fstest.MapFS) {
			editIndex(t, m, func(f []map[string]any) []map[string]any {
				for _, e := range f {
					if e["name"] == "get_system_info_ok" {
						e["request"].(map[string]any)["method"] = "HEAD"
					}
				}
				return f
			})
		}, "/body_file': got string, want null"},
		{"guarded 204 DELETE records a lowercase etag", func(t *testing.T, m fstest.MapFS) {
			editIndex(t, m, func(f []map[string]any) []map[string]any {
				for _, e := range f {
					if e["name"] == "guarded_delete_ok" {
						e["response_headers"].(map[string]any)["etag"] = `"stale"`
					}
				}
				return f
			})
		}, "a guarded 204 DELETE fixture records an ETag"},
		{"HEAD 429 without Retry-After", func(t *testing.T, m fstest.MapFS) {
			// A bodyless HEAD answer still owes its status-specific headers.
			editIndex(t, m, func(f []map[string]any) []map[string]any {
				for _, e := range f {
					if e["name"] == "rate_limited" {
						e["request"].(map[string]any)["method"] = "HEAD"
						delete(e["response_headers"].(map[string]any), "Retry-After")
						e["response_media_type"], e["schema"], e["body_file"] = nil, nil, nil
					}
				}
				return f
			})
		}, "a 429 fixture must record Retry-After"},
		{"429 without Retry-After", func(t *testing.T, m fstest.MapFS) {
			editIndex(t, m, func(f []map[string]any) []map[string]any {
				for _, e := range f {
					if e["name"] == "rate_limited" {
						delete(e["response_headers"].(map[string]any), "Retry-After")
					}
				}
				return f
			})
		}, "a 429 fixture must record Retry-After"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := seededFixtures(t)
			tc.seed(t, m)
			findings := ValidateFixtures(m, seedDocument(t))
			joined := strings.Join(findings, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("want a finding containing %q, got:\n%s", tc.want, joined)
			}
		})
	}
}
