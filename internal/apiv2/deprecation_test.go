package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

var (
	deprecatedAt   = probeDeprecatedAt.Add(987654321 * time.Nanosecond)
	deprecatedLink = probeDeprecatedLink
)

func TestFormatDeprecationIsWholeUnixSeconds(t *testing.T) {
	if got := FormatDeprecation(deprecatedAt); got != "@1788265845" {
		t.Fatalf("FormatDeprecation = %q", got)
	}
	// The instant's zone never leaks into the value.
	local := deprecatedAt.In(time.FixedZone("plus2", 2*3600))
	if got := FormatDeprecation(local); got != "@1788265845" {
		t.Fatalf("FormatDeprecation(local) = %q", got)
	}
	if strings.ContainsAny(FormatDeprecation(deprecatedAt), `".`) {
		t.Fatal("the structured Date form has no quotes and no fraction")
	}
}

func TestFormatSunsetIsIMFFixdate(t *testing.T) {
	rfc := time.Date(1994, time.November, 6, 8, 49, 37, 0, time.UTC)
	if got := FormatSunset(rfc); got != "Sun, 06 Nov 1994 08:49:37 GMT" {
		t.Fatalf("FormatSunset = %q", got)
	}
	local := rfc.In(time.FixedZone("plus2", 2*3600))
	if got := FormatSunset(local); got != "Sun, 06 Nov 1994 08:49:37 GMT" {
		t.Fatalf("FormatSunset(local) = %q; the HTTP-date form is always GMT", got)
	}
}

func TestAppendLinkKeepsExistingValue(t *testing.T) {
	want := `<` + deprecatedLink + `>; rel="deprecation"`
	if got := appendLink("", deprecatedLink, "deprecation"); got != want {
		t.Fatalf("appendLink(empty) = %q", got)
	}
	prior := `<https://example.test/next>; rel="next"`
	if got := appendLink(prior, deprecatedLink, "deprecation"); got != prior+", "+want {
		t.Fatalf("appendLink(prior) = %q", got)
	}
}

func TestSetDeprecationHeadersAppendsToLink(t *testing.T) {
	h := http.Header{}
	h.Set(LinkHeader, `<https://example.test/a>; rel="next"`)
	h.Add(LinkHeader, `<https://example.test/b>; rel="prev"`)
	setDeprecationHeaders(h, &Deprecation{At: deprecatedAt, Link: deprecatedLink})
	got := h.Get(LinkHeader)
	for _, part := range []string{`rel="next"`, `rel="prev"`, `<` + deprecatedLink + `>; rel="deprecation"`} {
		if !strings.Contains(got, part) {
			t.Fatalf("Link %q lacks %s", got, part)
		}
	}
	if len(h.Values(LinkHeader)) != 1 {
		t.Fatalf("Link folded into one field, got %v", h.Values(LinkHeader))
	}
	if h.Get(SunsetHeader) != "" {
		t.Fatal("Sunset is sent only when a removal is planned")
	}
}

func TestRegisterRefusesBadDeprecations(t *testing.T) {
	before := deprecatedAt.Add(-time.Second)
	cases := map[string]*Deprecation{
		"zero at":        {Link: deprecatedLink},
		"empty link":     {At: deprecatedAt},
		"http link":      {At: deprecatedAt, Link: "http://siloserver.org/docs/api/v2/migration/probe"},
		"foreign origin": {At: deprecatedAt, Link: "https://example.test/docs/migration"},
		"sunset first":   {At: deprecatedAt, Link: deprecatedLink, Sunset: &before},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil || !strings.Contains(r.(string), "deprecation") {
					t.Fatalf("expected a deprecation panic, got %v", r)
				}
			}()
			newChiRouter(Dependencies{testRegister: func(reg *Registry) {
				Register(reg, Operation{
					Operation:   humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""),
					Class:       ClassPublic,
					Deprecation: d,
				}, func(context.Context, *struct{}) (*probeOutput, error) { return nil, nil })
			}})
		})
	}
	t.Run("huma flag without a declaration", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil || !strings.Contains(r.(string), "Deprecation") {
				t.Fatalf("expected a panic naming the declaration, got %v", r)
			}
		}()
		newChiRouter(Dependencies{testRegister: func(reg *Registry) {
			o := humaOp(http.MethodGet, Prefix+"/x", "getX", "x", "")
			o.Deprecated = true
			Register(reg, Operation{Operation: o, Class: ClassPublic},
				func(context.Context, *struct{}) (*probeOutput, error) { return nil, nil })
		}})
	})
	t.Run("sunset equal to at is allowed", func(t *testing.T) {
		at := deprecatedAt
		newChiRouter(Dependencies{testRegister: func(reg *Registry) {
			Register(reg, Operation{
				Operation:   humaOp(http.MethodGet, Prefix+"/x", "getX", "x", ""),
				Class:       ClassPublic,
				Deprecation: &Deprecation{At: at, Link: deprecatedLink, Sunset: &at},
			}, func(context.Context, *struct{}) (*probeOutput, error) { return nil, nil })
		}})
	})
}

// wantDeprecated asserts the three headers of the deprecated probes; sunset
// says whether the operation declared one.
func wantDeprecated(t *testing.T, h http.Header, sunset bool) {
	t.Helper()
	if got := h.Get(DeprecationHeader); got != "@1788265845" {
		t.Fatalf("Deprecation = %q", got)
	}
	if got := h.Get(LinkHeader); !strings.Contains(got, `<`+probeDeprecatedLink+`>; rel="deprecation"`) {
		t.Fatalf("Link = %q", got)
	}
	got := h.Get(SunsetHeader)
	switch {
	case sunset && got != "Mon, 01 Mar 2027 00:00:00 GMT":
		t.Fatalf("Sunset = %q", got)
	case !sunset && got != "":
		t.Fatalf("Sunset = %q on an operation with no planned removal", got)
	}
}

func TestDeprecatedOperationHeaders(t *testing.T) {
	h := newTestHandler(t, Dependencies{Auth: fakeAuth(nil)})
	t.Run("200", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/api/v2/probe/deprecated", "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		wantDeprecated(t, rec.Header(), true)
	})
	t.Run("401 from the auth gate", func(t *testing.T) {
		rec := do(t, h, http.MethodPost, "/api/v2/probe/deprecated-nosunset", `{"name":"x","cleared":null}`, nil)
		requireProblem(t, rec, TypeAuthenticationRequired)
		wantDeprecated(t, rec.Header(), false)
	})
	t.Run("422 from the query guard", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/api/v2/probe/deprecated?verbose=1", "", nil)
		requireProblem(t, rec, TypeValidationFailed)
		wantDeprecated(t, rec.Header(), true)
	})
	t.Run("not deprecated", func(t *testing.T) {
		rec := do(t, h, http.MethodGet, "/api/v2/system/info", "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		for _, name := range []string{DeprecationHeader, LinkHeader, SunsetHeader} {
			if v := rec.Header().Get(name); v != "" {
				t.Fatalf("%s = %q on a live operation", name, v)
			}
		}
	})
}

// A Link value set by an earlier handler in the chain survives: the
// deprecation link is appended to it.
func TestDeprecatedOperationAppendsToExistingLink(t *testing.T) {
	prior := `<https://siloserver.org/docs/api/v2/>; rel="service-doc"`
	inner := newTestHandler(t, Dependencies{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(LinkHeader, prior)
		inner.ServeHTTP(w, r)
	})
	rec := do(t, h, http.MethodGet, "/api/v2/probe/deprecated", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got := rec.Header().Values(LinkHeader)
	if len(got) != 1 || !strings.HasPrefix(got[0], prior+", <"+probeDeprecatedLink+">") {
		t.Fatalf("Link = %q, want the prior value first and the deprecation link appended", got)
	}
}

// TestDeprecatedOperationsAreDocumented generates the document from a
// registry that carries the deprecated probes and checks what a client
// generator reads: deprecated: true plus x-silo-deprecation with RFC 3339 UTC
// at/link and a sunset only where declared, and neither on a plain operation.
func TestDeprecatedOperationsAreDocumented(t *testing.T) {
	api := huma.NewAPI(humaConfig(), noopAdapter{})
	reg := &Registry{api: api}
	registerProbes(reg)
	raw, err := api.OpenAPI().MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	op := func(method, path string) map[string]any {
		o := doc.Paths[Prefix+path][method]
		if o == nil {
			t.Fatalf("%s %s is not documented", method, path)
		}
		return o
	}
	withSunset := op("get", "/probe/deprecated")
	if withSunset["deprecated"] != true {
		t.Fatalf("deprecated probe lacks deprecated: true: %v", withSunset["deprecated"])
	}
	ext, _ := withSunset[extDeprecation].(map[string]any)
	if ext["at"] != probeDeprecatedAt.UTC().Format(time.RFC3339) || ext["link"] != probeDeprecatedLink || ext["sunset"] != probeDeprecatedSunset.UTC().Format(time.RFC3339) {
		t.Fatalf("x-silo-deprecation = %v", ext)
	}
	noSunset := op("post", "/probe/deprecated-nosunset")
	ext, _ = noSunset[extDeprecation].(map[string]any)
	if noSunset["deprecated"] != true || ext["at"] == nil || ext["link"] == nil {
		t.Fatalf("no-sunset probe: deprecated=%v ext=%v", noSunset["deprecated"], ext)
	}
	if _, has := ext["sunset"]; has {
		t.Fatalf("no-sunset probe documents a sunset: %v", ext)
	}
	plain := op("get", "/probe/zero")
	if plain["deprecated"] == true || plain[extDeprecation] != nil {
		t.Fatalf("plain probe carries deprecation metadata: deprecated=%v ext=%v", plain["deprecated"], plain[extDeprecation])
	}
}
