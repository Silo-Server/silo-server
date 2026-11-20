package apiv2

import (
	"context"
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

	var probeSchemas huma.Registry
	_ = NewHandler(Dependencies{
		testRegister: func(reg *Registry) {
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

	// Mutating a schema obtained from Map() must not alter the shared snapshot.
	baseReqBefore := slices.Clone(base.schemas["Problem"].Required)
	copiedProblem := probeSchemas.Map()["Problem"]
	copiedProblem.Required = append(copiedProblem.Required, "mutated-field")
	if !slices.Equal(base.schemas["Problem"].Required, baseReqBefore) {
		t.Fatalf("Map mutation leaked into sharedRuntimeSchemas: got %v, want %v", base.schemas["Problem"].Required, baseReqBefore)
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
