package contractspec

import (
	"encoding/json"
	"strings"
	"testing"

	contracts "github.com/Silo-Server/silo-server/contracts/api/v2"
)

// TestCommittedArtifactPassesLint is the spec-lint gate over the committed
// artifact.
func TestCommittedArtifactPassesLint(t *testing.T) {
	if findings := Lint(contracts.OpenAPI); len(findings) != 0 {
		t.Fatalf("committed openapi.json fails lint:\n  %s", strings.Join(findings, "\n  "))
	}
}

// mutate decodes the committed artifact, applies fn, and re-encodes it.
func mutate(t *testing.T, fn func(doc map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(contracts.OpenAPI, &doc); err != nil {
		t.Fatal(err)
	}
	fn(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func op(doc map[string]any, path, method string) map[string]any {
	return doc["paths"].(map[string]any)[path].(map[string]any)[method].(map[string]any)
}

func schemas(doc map[string]any) map[string]any {
	return doc["components"].(map[string]any)["schemas"].(map[string]any)
}

// TestLintSeededFailures seeds each rule violation into a copy of the
// committed artifact and proves the lint names it.
func TestLintSeededFailures(t *testing.T) {
	const info = "/api/v2/system/info"
	cases := map[string]struct {
		seed func(doc map[string]any)
		want string
	}{
		"duplicate operation id": {
			seed: func(doc map[string]any) { op(doc, info, "get")["operationId"] = "getOpenAPIDocument" },
			want: `operationId "getOpenAPIDocument" duplicates`,
		},
		"implicit operation id": {
			seed: func(doc map[string]any) { delete(op(doc, info, "get"), "operationId") },
			want: "has no operationId",
		},
		"non lowerCamel operation id": {
			seed: func(doc map[string]any) { op(doc, info, "get")["operationId"] = "GetSystemInfo" },
			want: "is not lowerCamelCase",
		},
		"non-PascalCase top-level schema": {
			seed: func(doc map[string]any) { schemas(doc)["system_info"] = schemas(doc)["SystemInfo"] },
			want: "components.schemas.system_info: schema name is not PascalCase",
		},
		"anonymous response schema": {
			seed: func(doc map[string]any) {
				content := op(doc, info, "get")["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)
				content["application/json"] = map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "additionalProperties": false}}
			},
			want: "anonymous object schema",
		},
		"undocumented implied status": {
			seed: func(doc map[string]any) { delete(op(doc, info, "get")["responses"].(map[string]any), "406") },
			want: "status 406 is implied by class public but not documented",
		},
		"undocumented body-read timeout": {
			seed: func(doc map[string]any) {
				delete(op(doc, "/api/v2/profiles/{id}", "patch")["responses"].(map[string]any), "408")
			},
			want: "status 408 is implied by class profile_scoped but not documented",
		},
		"undocumented profile 404": {
			seed: func(doc map[string]any) {
				delete(op(doc, "/api/v2/progress", "get")["responses"].(map[string]any), "404")
			},
			want: "status 404 is implied by class profile_scoped but not documented",
		},
		"undocumented 503 on a service-backed public operation": {
			seed: func(doc map[string]any) {
				delete(op(doc, "/api/v2/system/setup", "get")["responses"].(map[string]any), "503")
			},
			want: "status 503 is implied by class public but not documented",
		},
		"undocumented gated status": {
			seed: func(doc map[string]any) {
				o := op(doc, info, "get")
				o["x-silo-class"] = "authenticated"
				o["security"] = []any{map[string]any{"bearerAuth": []any{}}}
			},
			want: "status 401 is implied by class authenticated but not documented",
		},
		"missing success status": {
			seed: func(doc map[string]any) { delete(op(doc, info, "get")["responses"].(map[string]any), "200") },
			want: "no success status is documented",
		},
		"default response": {
			seed: func(doc map[string]any) {
				op(doc, info, "get")["responses"].(map[string]any)["default"] = map[string]any{"description": "Error"}
			},
			want: "a default response hides undocumented statuses",
		},
		"missing class extension": {
			seed: func(doc map[string]any) { delete(op(doc, info, "get"), "x-silo-class") },
			want: "x-silo-class is missing",
		},
		"missing security on non-public operation": {
			seed: func(doc map[string]any) { op(doc, info, "get")["x-silo-class"] = "authenticated" },
			want: "class authenticated requires the bearerAuth security scheme",
		},
		"security on public operation": {
			seed: func(doc map[string]any) {
				op(doc, info, "get")["security"] = []any{map[string]any{"bearerAuth": []any{}}}
			},
			want: "a public operation must not declare security",
		},
		"free-form response object": {
			seed: func(doc map[string]any) { schemas(doc)["SystemInfo"].(map[string]any)["additionalProperties"] = true },
			want: "components.schemas.SystemInfo: additionalProperties:true without x-silo-extension-bag",
		},
		"free-form nested object": {
			seed: func(doc map[string]any) {
				props := schemas(doc)["SystemInfo"].(map[string]any)["properties"].(map[string]any)
				props["extra"] = map[string]any{"type": "object"}
			},
			want: "components.schemas.SystemInfo.extra: object without properties or additionalProperties:false is free-form",
		},
		"missing security scheme": {
			seed: func(doc map[string]any) { delete(doc["components"].(map[string]any), "securitySchemes") },
			want: "components.securitySchemes.bearerAuth is missing",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			findings := Lint(mutate(t, tc.seed))
			for _, f := range findings {
				if strings.Contains(f, tc.want) {
					return
				}
			}
			t.Fatalf("lint did not report %q; findings:\n  %s", tc.want, strings.Join(findings, "\n  "))
		})
	}
}
