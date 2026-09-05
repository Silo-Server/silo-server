package apiv2

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

var (
	deprecatedAt     = time.Date(2026, time.September, 1, 12, 30, 45, 987654321, time.UTC)
	deprecatedSunset = time.Date(2027, time.March, 1, 0, 0, 0, 0, time.UTC)
	deprecatedLink   = DocsOrigin + "api/v2/migration/probe"
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
