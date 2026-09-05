package apiv2

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	contracts "github.com/Silo-Server/silo-server/contracts/api/v2"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/routeinventory"
)

// newTestHandler builds the real listener with the probe operations.
func newTestHandler(t *testing.T, deps Dependencies) http.Handler {
	t.Helper()
	deps.testRegister = registerProbes
	return NewHandler(deps)
}

type problemDoc struct {
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Status   int            `json:"status"`
	Detail   string         `json:"detail"`
	Instance string         `json:"instance"`
	Errors   []ProblemError `json:"errors"`
	Extra    map[string]any `json:"-"`
}

// requestIDHeader reads the X-Request-ID header in the contract's exact
// spelling; Header.Get would canonicalize the name to X-Request-Id.
func requestIDHeader(rec *httptest.ResponseRecorder) string {
	vs := rec.Header()[RequestIDHeader] //nolint:staticcheck // contract spelling, set through the map
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}

func do(t *testing.T, h http.Handler, method, path string, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rdr)
	if body != "" && headers["Content-Type"] == "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// requireProblem decodes a problem response and checks the envelope rules
// every problem must satisfy.
func requireProblem(t *testing.T, rec *httptest.ResponseRecorder, want ProblemType) problemDoc {
	t.Helper()
	if rec.Code != want.Status {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, want.Status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", cc)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
	}
	if _, has := raw["$schema"]; has {
		t.Fatalf("problem carries $schema: %s", rec.Body.String())
	}
	var p problemDoc
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p.Type != want.URI() || p.Title != want.Title || p.Status != want.Status || p.Detail == "" {
		t.Fatalf("envelope = %+v, want type %s", p, want.URI())
	}
	id := requestIDHeader(rec)
	if id == "" || p.Instance != "urn:silo:request:"+id {
		t.Fatalf("instance %q does not match X-Request-ID %q", p.Instance, id)
	}
	for _, e := range p.Errors {
		if e.Location == "" || e.Code == "" || e.Detail == "" {
			t.Fatalf("incomplete error entry: %+v", e)
		}
		if !strings.HasPrefix(e.Location, "body") && !strings.HasPrefix(e.Location, "query.") &&
			!strings.HasPrefix(e.Location, "path.") && !strings.HasPrefix(e.Location, "header.") {
			t.Fatalf("location grammar: %q", e.Location)
		}
	}
	if strings.Contains(rec.Body.String(), `"value"`) {
		t.Fatalf("problem echoes a rejected value: %s", rec.Body.String())
	}
	return p
}

// --- Mount and registration -------------------------------------------------

// TestRuntimeReconcile walks the real assembled router and asserts the routes
// it serves are exactly the set the registry declares.
func TestRuntimeReconcile(t *testing.T) {
	var declared []Declared
	deps := Dependencies{testRegister: func(reg *Registry) {
		registerProbes(reg)
		declared = reg.Declared()
	}}
	router := newChiRouter(deps)
	observed, err := routeinventory.Observed(router)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, d := range declared {
		want[d.Method+" "+d.Path] = true
	}
	got := map[string]bool{}
	for _, o := range observed {
		got[o] = true
	}
	for k := range want {
		if !got[k] {
			t.Errorf("declared but not served: %s", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("served but not declared: %s", k)
		}
	}
	if !want["GET /api/v2/system/info"] {
		t.Error("getSystemInfo not declared")
	}
	for _, o := range observed {
		if !strings.HasPrefix(o[strings.Index(o, " ")+1:], Prefix+"/") {
			t.Errorf("route outside %s: %s", Prefix, o)
		}
	}
}

// TestCommittedArtifactMatchesRouter is the route/spec reconciliation over
// the production wiring: every route the real assembled router serves is an
// operation in the COMMITTED contracts/api/v2/openapi.json or a manual
// registry entry, and vice versa. A stale artifact fails here as well as in
// make verify-apiv2-openapi.
func TestCommittedArtifactMatchesRouter(t *testing.T) {
	observed, err := routeinventory.Observed(newChiRouter(Dependencies{}))
	if err != nil {
		t.Fatal(err)
	}
	unaccounted, unserved, err := reconcileSpec(observed, contracts.OpenAPI, RawHandshakes())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range unaccounted {
		t.Errorf("served but in neither openapi.json nor the manual registry: %s", r)
	}
	for _, r := range unserved {
		t.Errorf("documented but not served: %s", r)
	}
	generated, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, contracts.OpenAPI) {
		t.Fatal("contracts/api/v2/openapi.json is stale; run make apiv2-openapi")
	}
	if len(RawHandshakes()) != 0 {
		t.Fatalf("the manual registry is expected to be empty until a raw handshake is ratified: %+v", RawHandshakes())
	}
}

// TestReconcileSpecSeeded proves the reconciliation fires in each direction
// and that a manual-registry entry accounts for a raw route: the one
// test-only entry the placeholder registry carries.
func TestReconcileSpecSeeded(t *testing.T) {
	ws := RawHandshake{Method: http.MethodGet, Path: Prefix + "/probe/ws", Protocol: "websocket", Reason: "test-only raw handshake"}
	observed := []string{
		"GET " + Prefix + "/account/me", "GET " + Prefix + "/admin/users", "GET " + Prefix + "/openapi.json", "PATCH " + Prefix + "/profiles/{id}", "GET " + Prefix + "/progress", "GET " + Prefix + "/system/info", "GET " + Prefix + "/system/setup",
		"GET " + Prefix + "/libraries", "POST " + Prefix + "/libraries", "PATCH " + Prefix + "/libraries/{id}", "DELETE " + Prefix + "/libraries/{id}",
		"POST " + Prefix + "/libraries/{id}/check-mount", "GET " + Prefix + "/libraries/metadata-match-queue", "GET " + Prefix + "/libraries/provider-defaults",
		"POST " + Prefix + "/libraries/reorder", "GET " + Prefix + "/libraries/roots", "PUT " + Prefix + "/libraries/roots/override", "DELETE " + Prefix + "/libraries/roots/override",
		"GET " + Prefix + "/libraries/skipped-roots", "GET " + Prefix + "/libraries/stale-ids", "POST " + Prefix + "/libraries/stale-ids/{content_id}/rematch", "GET " + Prefix + "/libraries/unmatched-items",
		"POST " + Prefix + "/libraries/{id}/confirm-empty-root-cleanup", "GET " + Prefix + "/libraries/{id}/metadata-match-queue", "POST " + Prefix + "/libraries/{id}/metadata-match-queue/retry",
		"POST " + Prefix + "/libraries/{id}/metadata-match-queue/cancel", "POST " + Prefix + "/libraries/{id}/refresh-metadata", "GET " + Prefix + "/libraries/{id}/providers",
		"PUT " + Prefix + "/libraries/{id}/providers", "PUT " + Prefix + "/libraries/{id}/poster", "DELETE " + Prefix + "/libraries/{id}/poster",
		"GET " + Prefix + "/library/{id}/layout", "GET " + Prefix + "/library/{id}/sections", "GET " + Prefix + "/library/{id}/sections/{section_id}/items",
		"GET " + Prefix + "/library/{id}/collections", "GET " + Prefix + "/library/{id}/collections/{collection_id}/items", "GET " + Prefix + "/library/{id}/user-collections",
		"GET " + Prefix + "/favorites", "GET " + Prefix + "/favorites/{item_id}", "PUT " + Prefix + "/favorites/{item_id}", "DELETE " + Prefix + "/favorites/{item_id}",
		"GET " + Prefix + "/ratings", "DELETE " + Prefix + "/ratings/{item_id}",
	}

	unaccounted, unserved, err := reconcileSpec(observed, contracts.OpenAPI, nil)
	if err != nil || len(unaccounted) != 0 || len(unserved) != 0 {
		t.Fatalf("baseline: %v %v %v", unaccounted, unserved, err)
	}
	// A raw route the router serves but nothing describes.
	unaccounted, _, _ = reconcileSpec(append(observed, "GET "+ws.Path), contracts.OpenAPI, nil)
	if len(unaccounted) != 1 || unaccounted[0] != "GET "+ws.Path {
		t.Fatalf("raw route not reported: %v", unaccounted)
	}
	// The same route with its manual-registry entry.
	unaccounted, unserved, _ = reconcileSpec(append(observed, "GET "+ws.Path), contracts.OpenAPI, []RawHandshake{ws})
	if len(unaccounted) != 0 || len(unserved) != 0 {
		t.Fatalf("manual entry did not account for the raw route: %v %v", unaccounted, unserved)
	}
	// A manual entry for a route nobody serves is reported.
	_, unserved, _ = reconcileSpec(observed, contracts.OpenAPI, []RawHandshake{ws})
	if len(unserved) != 1 || !strings.HasPrefix(unserved[0], "GET "+ws.Path) {
		t.Fatalf("unserved manual entry not reported: %v", unserved)
	}
	// A documented operation the router does not serve (stale artifact).
	var withoutInfo []string
	for _, route := range observed {
		if route != "GET "+Prefix+"/system/info" {
			withoutInfo = append(withoutInfo, route)
		}
	}
	_, unserved, _ = reconcileSpec(withoutInfo, contracts.OpenAPI, nil)
	if len(unserved) != 1 || !strings.HasPrefix(unserved[0], "GET "+Prefix+"/system/info") {
		t.Fatalf("stale artifact not reported: %v", unserved)
	}
	// A route cannot be both.
	if _, _, err := reconcileSpec(observed, contracts.OpenAPI, []RawHandshake{{Method: http.MethodGet, Path: Prefix + "/system/info"}}); err == nil {
		t.Fatal("an operation doubling as a manual entry was accepted")
	}
}

// TestDeterministicRegistration: the route table does not depend on the
// wiring; missing gates fail closed at request time.
func TestDeterministicRegistration(t *testing.T) {
	bare, _ := routeinventory.Observed(newChiRouter(Dependencies{testRegister: registerProbes}))
	wired, _ := routeinventory.Observed(newChiRouter(Dependencies{testRegister: registerProbes, Auth: fakeAuth(nil)}))
	if strings.Join(bare, "\n") != strings.Join(wired, "\n") {
		t.Fatalf("route table depends on wiring:\n%v\n%v", bare, wired)
	}
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodPost, "/api/v2/probe/authenticated", `{"name":"x","cleared":null}`, nil)
	requireProblem(t, rec, TypeDependencyUnavailable)
}

func TestRegisterRefusesBadDeclarations(t *testing.T) {
	cases := map[string]Operation{
		"upper id": {Operation: humaOp(http.MethodGet, Prefix+"/x", "GetX", "x", ""), Class: ClassPublic},
		"two tags": {Operation: func() huma_Operation {
			o := humaOp(http.MethodGet, Prefix+"/x", "getX", "x", "")
			o.Tags = append(o.Tags, "y")
			return o
		}(), Class: ClassPublic},
		"trailing": {Operation: humaOp(http.MethodGet, Prefix+"/x/", "getX", "x", ""), Class: ClassPublic},
		"no class": {Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", "")},
		// A declaration that would be inert or fail per request is refused at
		// registration, where a panic is a build failure.
		"demo on public":      {Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""), Class: ClassPublic, DemoRestricted: true},
		"unknown permission":  {Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""), Class: ClassPermissionGated, Permission: "not_a_permission"},
		"curation without id": {Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""), Class: ClassPermissionGated, Permission: policy.PermissionMetadataCuration},
		"huma body limit": {Operation: func() huma_Operation {
			o := humaOp(http.MethodPost, Prefix+"/x", "postX", "x", "")
			o.MaxBodyBytes = 10
			return o
		}(), Class: ClassPublic},
		"negative body limit": {Operation: humaOp(http.MethodPost, Prefix+"/x", "postX", "x", ""), Class: ClassPublic, MaxBodyBytes: -1},
		"perm class":          {Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""), Class: ClassPermissionGated},
	}
	for name, op := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected a panic")
				}
			}()
			newChiRouter(Dependencies{testRegister: func(reg *Registry) {
				Register(reg, op, func(context.Context, *struct{}) (*probeOutput, error) { return nil, nil })
			}})
		})
	}
	t.Run("item-scoped permission with an id parameter", func(t *testing.T) {
		newChiRouter(Dependencies{testRegister: func(reg *Registry) {
			Register(reg, Operation{
				Operation:  humaOp(http.MethodPatch, Prefix+"/items/{id}", "patchItem", "x", ""),
				Class:      ClassPermissionGated,
				Permission: policy.PermissionMetadataCuration,
			}, func(context.Context, *struct {
				ID string `path:"id"`
			}) (*probeOutput, error) {
				return nil, nil
			})
		}})
	})
	t.Run("slice without explode", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil || !strings.Contains(r.(string), "explode") {
				t.Fatalf("recover = %v", r)
			}
		}()
		newChiRouter(Dependencies{testRegister: func(reg *Registry) {
			Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""), Class: ClassPublic},
				func(context.Context, *struct {
					IDs []string `query:"ids"`
				}) (*probeOutput, error) {
					return nil, nil
				})
		}})
	})
	t.Run("uppercase enum", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil || !strings.Contains(r.(string), "lowercase") {
				t.Fatalf("recover = %v", r)
			}
		}()
		newChiRouter(Dependencies{testRegister: func(reg *Registry) {
			Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""), Class: ClassPublic},
				func(context.Context, *struct{}) (*struct {
					Body struct {
						Kind string `json:"kind" enum:"Movie,series"`
					}
				}, error) {
					return nil, nil
				})
		}})
	})
}

// --- Framework configuration -----------------------------------------------

func TestUnknownQueryParameterIs422(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/system/info?bogus=1", "", nil)
	p := requireProblem(t, rec, TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.bogus" || p.Errors[0].Code != "unknown_parameter" {
		t.Fatalf("errors = %+v", p.Errors)
	}
}

func TestUnacceptableAcceptIs406(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/system/info", "", map[string]string{"Accept": "text/xml"})
	requireProblem(t, rec, TypeNotAcceptable)
	for _, accept := range []string{"", "*/*", "application/*", "application/json", "text/html;q=0.9, */*;q=0.1", "application/json; charset=utf-8"} {
		rec := do(t, h, http.MethodGet, "/api/v2/system/info", "", map[string]string{"Accept": accept})
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("Accept %q: %d %s", accept, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	// A bare "json" is not a media type and must not select the alias.
	rec = do(t, h, http.MethodGet, "/api/v2/system/info", "", map[string]string{"Accept": "json"})
	requireProblem(t, rec, TypeNotAcceptable)
	// The most specific matching range decides: an explicit q=0 on JSON is
	// not overridden by a later wildcard, and a q=0 wildcard does not veto an
	// explicit JSON range.
	for _, accept := range []string{"application/json;q=0, */*;q=1", "application/*;q=0, */*", "application/json;q=0"} {
		rec := do(t, h, http.MethodGet, "/api/v2/system/info", "", map[string]string{"Accept": accept})
		if rec.Code != http.StatusNotAcceptable {
			t.Errorf("Accept %q: %d, want 406", accept, rec.Code)
		}
	}
	for _, accept := range []string{"*/*;q=0, application/json", "*/*;q=0, application/*;q=0.5", "text/html, application/json;q=0.1"} {
		rec := do(t, h, http.MethodGet, "/api/v2/system/info", "", map[string]string{"Accept": accept})
		if rec.Code != http.StatusOK {
			t.Errorf("Accept %q: %d, want 200", accept, rec.Code)
		}
	}
}

func TestMediaTypeGuard(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	body := `{"name":"x","cleared":null}`
	rejected := []string{"", "application/vnd.silo+json", "application/json; charset=iso-8859-1", "application/json; boundary=x", "text/plain", "application/json; charset=utf-8; foo=bar"}
	for _, ct := range rejected {
		r := httptest.NewRequest(http.MethodPost, "/api/v2/probe/public", strings.NewReader(body))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q: status %d, want 415: %s", ct, rec.Code, rec.Body.String())
			continue
		}
		requireProblem(t, rec, TypeUnsupportedMediaType)
	}
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "application/json;charset=UTF-8"} {
		rec := do(t, h, http.MethodPost, "/api/v2/probe/public", body, map[string]string{"Content-Type": ct})
		if rec.Code != 200 {
			t.Errorf("Content-Type %q: status %d: %s", ct, rec.Code, rec.Body.String())
		}
	}
}

func TestCollectionsNeverNull(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/probe/list", "", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) || strings.Contains(rec.Body.String(), "null") {
		t.Fatalf("collection body: %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", `{"name":"x","cleared":null}`, nil)
	if !strings.Contains(rec.Body.String(), `"tags":[]`) || !strings.Contains(rec.Body.String(), `"labels":{}`) {
		t.Fatalf("echo body: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "$schema") {
		t.Fatalf("$schema in success body: %s", rec.Body.String())
	}
}

func TestInstantWire(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	// UTC conversion and exactly three fractional digits.
	rec := do(t, h, http.MethodPost, "/api/v2/probe/public", `{"name":"x","when":"2024-03-01T12:34:56.7+02:00","cleared":null}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"when":"2024-03-01T10:34:56.700Z"`) {
		t.Fatalf("when: %d %s", rec.Code, rec.Body.String())
	}
	// Omission when unset; explicit null where the schema allows it.
	if !strings.Contains(rec.Body.String(), `"cleared":null`) {
		t.Fatalf("explicit null lost: %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", `{"name":"x","cleared":null}`, nil)
	if strings.Contains(rec.Body.String(), `"when"`) {
		t.Fatalf("unset instant not omitted: %s", rec.Body.String())
	}
	// Zero-value rejection on input.
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", `{"name":"x","when":"0001-01-01T00:00:00Z","cleared":null}`, nil)
	p := requireProblem(t, rec, TypeValidationFailed)
	if len(p.Errors) == 0 || p.Errors[0].Location != "body.when" {
		t.Fatalf("zero instant: %+v", p.Errors)
	}
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", `{"name":"x","when":"yesterday","cleared":null}`, nil)
	requireProblem(t, rec, TypeValidationFailed)
	// Zero-value refusal on output is an internal error, never a bogus wire value.
	rec = do(t, h, http.MethodGet, "/api/v2/probe/zero", "", nil)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "0001-01-01") {
		t.Fatalf("zero output: %d %s", rec.Code, rec.Body.String())
	}
	if got := NewInstant(time.Date(2024, 1, 2, 3, 4, 5, 123456789, time.FixedZone("x", 3600))).String(); got != "2024-01-02T02:04:05.123Z" {
		t.Fatalf("String = %s", got)
	}
}

func TestSliceQueryUsesRepeatedKeys(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodPost, "/api/v2/probe/public?tags=a&tags=b", `{"name":"x","cleared":null}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"tags":["a","b"]`) {
		t.Fatalf("repeated keys: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public?tags=a,b", `{"name":"x","cleared":null}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"tags":["a,b"]`) {
		t.Fatalf("comma form must not split: %d %s", rec.Code, rec.Body.String())
	}
}

func TestNoBuiltInDocsRoutes(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	for _, path := range []string{"/api/v2/docs", "/api/v2/openapi.yaml", "/api/v2/schemas/Problem.json", "/docs", "/openapi.json"} {
		rec := do(t, h, http.MethodGet, path, "", nil)
		if rec.Code != 404 {
			t.Errorf("%s served %d", path, rec.Code)
		}
	}
	// /api/v2/openapi.json is Silo's own operation (getOpenAPIDocument), not
	// Huma's built-in route: TestOpenAPIDocumentIsTheEmbeddedArtifact.
}

// TestOpenAPIDocumentIsTheEmbeddedArtifact: the served bytes are the committed
// artifact exactly, and the digest the discovery document reports is the
// digest of those bytes.
func TestOpenAPIDocumentIsTheEmbeddedArtifact(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/openapi.json", "", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	if !bytes.Equal(rec.Body.Bytes(), contracts.OpenAPI) {
		t.Fatal("served bytes differ from the embedded artifact")
	}
	sum := sha256.Sum256(rec.Body.Bytes())
	digest := hex.EncodeToString(sum[:])
	if digest != ContractDigest() {
		t.Fatalf("served digest %s != embedded %s", digest, ContractDigest())
	}
	if rec.Header().Get("ETag") != `"`+digest+`"` {
		t.Fatalf("ETag = %q", rec.Header().Get("ETag"))
	}
	var info SystemInfo
	if err := json.Unmarshal(do(t, h, http.MethodGet, "/api/v2/system/info", "", nil).Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.ContractDigest != digest {
		t.Fatalf("system/info digest %s != served %s", info.ContractDigest, digest)
	}
	// The artifact is a parseable OpenAPI 3.1 document with the operation
	// that serves it.
	var doc struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" || doc.Paths["/api/v2/openapi.json"]["get"] == nil {
		t.Fatalf("unexpected document: %s", rec.Body.String())
	}
}

// TestGenerateOpenAPIIsDeterministic: two generations in one process and the
// document's own hygiene rules (no servers, nothing build-specific; schema
// examples are fictional fixture-shaped values and are allowed).
func TestGenerateOpenAPIIsDeterministic(t *testing.T) {
	a, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("generation is not deterministic")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(a, &doc); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"servers", "webhooks", "externalDocs"} {
		if _, ok := doc[forbidden]; ok {
			t.Errorf("document carries %q", forbidden)
		}
	}
	for _, needle := range []string{"/Users/", "/home/", "localhost"} {
		if bytes.Contains(a, []byte(needle)) {
			t.Errorf("document contains %s", needle)
		}
	}
}

func TestBodyCapIsEnforcedBeforeDecoding(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	pad := func(n int64) string {
		prefix := `{"name":"x","cleared":null,"note":"`
		suffix := `"}`
		return prefix + strings.Repeat("a", int(n)-len(prefix)-len(suffix)) + suffix
	}
	rec := do(t, h, http.MethodPost, "/api/v2/probe/public", pad(MaxJSONBodyBytes), nil)
	if rec.Code != 200 {
		t.Fatalf("exactly at the cap: %d %s", rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
	}
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", pad(MaxJSONBodyBytes+1), nil)
	requireProblem(t, rec, TypePayloadTooLarge)
	// Not even valid JSON is required: the cap fires before decoding.
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", strings.Repeat("x", int(MaxJSONBodyBytes)+1), nil)
	requireProblem(t, rec, TypePayloadTooLarge)
}

// TestBodyReadTimeout exercises the 408 boundary over a real connection with
// an injected deadline, so the test does not wait for BodyReadTimeout.
func TestBodyReadTimeout(t *testing.T) {
	if BodyReadTimeout != 30*time.Second {
		t.Fatalf("BodyReadTimeout = %v; #135 ratified the 30 s server baseline on 2026-09-02", BodyReadTimeout)
	}
	h := newTestHandler(t, Dependencies{bodyReadTimeout: 150 * time.Millisecond})
	srv := httptest.NewServer(h)
	defer srv.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.WriteString(conn, "POST /api/v2/probe/public HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"name\":")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufioReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("status = %d body %s", resp.StatusCode, body)
	}
	var p problemDoc
	_ = json.Unmarshal(body, &p)
	if p.Type != TypeRequestTimeout.URI() || resp.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("408 envelope: %s", body)
	}
}

// --- Problem details --------------------------------------------------------

func TestFrameworkAndApplicationValidationShapesMatch(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	framework := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/probe/public", `{"cleared":null}`, nil), TypeValidationFailed)
	app := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/probe/apperror", "", nil), TypeValidationFailed)
	if len(framework.Errors) != 1 || len(app.Errors) != 1 {
		t.Fatalf("errors: %+v / %+v", framework.Errors, app.Errors)
	}
	if framework.Errors[0] != app.Errors[0] {
		t.Fatalf("shapes differ:\n%+v\n%+v", framework.Errors[0], app.Errors[0])
	}
	if framework.Detail != app.Detail || framework.Title != app.Title {
		t.Fatalf("envelopes differ: %+v / %+v", framework, app)
	}
}

func TestFrameworkFailuresMapToCatalog(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	cases := []struct {
		name         string
		method, path string
		body         string
		headers      map[string]string
		want         ProblemType
		location     string
		code         string
	}{
		{"malformed json: truncated", http.MethodPost, "/api/v2/probe/public", `{"name":`, nil, TypeMalformedRequest, "body", "malformed_json"},
		{"malformed json: invalid character", http.MethodPost, "/api/v2/probe/public", `{"name":"x",}`, nil, TypeMalformedRequest, "body", "malformed_json"},
		{"unknown field", http.MethodPost, "/api/v2/probe/public", `{"name":"x","cleared":null,"zzz":1}`, nil, TypeValidationFailed, "body.zzz", "unknown_field"},
		{"missing required", http.MethodPost, "/api/v2/probe/public", `{"cleared":null}`, nil, TypeValidationFailed, "body.name", "required"},
		{"closed enum", http.MethodPost, "/api/v2/probe/public", `{"name":"x","cleared":null,"kind":"Movie"}`, nil, TypeValidationFailed, "body.kind", "invalid_enum"},
		{"range", http.MethodPost, "/api/v2/probe/public", `{"name":"x","cleared":null,"count":11}`, nil, TypeValidationFailed, "body.count", "out_of_range"},
		{"unknown query", http.MethodGet, "/api/v2/system/info?x=1", "", nil, TypeValidationFailed, "query.x", "unknown_parameter"},
		{"limit above max", http.MethodGet, "/api/v2/probe/list?limit=201", "", nil, TypeValidationFailed, "query.limit", "out_of_range"},
		{"bad sort", http.MethodGet, "/api/v2/probe/list?sort=name,-nope", "", nil, TypeValidationFailed, "query.sort", "invalid_sort_field"},
		{"bool literal", http.MethodPost, "/api/v2/probe/public?flag=TRUE", `{"name":"x","cleared":null}`, nil, TypeValidationFailed, "query.flag", "invalid_type"},
		{"bool 1", http.MethodPost, "/api/v2/probe/public?flag=1", `{"name":"x","cleared":null}`, nil, TypeValidationFailed, "query.flag", "invalid_type"},
		{"406", http.MethodGet, "/api/v2/system/info", "", map[string]string{"Accept": "image/png"}, TypeNotAcceptable, "", ""},
		{"405", http.MethodDelete, "/api/v2/system/info", "", nil, TypeMethodNotAllowed, "", ""},
		{"404", http.MethodGet, "/api/v2/nothing", "", nil, TypeNotFound, "", ""},
		{"415", http.MethodPost, "/api/v2/probe/public", `{}`, map[string]string{"Content-Type": "text/json"}, TypeUnsupportedMediaType, "", ""},
		{"invalid path value", http.MethodGet, "/api/v2/probe/item/abc", "", nil, TypeValidationFailed, "path.id", "invalid_type"},
		{"invalid header value", http.MethodGet, "/api/v2/probe/item/7", "", map[string]string{"X-Probe-Count": "abc"}, TypeValidationFailed, "header.x-probe-count", "invalid_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := requireProblem(t, do(t, h, tc.method, tc.path, tc.body, tc.headers), tc.want)
			if tc.location == "" {
				if len(p.Errors) != 0 {
					t.Fatalf("unexpected errors: %+v", p.Errors)
				}
				return
			}
			found := false
			for _, e := range p.Errors {
				if e.Location == tc.location && e.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("want %s/%s in %+v", tc.location, tc.code, p.Errors)
			}
			for _, rejected := range []string{"Movie", "11", "abc"} {
				if strings.Contains(p.Detail+p.Errors[0].Detail, rejected) {
					t.Fatalf("rejected value %q echoed: %+v", rejected, p)
				}
			}
		})
	}
}

func TestQueryGrammar(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/probe/public?flag=true&flag=false", `{"name":"x","cleared":null}`, nil), TypeMalformedRequest)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/system/info?%zz=1", "", nil), TypeMalformedRequest)
	rec := do(t, h, http.MethodGet, "/api/v2/system/info/", "", nil)
	if rec.Code != 404 || rec.Header().Get("Location") != "" {
		t.Fatalf("trailing slash: %d %v", rec.Code, rec.Header())
	}
	rec = do(t, h, http.MethodGet, "/api/v2/probe/list?sort=name,-added_at&limit=5", "", nil)
	if rec.Code != 200 {
		t.Fatalf("valid sort: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRetryAfterOn429And503(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/probe/ratelimited", "", nil)
	requireProblem(t, rec, TypeRateLimited)
	if rec.Header().Get("Retry-After") != "7" {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	rec = do(t, h, http.MethodGet, "/api/v2/probe/unavailable", "", nil)
	requireProblem(t, rec, TypeDependencyUnavailable)
	if rec.Header().Get("Retry-After") != "3" {
		t.Fatalf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
}

func TestPanicIsInternalErrorWithoutLeak(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/probe/panic", "", nil)
	p := requireProblem(t, rec, TypeInternalError)
	if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "boom") || len(p.Errors) != 0 {
		t.Fatalf("panic detail leaked: %s", rec.Body.String())
	}
}

func TestCanceledRequestWritesNothing(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet, "/api/v2/probe/slow", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }()
		h.ServeHTTP(rec, r)
	}()
	cancel()
	<-done
	if rec.Body.Len() != 0 {
		t.Fatalf("body written after cancel: %s", rec.Body.String())
	}
}

func TestSuccessDefaultsNoStoreAndNoSchema(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodPost, "/api/v2/probe/public", `{"name":"x","cleared":null}`, nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if strings.Contains(rec.Body.String(), "$schema") {
		t.Fatal("$schema present")
	}
}

func TestPatchTransport(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	for body, want := range map[string]string{
		`{"name":"x","cleared":null}`:             `"note_set":false,"note_null":false,"note":""`,
		`{"name":"x","cleared":null,"note":null}`: `"note_set":true,"note_null":true,"note":""`,
		`{"name":"x","cleared":null,"note":"hi"}`: `"note_set":true,"note_null":false,"note":"hi"`,
	} {
		rec := do(t, h, http.MethodPost, "/api/v2/probe/public", body, nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s -> %d %s", body, rec.Code, rec.Body.String())
		}
	}
}

// --- Discovery --------------------------------------------------------------

func TestSystemInfo(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/system/info", "", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	var info SystemInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.APIMajor != 2 || info.ServerVersion == "" || len(info.ContractDigest) != 64 ||
		info.Links.OpenAPI != "/api/v2/openapi.json" || info.Links.Capabilities != "/api/v2/capabilities" {
		t.Fatalf("%+v", info)
	}
	if info.ContractDigest != ContractDigest() {
		t.Fatal("digest not from the embedded bytes")
	}
	if rec.Body.Len() > 1024 {
		t.Fatalf("discovery document is not bounded: %d bytes", rec.Body.Len())
	}
	// Reproducible: the same bytes, the same digest.
	if do(t, h, http.MethodGet, "/api/v2/system/info", "", nil).Body.String() != rec.Body.String() {
		t.Fatal("discovery document varies between requests")
	}
}

// --- Cursor and helpers -----------------------------------------------------

func TestCursors(t *testing.T) {
	c := NewCursors([]byte("k"))
	scope := CursorScope{OperationID: "listX", Security: "u1", Filter: "a=1", Sort: "name", Tiebreaker: "id"}
	type pos struct{ Name, ID string }
	cur, err := c.Encode(scope, pos{"n", "7"})
	if err != nil || strings.ContainsAny(cur, "+/=") {
		t.Fatalf("encode: %v %q", err, cur)
	}
	var got pos
	if p := c.Decode(scope, cur, &got); p != nil || got != (pos{"n", "7"}) {
		t.Fatalf("decode: %v %+v", p, got)
	}
	other := scope
	other.Security = "u2"
	for name, bad := range map[string]struct {
		s CursorScope
		c string
	}{
		"other scope": {other, cur},
		"tampered":    {scope, "x" + cur[1:]},
		"garbage":     {scope, "not-a-cursor"},
		"other key":   {scope, func() string { k, _ := NewCursors([]byte("k2")).Encode(scope, pos{}); return k }()},
	} {
		if p := c.Decode(bad.s, bad.c, &got); p == nil || p.Type != TypeInvalidCursor.URI() || p.Status != 400 {
			t.Errorf("%s: %+v", name, p)
		}
	}
}

func TestRequestIDNeverAdoptsClientValue(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	check := func(t *testing.T, rec *httptest.ResponseRecorder) string {
		t.Helper()
		id := requestIDHeader(rec)
		if id == "" || strings.Contains(id, "evil") || strings.Contains(rec.Body.String(), "evil") {
			t.Fatalf("client value adopted: header %q body %s", id, rec.Body.String())
		}
		var p problemDoc
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if p.Instance != "urn:silo:request:"+id {
			t.Fatalf("instance %q vs header %q", p.Instance, id)
		}
		return id
	}
	// Standalone: both header spellings a client might try.
	for _, name := range []string{"X-Request-ID", "X-Request-Id"} {
		rec := do(t, h, http.MethodGet, "/api/v2/nothing", "", map[string]string{name: "evil"})
		check(t, rec)
	}
	// Under the API listener's base chain, the server-generated ID from the
	// context is reused verbatim and the client value is still ignored.
	outer := chi.NewRouter()
	outer.Use(apimw.RequestID)
	var ctxID string
	outer.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctxID = chiRequestIDFrom(r)
			next.ServeHTTP(w, r)
		})
	})
	outer.Handle(DelegationPattern, h)
	r := httptest.NewRequest(http.MethodGet, "/api/v2/nothing", nil)
	r.Header.Set("X-Request-Id", "evil")
	rec := httptest.NewRecorder()
	outer.ServeHTTP(rec, r)
	if id := check(t, rec); id != ctxID {
		t.Fatalf("v2 header %q differs from the context id %q the v1 logs use", id, ctxID)
	}
}

// TestCompressedRequestBodyIsRejected: the media-type guard refuses a
// non-identity Content-Encoding before the body is read, so a gzip header over
// a plain body is a 415, not a 200 or a parse error.
func TestCompressedRequestBodyIsRejected(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	body := `{"name":"x","cleared":null}`
	for _, enc := range []string{"gzip", "br", "deflate", "GZIP", "gzip, identity"} {
		rec := do(t, h, http.MethodPost, "/api/v2/probe/public", body, map[string]string{"Content-Encoding": enc})
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Encoding %q: status %d, want 415: %s", enc, rec.Code, rec.Body.String())
			continue
		}
		requireProblem(t, rec, TypeUnsupportedMediaType)
	}
	// A real gzip body is refused for the same reason, before decoding.
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(body))
	_ = zw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/v2/probe/public", bytes.NewReader(gz.Bytes()))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	requireProblem(t, rec, TypeUnsupportedMediaType)
	// identity is the one encoding an unencoded body may declare.
	if rec := do(t, h, http.MethodPost, "/api/v2/probe/public", body, map[string]string{"Content-Encoding": "identity"}); rec.Code != 200 {
		t.Fatalf("identity: %d %s", rec.Code, rec.Body.String())
	}
}

// TestMediaTypeIsCanonicalizedBeforeHuma: the guard, not Huma's
// case-sensitive format table, is the single 415 authority, so a media type
// that differs only in case is accepted.
func TestMediaTypeCaseIsAcceptedByTheGuard(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	body := `{"name":"x","cleared":null}`
	for _, ct := range []string{"APPLICATION/JSON", "Application/Json", "APPLICATION/JSON; CHARSET=UTF-8"} {
		rec := do(t, h, http.MethodPost, "/api/v2/probe/public", body, map[string]string{"Content-Type": ct})
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type %q: %d %q %s", ct, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
	}
}

// TestMethodNotAllowedSendsAllow: RFC 9110 requires Allow on a 405, and the
// set comes from the registry's declared rows for the matched path.
func TestMethodNotAllowedSendsAllow(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	for _, method := range []string{http.MethodDelete, http.MethodHead} {
		rec := do(t, h, method, "/api/v2/system/info", "", nil)
		requireProblem(t, rec, TypeMethodNotAllowed)
		if got := rec.Header().Get("Allow"); got != "GET" {
			t.Errorf("%s /api/v2/system/info: Allow = %q, want %q", method, got, "GET")
		}
	}
	// A path parameter is matched, not compared literally.
	rec := do(t, h, http.MethodDelete, "/api/v2/probe/item/7", "", nil)
	requireProblem(t, rec, TypeMethodNotAllowed)
	if got := rec.Header().Get("Allow"); got != "GET" {
		t.Errorf("Allow = %q, want GET", got)
	}
}

// TestNamespaceRootIsNotAV2Path: /api/v2 (no slash) is outside the v2 surface
// and answered by the legacy listener; /api/v2/ is a v2 not_found problem.
func TestNamespaceRootIsNotAV2Path(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	rec := do(t, h, http.MethodGet, "/api/v2/", "", nil)
	requireProblem(t, rec, TypeNotFound)

	// Through the delegation the API listener registers, the same holds and
	// /api/v2 never reaches this package.
	outer := chi.NewRouter()
	outer.Use(apimw.RequestID)
	outer.Handle(DelegationPattern, h)
	outer.NotFound(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	r := httptest.NewRequest(http.MethodGet, "/api/v2", nil)
	rec = httptest.NewRecorder()
	outer.ServeHTTP(rec, r)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("/api/v2 reached the v2 listener: %d %s", rec.Code, rec.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v2/", nil)
	rec = httptest.NewRecorder()
	outer.ServeHTTP(rec, r)
	requireProblem(t, rec, TypeNotFound)
}

// TestOperationBodyLimitOverride: an operation's own cap gets the same
// off-by-one translation as the default, and the 413 names that cap.
func TestOperationBodyLimitOverride(t *testing.T) {
	h := newTestHandler(t, Dependencies{})
	pad := func(n int64) string {
		prefix := `{"name":"x","cleared":null,"note":"`
		suffix := `"}`
		return prefix + strings.Repeat("a", int(n)-len(prefix)-len(suffix)) + suffix
	}
	if rec := do(t, h, http.MethodPost, "/api/v2/probe/smallbody", pad(ProbeSmallBodyLimit), nil); rec.Code != 200 {
		t.Fatalf("exactly at the override: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(t, h, http.MethodPost, "/api/v2/probe/smallbody", pad(ProbeSmallBodyLimit+1), nil)
	p := requireProblem(t, rec, TypePayloadTooLarge)
	if !strings.Contains(p.Detail, strconv.FormatInt(ProbeSmallBodyLimit, 10)) {
		t.Fatalf("413 detail names the wrong limit: %q", p.Detail)
	}
	// The default limit still renders its own value.
	rec = do(t, h, http.MethodPost, "/api/v2/probe/public", pad(MaxJSONBodyBytes+1), nil)
	p = requireProblem(t, rec, TypePayloadTooLarge)
	if !strings.Contains(p.Detail, strconv.FormatInt(MaxJSONBodyBytes, 10)) {
		t.Fatalf("default 413 detail: %q", p.Detail)
	}
}

// TestTamperedCursorThroughRouter: a cursor the operation decodes fails with
// the invalid_cursor problem end to end, envelope and cache policy included.
func TestTamperedCursorThroughRouter(t *testing.T) {
	h := newTestHandler(t, Dependencies{CursorSecret: []byte("k")})
	good, err := NewCursors([]byte("k")).Encode(probeCursorScope, probeCursor{Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, h, http.MethodGet, "/api/v2/probe/list?cursor="+good, "", nil); rec.Code != 200 {
		t.Fatalf("valid cursor: %d %s", rec.Code, rec.Body.String())
	}
	for name, cursor := range map[string]string{
		"tampered": "x" + good[1:],
		"garbage":  "not-a-cursor",
		"foreign":  func() string { c, _ := NewCursors([]byte("other")).Encode(probeCursorScope, probeCursor{}); return c }(),
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, "/api/v2/probe/list?cursor="+cursor, "", nil)
			requireProblem(t, rec, TypeInvalidCursor)
		})
	}
}
