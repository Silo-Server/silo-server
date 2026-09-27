package proxy

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

func TestDirectProtocolConditionalResponses(t *testing.T) {
	const secret = "synthetic-direct-protocol"
	server := httptest.NewServer(newSocketProxyServer(t, secret, clientip.NewResolver(nil)).Handler())
	defer server.Close()
	token := socketProxyMediaToken(t, secret, writeSocketProxyMedia(t))
	url := server.URL + "/stream/direct/" + token
	initial := socketProxyRequest(t, server.Client(), http.MethodHead, url, nil)
	etag := initial.header.Get("ETag")
	if initial.status != 200 || etag == "" || strings.HasPrefix(etag, "W/") || initial.body != "" {
		t.Fatal(initial.status, initial.header, initial.body)
	}
	for _, tc := range []struct {
		method  string
		headers map[string]string
		status  int
	}{
		{http.MethodGet, map[string]string{"Range": "bytes=1-2", "If-Range": etag}, 206},
		{http.MethodGet, map[string]string{"Range": "bytes=0-1,8-9"}, 206},
		{http.MethodGet, map[string]string{"If-None-Match": etag}, 304},
		{http.MethodGet, map[string]string{"If-Match": "\"mismatch\""}, 412},
		{http.MethodGet, map[string]string{"Range": "bytes=999-1000"}, 416},
		{http.MethodHead, map[string]string{"Range": "bytes=1-2"}, 206},
	} {
		response := socketProxyRequest(t, server.Client(), tc.method, url, tc.headers)
		if response.status != tc.status {
			t.Fatal(response.status, response.body)
		}
		for _, op := range ProtocolDirectPlayback() {
			if op.Method != tc.method {
				continue
			}
			declared := op.Responses[strconv.Itoa(response.status)]
			if declared == nil {
				t.Fatal("missing direct status", tc.status)
			}
			if tc.method == http.MethodHead {
				if len(declared.Content) != 0 || response.body != "" {
					t.Fatal("HEAD advertised or sent body")
				}
				continue
			}
			if response.body != "" {
				media, _, _ := strings.Cut(response.header.Get("Content-Type"), ";")
				if declared.Content[media] == nil {
					t.Fatal("missing direct media", media)
				}
			}
		}
	}
}
