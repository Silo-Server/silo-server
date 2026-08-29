package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

const nativeSocketMediaETag = `"native-socket-media-v1"`

type socketActivityWriter struct{}

func (socketActivityWriter) Write(activitylog.LogEntry) {}
func (socketActivityWriter) Close() error               { return nil }

type socketRoutes struct {
	readerFromSeen atomic.Bool
	telemetry      *streamtelemetry.Registry
}

func (h *socketRoutes) Mount(r chi.Router) {
	media := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	serveMedia := func(w http.ResponseWriter, req *http.Request) {
		streamtelemetry.Attach(req.Context(), streamtelemetry.Attachment{Subject: streamtelemetry.UserSubject(7),
			ProfileID: "socket-profile", SessionID: "socket-session", MediaFileID: 42,
			PlayMethod: "direct", StartedAt: time.Unix(1_700_000_000, 0), StartedAtSource: streamtelemetry.StartedAtSourceSession})
		_, ok := w.(io.ReaderFrom)
		h.readerFromSeen.Store(ok)
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("ETag", nativeSocketMediaETag)
		http.ServeContent(w, req, "movie.mp4", time.Unix(1_700_000_000, 0), bytes.NewReader(media))
	}
	wrap := func(method, pattern string, handler http.HandlerFunc) http.HandlerFunc {
		if h.telemetry == nil {
			return handler
		}
		route := streamtelemetry.MediaRoute{Family: streamtelemetry.FamilyNative, Method: method, Pattern: pattern,
			Class: streamtelemetry.ClassPlayback, Role: streamtelemetry.RoleViewerEgress, CapRelevant: true, Enrolled: true,
			Capture: func(r *http.Request) streamtelemetry.CaptureSet {
				return streamtelemetry.CaptureSet{Method: r.Method, Pattern: pattern, ReceivedAt: time.Now()}
			}}
		return h.telemetry.Observe(route)(handler).ServeHTTP
	}
	r.Get("/api/v1/stream/socket-test", wrap(http.MethodGet, "/api/v1/stream/socket-test", serveMedia))
	r.Head("/api/v1/stream/socket-test", wrap(http.MethodHead, "/api/v1/stream/socket-test", serveMedia))
	r.Get("/api/v1/stream/socket-test/subtitles/1/fonts", wrap(http.MethodGet, "/api/v1/stream/socket-test/subtitles/1/fonts", func(w http.ResponseWriter, req *http.Request) {
		streamtelemetry.Attach(req.Context(), streamtelemetry.Attachment{Subject: streamtelemetry.UserSubject(7),
			ProfileID: "socket-profile", SessionID: "socket-session", MediaFileID: 42,
			PlayMethod: "direct", StartedAt: time.Unix(1_700_000_000, 0), StartedAtSource: streamtelemetry.StartedAtSourceSession})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"payload": strings.Repeat("compressible-json-", 128)})
	}))
}

func TestMountedNativeRouterPreservesMediaHTTPAndCompression(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	telemetryConfig := streamtelemetry.DefaultConfig("socket-test")
	telemetryConfig.Enabled = true
	routes := &socketRoutes{telemetry: streamtelemetry.NewRegistry(telemetryConfig, streamtelemetry.NewLocalStore(), nil)}
	// Drive the real middleware chain (useBaseMiddleware is what NewRouter
	// itself mounts) rather than NewRouter's full route tree: the media routes
	// are only registered when their handler dependencies are non-nil, which a
	// unit test cannot supply. Mounting the shared chain keeps this test honest
	// about ordering while still exercising stub media routes end to end.
	root := chi.NewRouter()
	useBaseMiddleware(root, Dependencies{
		Config:            cfg,
		ActivityLogWriter: socketActivityWriter{},
	})
	routes.Mount(root)
	server := httptest.NewUnstartedServer(root)
	server.Start()
	t.Cleanup(server.Close)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)

	mediaURL := server.URL + "/api/v1/stream/socket-test"
	assertNativeSocketResponse(t, client, http.MethodGet, mediaURL, nil, http.StatusOK, "0123456789abcdefghijklmnopqrstuvwxyz")
	assertNativeSocketResponse(t, client, http.MethodHead, mediaURL, nil, http.StatusOK, "")
	assertNativeSocketResponse(t, client, http.MethodGet, mediaURL, map[string]string{"Range": "bytes=2-5"}, http.StatusPartialContent, "2345")
	assertNativeMultiRange(t, client, mediaURL)
	assertNativeSocketResponse(t, client, http.MethodGet, mediaURL, map[string]string{"If-None-Match": nativeSocketMediaETag}, http.StatusNotModified, "")
	assertNativeSocketResponse(t, client, http.MethodGet, mediaURL, map[string]string{"Range": "bytes=2-5", "If-Range": nativeSocketMediaETag}, http.StatusPartialContent, "2345")
	assertNativeSocketResponse(t, client, http.MethodGet, mediaURL, map[string]string{"Range": "bytes=2-5", "If-Range": `"stale"`}, http.StatusOK, "0123456789abcdefghijklmnopqrstuvwxyz")

	resp := nativeSocketRequest(t, client, http.MethodGet, mediaURL, map[string]string{"Accept-Encoding": "gzip"})
	body := readNativeSocketBody(t, resp)
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" {
		t.Fatalf("media Content-Encoding = %q, want empty", encoding)
	}
	if string(body) != "0123456789abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("media body = %q", body)
	}
	if !routes.readerFromSeen.Load() {
		t.Fatal("bypassed media handler ResponseWriter does not implement io.ReaderFrom through mounted middleware")
	}

	jsonURL := server.URL + "/api/v1/stream/socket-test/subtitles/1/fonts"
	resp = nativeSocketRequest(t, client, http.MethodGet, jsonURL, map[string]string{"Accept-Encoding": "gzip"})
	defer func() { _ = resp.Body.Close() }()
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "gzip" {
		t.Fatalf("JSON Content-Encoding = %q, want gzip", encoding)
	}
	if vary := resp.Header.Values("Vary"); !headerValuesContain(vary, "Accept-Encoding") {
		t.Fatalf("JSON Vary = %q, want Accept-Encoding", vary)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer func() { _ = zr.Close() }()
	if body, err := io.ReadAll(zr); err != nil || !bytes.Contains(body, []byte("compressible-json")) {
		t.Fatalf("compressed JSON body invalid: body=%q err=%v", body, err)
	}
	snapshot := routes.telemetry.Sweep()
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].RequestCount != 9 || snapshot.Sessions[0].BytesAccepted == 0 {
		t.Fatalf("mounted telemetry snapshot = %+v", snapshot)
	}
}

func assertNativeSocketResponse(t *testing.T, client *http.Client, method, url string, headers map[string]string, wantStatus int, wantBody string) {
	t.Helper()
	resp := nativeSocketRequest(t, client, method, url, headers)
	body := readNativeSocketBody(t, resp)
	_ = resp.Body.Close()
	if resp.StatusCode != wantStatus || string(body) != wantBody {
		t.Fatalf("%s status/body = %d, %q; want %d, %q", method, resp.StatusCode, body, wantStatus, wantBody)
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" {
		t.Fatalf("%s Content-Encoding = %q, want empty", method, encoding)
	}
}

func assertNativeMultiRange(t *testing.T, client *http.Client, url string) {
	t.Helper()
	resp := nativeSocketRequest(t, client, http.MethodGet, url, map[string]string{"Range": "bytes=0-1,4-6"})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("multi-range status = %d, want 206", resp.StatusCode)
	}
	mediaType, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/byteranges" {
		t.Fatalf("multi-range Content-Type = %q: %v", resp.Header.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(resp.Body, params["boundary"])
	var bodies []string
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read multipart range: %v", err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read multipart body: %v", err)
		}
		bodies = append(bodies, string(body))
	}
	if strings.Join(bodies, ",") != "01,456" {
		t.Fatalf("multi-range bodies = %q, want [01 456]", bodies)
	}
}

func nativeSocketRequest(t *testing.T, client *http.Client, method, url string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, url, err)
	}
	return resp
}

func readNativeSocketBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return body
}

func headerValuesContain(values []string, want string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), want) {
				return true
			}
		}
	}
	return false
}
