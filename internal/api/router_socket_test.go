package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

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
	// The stub route sets no header of its own: the chain must, or a browser
	// runs user-supplied bytes from an API route as a same-origin script.
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("%s X-Content-Type-Options = %q, want nosniff", method, got)
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

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A stream that commits its response and then breaks it with
// panic(http.ErrAbortHandler) is still logged and counted once, marked
// aborted, and the panic still reaches net/http so the client sees a broken
// body instead of a clean end.
func TestBaseMiddlewareRecordsAbortedStream(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	logs := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const route = "/api/v1/test-aborted-stream/{id}"
	root := chi.NewRouter()
	useBaseMiddleware(root, Dependencies{Config: cfg})
	root.Get(route, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("partial"))
		http.NewResponseController(w).Flush() //nolint:errcheck // the abort below is the point
		panic(http.ErrAbortHandler)
	})
	before := legacyRequestCount(t, route, "aborted")
	server := httptest.NewServer(root)
	resp, err := server.Client().Get(server.URL + "/api/v1/test-aborted-stream/1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	server.Close() // waits for the handler goroutine to finish unwinding
	if resp.StatusCode != http.StatusOK || string(body) != "partial" || !errors.Is(readErr, io.ErrUnexpectedEOF) {
		t.Fatalf("client got %d %q, read error %v; want 200, the partial body and a broken connection", resp.StatusCode, body, readErr)
	}

	if got := legacyRequestCount(t, route, "aborted") - before; got != 1 {
		t.Fatalf("aborted request counted %v times, want 1", got)
	}
	var logged int
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil || rec["msg"] != "api request" || rec["path_pattern"] != route {
			continue
		}
		logged++
		if rec["outcome"] != "aborted" || rec["status"] != float64(http.StatusOK) {
			t.Fatalf("request log line = %s, want outcome aborted and status 200", line)
		}
	}
	if logged != 1 {
		t.Fatalf("request logged %d times, want 1; log:\n%s", logged, logs.String())
	}
}

// legacyRequestCount reads the native request counter from the default
// registry; the middleware package keeps the vector unexported.
func legacyRequestCount(t *testing.T, path, status string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() != "streamapp_http_requests_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, pair := range metric.GetLabel() {
				labels[pair.GetName()] = pair.GetValue()
			}
			if labels["method"] == http.MethodGet && labels["path"] == path && labels["status"] == status {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}
