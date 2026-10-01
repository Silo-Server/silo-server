package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestDownloadProtocolRemoteStatusAndHead(t *testing.T) {
	const secret = "synthetic-download-protocol"
	for _, tc := range []struct {
		method         string
		origin, status int
	}{
		{http.MethodGet, 299, 299}, {http.MethodGet, 304, 304},
		{http.MethodGet, 412, 412}, {http.MethodGet, 416, 416},
		{http.MethodGet, 500, 502}, {http.MethodGet, 404, 404},
		{http.MethodHead, 206, 206},
	} {
		t.Run(tc.method+strconv.Itoa(tc.origin), func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method || r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("If-Match") != "fixture" {
					t.Error("origin request changed")
				}
				w.Header().Set("Content-Type", "application/x-worker-fixture")
				w.Header().Set("X-Silo-Transcode-Segment-Generation", "private-generation")
				w.Header().Set("Set-Cookie", "private=fixture")
				w.WriteHeader(tc.origin)
				if tc.origin != 304 {
					_, _ = io.WriteString(w, "opaque")
				}
			}))
			defer origin.Close()
			token, err := streamtoken.Sign(streamtoken.Claims{SessionID: "download-fixture", PlayMethod: streamtoken.PlayMethodDownload, TranscodeNode: origin.URL, DownloadArtifactID: "fixture"}, secret, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(newDownloadProxyServer(t, secret).Handler())
			defer server.Close()
			req, err := http.NewRequestWithContext(t.Context(), tc.method, server.URL+"/downloads/file/"+token, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("If-Match", "fixture")
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
			if response.StatusCode != tc.status {
				t.Fatal(response.StatusCode, string(body))
			}
			if response.Header.Get("Set-Cookie") != "" || response.Header.Get("X-Silo-Transcode-Segment-Generation") != "" {
				t.Fatal("private origin headers leaked")
			}
			if tc.method == http.MethodHead && len(body) != 0 {
				t.Fatal("HEAD returned bytes")
			}
			if tc.origin == 299 && (string(body) != "opaque" || response.Header.Get("Content-Type") != "application/x-worker-fixture") {
				t.Fatal("opaque origin response changed")
			}
			for _, op := range ProtocolDownloads() {
				if op.Method != tc.method {
					continue
				}
				declared := op.Responses[strconv.Itoa(tc.status)]
				if declared == nil && tc.status >= 200 && tc.status < 300 {
					declared = op.Responses["2XX"]
				}
				if declared == nil {
					t.Fatal("undeclared status", tc.status)
				}
				if tc.method == http.MethodHead && len(declared.Content) != 0 {
					t.Fatal("HEAD description has body")
				}
			}
		})
	}
}
