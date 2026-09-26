package apiv2

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

// A transforming intermediary can weaken an origin's strong ETag even when
// the origin itself did not compress. Clients must receive a usable validator
// for their subsequent If-Match write, not silently discard the settings read.
func TestValidatorSurvivesTransformingProxy(t *testing.T) {
	origin := httptest.NewServer(httpstream.CompressWithExclusions(5, nil, IdentityEncoded)(
		NewHandler(Dependencies{testRegister: registerProbes})))
	t.Cleanup(origin.Close)
	target, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(resp *http.Response) error {
		// Model gzip filters that ignore Cache-Control but skip an already
		// declared content coding, as nginx/OpenResty does.
		if resp.Header.Get("ETag") == "" || resp.Header.Get("Content-Encoding") != "" || resp.StatusCode != http.StatusOK {
			return nil
		}
		var body bytes.Buffer
		writer := gzip.NewWriter(&body)
		_, copyErr := io.Copy(writer, resp.Body)
		_ = resp.Body.Close()
		if copyErr != nil {
			return copyErr
		}
		if err := writer.Close(); err != nil {
			return err
		}
		resp.ContentLength = int64(body.Len())
		resp.Header.Set("Content-Length", strconv.Itoa(body.Len()))
		resp.Header.Set("Content-Encoding", "gzip")
		resp.Header.Set("ETag", "W/"+resp.Header.Get("ETag"))
		resp.Body = io.NopCloser(&body)
		return nil
	}

	read := do(t, proxy, http.MethodGet, "/api/v2/probe/guarded/a", "", map[string]string{"Accept-Encoding": "gzip"})
	tag := read.Header().Get("ETag")
	if read.Code != http.StatusOK || tag != RenderETag(guardedProbeScope, "a", 1).String() {
		t.Fatalf("proxied read: status=%d etag=%q, want the strong origin validator", read.Code, tag)
	}
	if read.Header().Get("Content-Encoding") != "identity" || !strings.Contains(read.Body.String(), `"alpha"`) {
		t.Fatalf("proxied read lost its identity representation")
	}
	if !strings.Contains(read.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("validator protection replaced the original cache policy")
	}
	write := do(t, proxy, http.MethodPut, "/api/v2/probe/guarded/a", `{"name":"beta"}`, map[string]string{
		"Accept-Encoding": "gzip", "If-Match": tag,
	})
	if write.Code != http.StatusOK || write.Header().Get("ETag") != RenderETag(guardedProbeScope, "a", 2).String() {
		t.Fatalf("guarded write using the proxied validator: status=%d etag=%q", write.Code, write.Header().Get("ETag"))
	}
}
