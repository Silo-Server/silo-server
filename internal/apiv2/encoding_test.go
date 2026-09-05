package apiv2

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

// The API listener compresses JSON by Accept-Encoding; a v2 response that
// carries an ETag must reach the wire identity-encoded so the strong
// validator names one representation, and the tag a client received over a
// gzip-accepting request must still admit its guarded write.
func TestValidatorBearingResponsesStayIdentityEncodedUnderCompression(t *testing.T) {
	// registerProbes includes the guarded probe over a fresh store ("a" at
	// version 1) and the unvalidated list probe.
	h := httpstream.CompressWithExclusions(5, nil, IdentityEncoded)(
		NewHandler(Dependencies{testRegister: registerProbes}))
	gz := map[string]string{"Accept-Encoding": "gzip"}

	rec := do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", gz)
	received := rec.Header().Get("ETag")
	if rec.Code != http.StatusOK || received != RenderETag(guardedProbeScope, "a", 1).String() {
		t.Fatalf("conditional read: %d etag %q body %s", rec.Code, received, rec.Body.String())
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("validator-bearing read Content-Encoding = %q, want identity", enc)
	}
	if vary := rec.Header().Values("Vary"); len(vary) != 0 {
		t.Fatalf("validator-bearing read Vary = %q, want none", vary)
	}
	if !strings.Contains(rec.Body.String(), `"alpha"`) {
		t.Fatalf("read body is not the identity JSON: %q", rec.Body.String())
	}

	rec = do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": received})
	if rec.Code != http.StatusNotModified || rec.Header().Get("ETag") != received || rec.Header().Get("Content-Encoding") != "" || rec.Body.Len() != 0 {
		t.Fatalf("If-None-Match with the received tag: %d etag %q enc %q body %q", rec.Code, rec.Header().Get("ETag"), rec.Header().Get("Content-Encoding"), rec.Body.String())
	}

	rec = do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"Accept-Encoding": "gzip", "If-Match": received})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != RenderETag(guardedProbeScope, "a", 2).String() {
		t.Fatalf("guarded write with the received tag: %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("guarded write Content-Encoding = %q, want identity", enc)
	}
	if !strings.Contains(rec.Body.String(), `"beta"`) {
		t.Fatalf("write body is not the identity JSON: %q", rec.Body.String())
	}

	// A v2 JSON response without a validator still compresses: the
	// exclusion is per response, not per subtree.
	rec = do(t, h, http.MethodGet, "/api/v2/probe/list", "", gz)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("unvalidated list: %d enc %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil || !strings.Contains(string(body), `"items"`) {
		t.Fatalf("compressed list body invalid: body=%q err=%v", body, err)
	}
}

func TestIdentityEncoded(t *testing.T) {
	withETag := http.Header{}
	withETag.Set("ETag", `"v"`)
	for _, tt := range []struct {
		path string
		h    http.Header
		want bool
	}{
		{"/api/v2/probe/guarded/a", withETag, true},
		{"/api/v2/openapi.json", withETag, true},
		{"/api/v2/probe/list", http.Header{}, false},
		{"/api/v1/things/1", withETag, false},
		{"/api/v2x/things/1", withETag, false},
	} {
		if got := IdentityEncoded(httptest.NewRequest(http.MethodGet, tt.path, nil), tt.h); got != tt.want {
			t.Errorf("IdentityEncoded(%s, ETag=%v) = %v, want %v", tt.path, len(tt.h) > 0, got, tt.want)
		}
	}
}

// A client that forbids identity cannot be given the identity body a
// validator-bearing response requires (RFC 9110 12.5.3), so the guard answers
// 406 before the operation runs; a client that merely prefers gzip receives
// the identity body with the same tag it would get without compression.
func TestValidatorBearingResponsesRefuseAcceptEncodingThatForbidsIdentity(t *testing.T) {
	h := httpstream.CompressWithExclusions(5, nil, IdentityEncoded)(
		NewHandler(Dependencies{testRegister: registerProbes}))
	forbid := map[string]string{"Accept-Encoding": "gzip, identity;q=0"}

	for _, path := range []string{"/api/v2/probe/guarded/a", "/api/v2/openapi.json"} {
		rec := do(t, h, http.MethodGet, path, "", forbid)
		requireProblem(t, rec, TypeNotAcceptable)
		if rec.Header().Get("ETag") != "" || rec.Header().Get("Content-Encoding") != "" {
			t.Fatalf("%s: 406 carries ETag %q Content-Encoding %q", path, rec.Header().Get("ETag"), rec.Header().Get("Content-Encoding"))
		}
	}
	rec := do(t, h, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{"Accept-Encoding": "gzip, identity;q=0", "If-Match": RenderETag(guardedProbeScope, "a", 1).String()})
	requireProblem(t, rec, TypeNotAcceptable)
	if got := do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", nil); got.Header().Get("ETag") != RenderETag(guardedProbeScope, "a", 1).String() {
		t.Fatalf("refused guarded write still changed the resource: etag %q", got.Header().Get("ETag"))
	}

	// A plain gzip preference is served identity with the tag unchanged.
	plain := do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", nil)
	gz := do(t, h, http.MethodGet, "/api/v2/probe/guarded/a", "", map[string]string{"Accept-Encoding": "gzip"})
	if gz.Code != http.StatusOK || gz.Header().Get("Content-Encoding") != "" || gz.Header().Get("ETag") != plain.Header().Get("ETag") || gz.Header().Get("ETag") == "" {
		t.Fatalf("gzip preference: %d enc %q etag %q (identity etag %q)", gz.Code, gz.Header().Get("Content-Encoding"), gz.Header().Get("ETag"), plain.Header().Get("ETag"))
	}
	doc := do(t, h, http.MethodGet, "/api/v2/openapi.json", "", map[string]string{"Accept-Encoding": "gzip"})
	if doc.Code != http.StatusOK || doc.Header().Get("Content-Encoding") != "" || doc.Header().Get("ETag") != `"`+contractDigest+`"` {
		t.Fatalf("openapi.json under gzip: %d enc %q etag %q", doc.Code, doc.Header().Get("Content-Encoding"), doc.Header().Get("ETag"))
	}

	// An operation without a validator is not identity-only: the client's
	// exclusion of identity is honored by compressing.
	rec = do(t, h, http.MethodGet, "/api/v2/probe/list", "", forbid)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("unvalidated list under identity;q=0: %d enc %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
}

func TestIdentityForbidden(t *testing.T) {
	for _, tt := range []struct {
		accept string
		want   bool
	}{
		{"", false},
		{"gzip", false},
		{"gzip, deflate, br", false},
		{"identity", false},
		{"*", false},
		{"gzip, identity;q=0", true},
		{"identity;q=0", true},
		{"identity; q=0.0", true},
		{"identity;Q=0", true},
		{"gzip;q=1.0, identity; q=0.5", false},
		{"*;q=0", true},
		{"gzip, *;q=0", true},
		{"gzip;q=1, *;q=0, identity", false},
		{"identity;q=0.001, *;q=0", false},
		{"identity;q=0, *", true},
		{"IDENTITY;q=0", true},
		{"gzip;q=0", false},
		{"identity;q=abc", false},
	} {
		if got := identityForbidden(tt.accept); got != tt.want {
			t.Errorf("identityForbidden(%q) = %v, want %v", tt.accept, got, tt.want)
		}
	}
	// The guard joins repeated field lines before parsing.
	if !identityForbidden(strings.Join([]string{"gzip", "identity;q=0"}, ",")) {
		t.Error("joined field lines: identity;q=0 not honored")
	}
}
