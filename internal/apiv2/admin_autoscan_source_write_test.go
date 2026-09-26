package apiv2

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/danielgtaylor/huma/v2"
)

type fakeSourceWrites struct {
	calls int
	id    string
	input handlers.AdminAutoscanSourceWrite
	err   error
}

func (f *fakeSourceWrites) CreateAdminAutoscanSource(_ context.Context, in handlers.AdminAutoscanSourceWrite) (handlers.AdminAutoscanSourceView, error) {
	f.calls++
	f.input = in
	return handlers.AdminAutoscanSourceView{ID: "created", DeliveryMode: "poll"}, f.err
}
func (f *fakeSourceWrites) UpdateAdminAutoscanSource(_ context.Context, id string, in handlers.AdminAutoscanSourceWrite) (handlers.AdminAutoscanSourceView, error) {
	f.id = id
	return f.CreateAdminAutoscanSource(context.Background(), in)
}
func TestAdminAutoscanSourceWriteTransport(t *testing.T) {
	for _, method := range []string{"POST", "PUT"} {
		t.Run(method, func(t *testing.T) {
			f := new(fakeSourceWrites)
			deps := pilotDeps(nil, nil)
			deps.AdminAutoscanSourceWrites = f
			h := NewHandler(deps)
			path := Prefix + "/admin/autoscan/sources"
			status := 201
			body := `{"plugin_id":"plugin","capability_id":"cap","enabled":true,"path_rewrites":[{"from":"a","to":"b"}]}`
			if method == "PUT" {
				path += "/source"
				status = 200
				body = `{"enabled":false,"connection_id":null,"path_rewrites":[]}`
			}
			requireProblem(t, do(t, h, method, path, body, nil), TypeAuthenticationRequired)
			requireProblem(t, do(t, h, method, path, body, bearer(memberToken)), TypePermissionDenied)
			if f.calls != 0 {
				t.Fatal("unauthorized dispatch")
			}
			rec := do(t, h, method, path, body, bearer(adminToken))
			if rec.Code != status || f.calls != 1 || !strings.Contains(rec.Body.String(), `"id":"created"`) {
				t.Fatal(rec.Code, rec.Body.String(), f.calls)
			}
			if method == "PUT" && (f.id != "source" || f.input.ConnectionID != nil) {
				t.Fatal(f)
			}
			rec = do(t, h, method, path, `{"enabled":true,"path_rewrites":[],"plugin_id":"p","capability_id":"c","poll_interval_seconds":0}`, bearer(adminToken))
			if rec.Code != 422 || f.calls != 1 {
				t.Fatal(rec.Code, f.calls)
			}
			for _, tc := range []struct {
				err    error
				status int
			}{{handlers.ErrAdminAutoscanSourceWriteInvalid, 422}, {handlers.ErrAdminAutoscanSourceWriteUnavailable, 503}, {autoscan.ErrNotFound, 404}, {errors.New("private-store"), 500}} {
				f.err = tc.err
				rec = do(t, h, method, path, body, bearer(adminToken))
				if rec.Code != tc.status || strings.Contains(rec.Body.String(), "private-store") {
					t.Fatal(rec.Code, rec.Body.String())
				}
			}
			deps.AdminAutoscanSourceWrites = nil
			requireProblem(t, do(t, NewHandler(deps), method, path, body, bearer(adminToken)), TypeDependencyUnavailable)
		})
	}
}

func TestAdminAutoscanSourceWriteNullableSchemas(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[AdminAutoscanSourceCreateBody](), reflect.TypeFor[AdminAutoscanSourceWriteBody]()} {
		registry := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
		schema := huma.SchemaFromType(registry, typ)
		for _, field := range []string{"connection_id", "poll_interval_seconds"} {
			if !schema.Properties[field].Nullable || slices.Contains(schema.Required, field) {
				t.Fatalf("%s.%s must allow explicit null and omission", typ.Name(), field)
			}
		}
	}
}
