package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
)

type schemaCacheIsolationProbeBody struct {
	Value string `json:"value"`
}

func TestRuntimeSchemaRegistryIsolation(t *testing.T) {
	probeName := nativeSchemaName(reflect.TypeFor[schemaCacheIsolationProbeBody](), "")
	base := sharedRuntimeSchemas()
	if _, exists := base.schemas[probeName]; exists {
		t.Fatal("probe schema unexpectedly present in shared snapshot")
	}

	baseReqBefore := slices.Clone(base.schemas["Problem"].Required)
	baseTitleDescBefore := base.schemas["Problem"].Properties["title"].Description
	var probeSchemas huma.Registry
	_ = NewHandler(Dependencies{
		testRegister: func(reg *Registry) {
			// Mutating a schema obtained via SchemaFromRef before Map() must
			// stay isolated to this handler.
			refProblem := reg.api.OpenAPI().Components.Schemas.SchemaFromRef(schemaRefPrefix + "Problem")
			refProblem.Required = append(refProblem.Required, "ref-mutated")
			refProblem.Properties["title"].Description = "ref-mutated-description"

			type probeOut struct {
				Body schemaCacheIsolationProbeBody
			}
			Register(reg, Operation{
				Operation: humaOp(http.MethodGet, Prefix+"/probe/schema-cache-isolation", "probeSchemaCacheIsolation", "system", "Schema cache isolation probe."),
				Class:     ClassPublic,
			}, func(context.Context, *struct{}) (*probeOut, error) {
				return &probeOut{}, nil
			})
			probeSchemas = reg.api.OpenAPI().Components.Schemas
		},
	})

	if _, exists := base.schemas[probeName]; exists {
		t.Fatal("testRegister probe leaked into sharedRuntimeSchemas")
	}
	if _, exists := probeSchemas.Map()[probeName]; !exists {
		t.Fatalf("testRegister probe %q missing from instance schema map", probeName)
	}
	if !slices.Equal(base.schemas["Problem"].Required, baseReqBefore) {
		t.Fatalf("SchemaFromRef mutation leaked into sharedRuntimeSchemas: got %v, want %v", base.schemas["Problem"].Required, baseReqBefore)
	}
	if base.schemas["Problem"].Properties["title"].Description != baseTitleDescBefore {
		t.Fatalf("SchemaFromRef nested property mutation leaked into sharedRuntimeSchemas: got %q, want %q", base.schemas["Problem"].Properties["title"].Description, baseTitleDescBefore)
	}

	// Mutating a schema (including nested property schemas and nested example
	// slices) obtained from Map() must not alter the shared snapshot.
	copiedProblem := probeSchemas.Map()["Problem"]
	copiedProblem.Required = append(copiedProblem.Required, "mutated-field")
	copiedProblem.Properties["title"].Description = "mutated-description"
	copiedProblem.Properties["injected"] = &huma.Schema{Type: huma.TypeString}
	if !slices.Equal(base.schemas["Problem"].Required, baseReqBefore) {
		t.Fatalf("Map mutation leaked into sharedRuntimeSchemas: got %v, want %v", base.schemas["Problem"].Required, baseReqBefore)
	}
	if base.schemas["Problem"].Properties["title"].Description != baseTitleDescBefore {
		t.Fatalf("nested property mutation leaked into sharedRuntimeSchemas: got %q, want %q", base.schemas["Problem"].Properties["title"].Description, baseTitleDescBefore)
	}
	if _, leaked := base.schemas["Problem"].Properties["injected"]; leaked {
		t.Fatal("injected property map entry leaked into sharedRuntimeSchemas")
	}

	// Registering a duplicate multipart form schema name must still panic.
	var panicked any
	func() {
		defer func() { panicked = recover() }()
		_ = NewHandler(Dependencies{
			testRegister: func(reg *Registry) {
				type dupFormInput struct {
					RawBody huma.MultipartFormFiles[ProfileAvatarForm]
				}
				Register(reg, Operation{
					Operation:   humaOp(http.MethodPost, Prefix+"/probe/dup-avatar-form", "probeDupAvatarForm", "system", "Duplicate form schema probe."),
					Class:       ClassPublic,
					RetrySafety: RetrySafetyCoalescing,
				}, func(context.Context, *dupFormInput) (*struct{}, error) {
					return &struct{}{}, nil
				})
			},
		})
	}()
	if panicked == nil || !strings.Contains(strings.ToLower(toString(panicked)), "already registered") {
		t.Fatalf("duplicate multipart form registration panic = %v, want already registered", panicked)
	}
}

func TestRuntimeSchemaRegistryNestedExampleIsolation(t *testing.T) {
	base := sharedRuntimeSchemas()
	basePermEx := base.schemas["Account"].Properties["permissions"].Examples
	if len(basePermEx) == 0 {
		t.Fatal("Account.permissions has no examples to test nested example isolation")
	}
	reg := newRuntimeSchemaRegistry()
	copiedPermEx := reg.Map()["Account"].Properties["permissions"].Examples
	switch ex := copiedPermEx[0].(type) {
	case []any:
		if len(ex) == 0 {
			t.Fatal("Account.permissions example slice is empty")
		}
		orig := basePermEx[0].([]any)[0]
		ex[0] = "mutated-permission"
		if basePermEx[0].([]any)[0] != orig {
			t.Fatalf("nested []any example mutation leaked into sharedRuntimeSchemas: got %v, want %v", basePermEx[0].([]any)[0], orig)
		}
	case []string:
		if len(ex) == 0 {
			t.Fatal("Account.permissions example slice is empty")
		}
		orig := basePermEx[0].([]string)[0]
		ex[0] = "mutated-permission"
		if basePermEx[0].([]string)[0] != orig {
			t.Fatalf("nested []string example mutation leaked into sharedRuntimeSchemas: got %q, want %q", basePermEx[0].([]string)[0], orig)
		}
	default:
		t.Fatalf("unexpected Account.permissions example type %T", copiedPermEx[0])
	}
}

func TestRuntimeSchemaRegistryAllowRefFalsePreservesRefinements(t *testing.T) {
	reg := newRuntimeSchemaRegistry()
	got := reg.Schema(reflect.TypeFor[AccountPasswordCapability](), false, "")
	if !slices.Contains(got.Required, "allowed") {
		t.Fatalf("Schema(AccountPasswordCapability, allowRef=false).Required = %v, want \"allowed\" preserved from baseSchemaSnapshot", got.Required)
	}
	// Mutating the returned clone and recomputing messages must not affect the snapshot.
	got.Required = append(got.Required, "synthetic")
	got.PrecomputeMessages()
	if slices.Contains(sharedRuntimeSchemas().schemas["AccountPasswordCapability"].Required, "synthetic") {
		t.Fatal("allowRef=false clone mutation leaked into sharedRuntimeSchemas")
	}
}

func TestRuntimeSchemaRegistryPublicCapabilityPrecedence(t *testing.T) {
	var schemas huma.Registry
	_ = NewHandler(Dependencies{
		testRegister: func(reg *Registry) {
			type scopedSharedCapOut struct {
				Status       int
				ETag         string `header:"ETag"`
				CacheControl string `header:"Cache-Control"`
				Body         Capability
			}
			Register(reg, Operation{
				Operation:     humaOp(http.MethodGet, Prefix+"/probe/scoped-shared-cap", "probeScopedSharedCap", "system", "Scoped probe reusing public Capability schema."),
				Class:         ClassAuthenticated,
				ServiceBacked: true,
			}, func(context.Context, *CapabilityInput) (*scopedSharedCapOut, error) {
				return &scopedSharedCapOut{CacheControl: cachePrivateNoCache, Body: Capability{State: StateAvailable}}, nil
			})
			schemas = reg.api.OpenAPI().Components.Schemas
		},
	})
	if slices.Contains(schemas.Map()["Capability"].Required, "allowed") {
		t.Fatalf("public Capability schema gained required \"allowed\" when reused by scoped testRegister op: Required=%v", schemas.Map()["Capability"].Required)
	}
}

func TestRuntimeSchemaRegistryPrecomputeNoRace(t *testing.T) {
	base := sharedRuntimeSchemas()
	problemSchema := base.schemas["Problem"]
	validProblem := map[string]any{
		"type":   "https://silo.example/problems/test",
		"title":  "Test",
		"status": float64(400),
		"detail": "detail",
		"code":   "bad_request",
	}

	var wg sync.WaitGroup
	// Concurrent readers validating directly against the shared snapshot schemas.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg := newRuntimeSchemaRegistry()
			pb := huma.NewPathBuffer([]byte{}, 0)
			res := &huma.ValidateResult{}
			for range 200 {
				pb.Reset()
				res.Reset()
				huma.Validate(reg, problemSchema, pb, huma.ModeReadFromServer, validProblem, res)
			}
		}()
	}
	// Concurrent testRegister writers exercising Map(), Patch[Problem] (which
	// copies a base schema and invokes PrecomputeMessages), and direct
	// PrecomputeMessages after modifying Required.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				_ = NewHandler(Dependencies{
					testRegister: func(reg *Registry) {
						type patchProblemIn struct {
							Body struct {
								Problem Patch[Problem] `json:"problem,omitzero"`
							}
						}
						Register(reg, Operation{
							Operation:   humaOp(http.MethodPatch, Prefix+"/probe/patch-problem", "probePatchProblem", "system", "Patch Problem probe."),
							Class:       ClassPublic,
							RetrySafety: RetrySafetyCoalescing,
						}, func(context.Context, *patchProblemIn) (*struct{}, error) {
							return &struct{}{}, nil
						})
						s := reg.api.OpenAPI().Components.Schemas.Map()["Problem"]
						s.Required = append(s.Required, "extra_required")
						s.PrecomputeMessages()
					},
				})
			}
		}()
	}
	wg.Wait()
	if slices.Contains(base.schemas["Problem"].Required, "extra_required") {
		t.Fatal("PrecomputeMessages test leaked Required entry into sharedRuntimeSchemas")
	}
}

func TestRuntimeSchemaRegistryMatchesUncachedOpenAPI(t *testing.T) {
	uncachedAPI := huma.NewAPI(humaConfig(), noopAdapter{})
	registerAll(&Registry{api: uncachedAPI})

	cachedAPI := huma.NewAPI(runtimeHumaConfig(), noopAdapter{})
	registerAll(&Registry{api: cachedAPI})

	wantPaths, err := json.Marshal(uncachedAPI.OpenAPI().Paths)
	if err != nil {
		t.Fatalf("marshal uncached paths: %v", err)
	}
	gotPaths, err := json.Marshal(cachedAPI.OpenAPI().Paths)
	if err != nil {
		t.Fatalf("marshal cached paths: %v", err)
	}
	if !bytes.Equal(wantPaths, gotPaths) {
		t.Fatal("runtimeHumaConfig() OpenAPI Paths diverge from humaConfig()")
	}

	wantSchemas, err := json.Marshal(uncachedAPI.OpenAPI().Components.Schemas)
	if err != nil {
		t.Fatalf("marshal uncached schemas: %v", err)
	}
	gotSchemas, err := json.Marshal(cachedAPI.OpenAPI().Components.Schemas)
	if err != nil {
		t.Fatalf("marshal cached schemas: %v", err)
	}
	if !bytes.Equal(wantSchemas, gotSchemas) {
		t.Fatal("runtimeHumaConfig() OpenAPI Components.Schemas diverge from humaConfig()")
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case error:
		return x.Error()
	default:
		return ""
	}
}

func TestRuntimeSchemaRegistryConcurrentHandlers(t *testing.T) {
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := NewHandler(Dependencies{})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, Prefix+"/system/info", nil)
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				errs <- rec.Body.String()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Fatalf("concurrent NewHandler request failed: %s", msg)
	}
}
