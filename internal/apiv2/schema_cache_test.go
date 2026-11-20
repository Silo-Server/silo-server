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

func TestRuntimeSchemaRegistryTestRegisterIsolation(t *testing.T) {
	probeName := nativeSchemaName(reflect.TypeFor[schemaCacheIsolationProbeBody](), "")
	base := sharedRuntimeSchemas()
	if _, exists := base.schemas[probeName]; exists {
		t.Fatal("probe schema unexpectedly present in shared snapshot")
	}

	baseReqBefore := slices.Clone(base.schemas["Problem"].Required)
	baseTitleDescBefore := base.schemas["Problem"].Properties["title"].Description
	baseCreateProfile := *(base.schemas["AdminInvitationCreateInputBody"].Properties["create_profile"].Default.(*bool))

	var probeSchemas huma.Registry
	_ = NewHandler(Dependencies{
		testRegister: func(reg *Registry) {
			// Mutating schemas via SchemaFromRef or Map, deleting entries, or
			// mutating pointer defaults must never affect sharedRuntimeSchemas.
			refProblem := reg.api.OpenAPI().Components.Schemas.SchemaFromRef(schemaRefPrefix + "Problem")
			refProblem.Required = append(refProblem.Required, "ref-mutated")
			refProblem.Properties["title"].Description = "ref-mutated-description"

			inv := reg.api.OpenAPI().Components.Schemas.Map()["AdminInvitationCreateInputBody"]
			*(inv.Properties["create_profile"].Default.(*bool)) = !baseCreateProfile
			delete(reg.api.OpenAPI().Components.Schemas.Map(), "Problem")
			if got := reg.api.OpenAPI().Components.Schemas.SchemaFromRef(schemaRefPrefix + "Problem"); got != nil {
				t.Fatalf("SchemaFromRef returned %v after deleting Problem from Map()", got)
			}

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
		t.Fatalf("mutation leaked into sharedRuntimeSchemas: got %v, want %v", base.schemas["Problem"].Required, baseReqBefore)
	}
	if base.schemas["Problem"].Properties["title"].Description != baseTitleDescBefore {
		t.Fatalf("nested property mutation leaked into sharedRuntimeSchemas: got %q, want %q", base.schemas["Problem"].Properties["title"].Description, baseTitleDescBefore)
	}
	if got := *(base.schemas["AdminInvitationCreateInputBody"].Properties["create_profile"].Default.(*bool)); got != baseCreateProfile {
		t.Fatalf("pointer default mutation leaked into sharedRuntimeSchemas: got %v, want %v", got, baseCreateProfile)
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

func TestRuntimeSchemaRegistryReadOnlyGuards(t *testing.T) {
	reg := newRuntimeSchemaRegistry()
	assertPanics(t, "Map", func() { _ = reg.Map() })
	assertPanics(t, "allowRef=false", func() { _ = reg.Schema(reflect.TypeFor[Problem](), false, "") })
	assertPanics(t, "unknown struct", func() { _ = reg.Schema(reflect.TypeFor[schemaCacheIsolationProbeBody](), true, "") })
}

func assertPanics(t *testing.T, name string, fn func()) {
	t.Helper()
	var panicked any
	func() {
		defer func() { panicked = recover() }()
		fn()
	}()
	if panicked == nil {
		t.Fatalf("%s did not panic", name)
	}
}

// TestRuntimeSchemaRegistryProductionParity compares the production
// runtimeHumaConfig() + fromSnapshot: true registration path against uncached
// humaConfig(): every operation's parameters and request body schemas (which
// Huma validates requests against) and every referenced component schema must
// match byte for byte.
func TestRuntimeSchemaRegistryProductionParity(t *testing.T) {
	uncachedAPI := huma.NewAPI(humaConfig(), noopAdapter{})
	registerAll(&Registry{api: uncachedAPI})

	cachedAPI := huma.NewAPI(runtimeHumaConfig(), noopAdapter{})
	registerAll(&Registry{api: cachedAPI, fromSnapshot: true})

	uncachedPaths := uncachedAPI.OpenAPI().Paths
	cachedPaths := cachedAPI.OpenAPI().Paths
	if len(cachedPaths) != len(uncachedPaths) {
		t.Fatalf("cached Paths len = %d, want %d", len(cachedPaths), len(uncachedPaths))
	}
	for path, wantItem := range uncachedPaths {
		gotItem := cachedPaths[path]
		if gotItem == nil {
			t.Fatalf("cached Paths missing %s", path)
		}
		for method, wantOp := range operationByMethod(wantItem) {
			gotOp := operationByMethod(gotItem)[method]
			if gotOp == nil {
				t.Fatalf("cached %s %s missing", method, path)
			}
			assertJSONEqual(t, method+" "+path+" parameters", wantOp.Parameters, gotOp.Parameters)
			assertJSONEqual(t, method+" "+path+" requestBody", wantOp.RequestBody, gotOp.RequestBody)
		}
	}

	uncachedSchemas := uncachedAPI.OpenAPI().Components.Schemas
	cachedSchemas := cachedAPI.OpenAPI().Components.Schemas
	for name, wantSchema := range uncachedSchemas.Map() {
		gotSchema := cachedSchemas.SchemaFromRef(schemaRefPrefix + name)
		if gotSchema == nil {
			t.Fatalf("cached SchemaFromRef missing %s", name)
		}
		assertJSONEqual(t, "schema "+name, wantSchema, gotSchema)
	}
}

func operationByMethod(item *huma.PathItem) map[string]*huma.Operation {
	ops := map[string]*huma.Operation{}
	for method, op := range map[string]*huma.Operation{
		http.MethodGet:     item.Get,
		http.MethodPost:    item.Post,
		http.MethodPut:     item.Put,
		http.MethodPatch:   item.Patch,
		http.MethodDelete:  item.Delete,
		http.MethodHead:    item.Head,
		http.MethodOptions: item.Options,
	} {
		if op != nil {
			ops[method] = op
		}
	}
	return ops
}

func assertJSONEqual(t *testing.T, label string, want, got any) {
	t.Helper()
	wantBytes, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("%s: marshal want: %v", label, err)
	}
	gotBytes, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: marshal got: %v", label, err)
	}
	if !bytes.Equal(wantBytes, gotBytes) {
		t.Fatalf("%s diverged:\nwant: %s\ngot:  %s", label, wantBytes, gotBytes)
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
