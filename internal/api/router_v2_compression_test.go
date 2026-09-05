package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/apiv2"
	"github.com/Silo-Server/silo-server/internal/config"
)

// The v2 subtree runs under the native listener's compression middleware. A
// v2 response carrying an ETag must reach the wire identity-encoded so its
// strong validator names one representation (RFC 9110 8.8.1); a v2 JSON
// response without a validator still compresses. Driven through the real
// chain (useBaseMiddleware plus the wildcard delegation NewRouter registers)
// over a real socket, against production public operations; the guarded
// round trip (tag received under Accept-Encoding: gzip admits the If-Match
// write) is pinned in internal/apiv2/encoding_test.go over the same pairing.
func TestMountedV2ValidatorBearingResponsesAreIdentityEncoded(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	root := chi.NewRouter()
	useBaseMiddleware(root, Dependencies{Config: cfg})
	root.Handle(apiv2.DelegationPattern, apiv2.NewHandler(apiv2.Dependencies{}))
	server := httptest.NewServer(root)
	t.Cleanup(server.Close)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)
	gz := map[string]string{"Accept-Encoding": "gzip"}

	resp := nativeSocketRequest(t, client, http.MethodGet, server.URL+"/api/v2/openapi.json", gz)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read openapi.json body: %v", err)
	}
	etag := resp.Header.Get("ETag")
	if resp.StatusCode != http.StatusOK || etag == "" || strings.HasPrefix(etag, "W/") {
		t.Fatalf("openapi.json: status %d ETag %q, want 200 with a strong tag", resp.StatusCode, etag)
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "" {
		t.Fatalf("validator-bearing v2 Content-Encoding = %q, want identity", enc)
	}
	if headerValuesContain(resp.Header.Values("Vary"), "Accept-Encoding") {
		t.Fatalf("validator-bearing v2 Vary = %q, want no Accept-Encoding", resp.Header.Values("Vary"))
	}
	if !strings.HasPrefix(strings.TrimSpace(string(body)), "{") || !strings.Contains(string(body), `"openapi"`) {
		t.Fatalf("openapi.json body is not identity JSON: %.60q", body)
	}

	resp = nativeSocketRequest(t, client, http.MethodGet, server.URL+"/api/v2/system/info", gz)
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("system/info: status %d", resp.StatusCode)
	}
	if resp.Header.Get("ETag") != "" {
		t.Fatalf("system/info unexpectedly carries ETag %q; pick another unvalidated operation", resp.Header.Get("ETag"))
	}
	if enc := resp.Header.Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("unvalidated v2 JSON Content-Encoding = %q, want gzip", enc)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	unzipped, err := io.ReadAll(zr)
	_ = resp.Body.Close()
	if err != nil || !strings.Contains(string(unzipped), `"api_major"`) && !strings.Contains(string(unzipped), `"version"`) {
		t.Fatalf("compressed system/info body invalid: body=%.80q err=%v", unzipped, err)
	}
}
