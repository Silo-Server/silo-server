package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

type recordingArtifactMissReporter struct {
	artifactID       string
	originNodeURL    string
	originArtifactID string
}

func (r *recordingArtifactMissReporter) ReportRemoteArtifactMissing(_ context.Context, artifactID, originNodeURL, originArtifactID string) error {
	r.artifactID = artifactID
	r.originNodeURL = originNodeURL
	r.originArtifactID = originArtifactID
	return nil
}

func newDownloadProxyServer(t *testing.T, secret string) *Server {
	t.Helper()
	w := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = secret
	w.SetConfigForTest(cfg)
	return NewServer(w, nil)
}

func TestProxyDownloadServesAuthorizedRange(t *testing.T) {
	const secret = "download-proxy-secret"
	dir := t.TempDir()
	path := filepath.Join(dir, "prepared movie.mp4")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:   "download-1",
		MediaPath:   path,
		PlayMethod:  streamtoken.PlayMethodDownload,
		UserID:      7,
		ProfileID:   "profile-1",
		MediaFileID: 42,
	}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/downloads/file/"+token, nil)
	req.Header.Set("Range", "bytes=2-5")
	rr := httptest.NewRecorder()
	newDownloadProxyServer(t, secret).Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "2345" {
		t.Fatalf("body = %q, want %q", rr.Body.String(), "2345")
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, "prepared movie.mp4") {
		t.Fatalf("Content-Disposition = %q", got)
	}
}

func TestProxyDownloadRelaysNodeLocalArtifactRange(t *testing.T) {
	const secret = "download-proxy-secret"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/downloads/artifacts/artifact-1" {
			t.Errorf("origin path = %q", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer "+secret {
			t.Errorf("origin Authorization = %q", auth)
			http.Error(w, "unexpected authorization", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("Range"); got != "bytes=2-5" {
			t.Errorf("origin Range = %q", got)
			http.Error(w, "unexpected range", http.StatusBadRequest)
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Disposition", `attachment; filename="artifact-1.mp4"`)
		w.Header().Set("Content-Length", "4")
		w.Header().Set("Content-Range", "bytes 2-5/10")
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "2345")
	}))
	defer origin.Close()
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:          "download-remote-1",
		PlayMethod:         streamtoken.PlayMethodDownload,
		TranscodeNode:      origin.URL,
		DownloadArtifactID: "artifact-1",
		DownloadFilename:   "Movie Final.mp4",
		UserID:             7,
	}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/downloads/file/"+token, nil)
	req.Header.Set("Range", "bytes=2-5")
	rr := httptest.NewRecorder()
	newDownloadProxyServer(t, secret).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusPartialContent || rr.Body.String() != "2345" {
		t.Fatalf("status = %d body = %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Range"); got != "bytes 2-5/10" {
		t.Fatalf("Content-Range = %q", got)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, "Movie Final.mp4") || strings.Contains(got, "artifact-1.mp4") {
		t.Fatalf("Content-Disposition = %q", got)
	}
}

func TestProxyDownloadCORSAllowsConditionalHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/downloads/file/token", nil)
	req.Header.Set("Origin", "https://web.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	req.Header.Set("Access-Control-Request-Headers", "Range, If-Range, If-None-Match, If-Modified-Since, If-Match, If-Unmodified-Since")
	rr := httptest.NewRecorder()
	newDownloadProxyServer(t, "download-proxy-secret").Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	allowed := strings.ToLower(rr.Header().Get("Access-Control-Allow-Headers"))
	for _, name := range []string{"range", "if-range", "if-none-match", "if-modified-since", "if-match", "if-unmodified-since"} {
		if !strings.Contains(allowed, name) {
			t.Errorf("Access-Control-Allow-Headers = %q, missing %s", allowed, name)
		}
	}
}

func TestProxyDownloadRelaysNodeLocalArtifactPreconditionFailure(t *testing.T) {
	const secret = "download-proxy-secret"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-Match"); got != `"old-etag"` {
			t.Errorf("origin If-Match = %q", got)
			http.Error(w, "unexpected condition", http.StatusBadRequest)
			return
		}
		w.Header().Set("ETag", `"new-etag"`)
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer origin.Close()
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:          "download-remote-412",
		PlayMethod:         streamtoken.PlayMethodDownload,
		TranscodeNode:      origin.URL,
		DownloadArtifactID: "artifact-1",
	}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/downloads/file/"+token, nil)
	req.Header.Set("If-Match", `"old-etag"`)
	rr := httptest.NewRecorder()
	newDownloadProxyServer(t, secret).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusPreconditionFailed || rr.Header().Get("ETag") != `"new-etag"` {
		t.Fatalf("status=%d etag=%q body=%q", rr.Code, rr.Header().Get("ETag"), rr.Body.String())
	}
}

func TestProxyDownloadReportsNodeLocalArtifactMissing(t *testing.T) {
	const secret = "download-proxy-secret"
	origin := httptest.NewServer(http.NotFoundHandler())
	defer origin.Close()
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:             "download-remote-missing",
		PlayMethod:            streamtoken.PlayMethodDownload,
		TranscodeNode:         origin.URL,
		DownloadArtifactID:    "artifact-opaque",
		DownloadArtifactRowID: "artifact-row",
	}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reporter := &recordingArtifactMissReporter{}
	server := newDownloadProxyServer(t, secret)
	server.SetRemoteArtifactMissReporter(reporter)
	rr := httptest.NewRecorder()
	server.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/downloads/file/"+token, nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if reporter.artifactID != "artifact-row" || reporter.originNodeURL != origin.URL || reporter.originArtifactID != "artifact-opaque" {
		t.Fatalf("missing report = %+v", reporter)
	}
}

func TestProxyDownloadRejectsRemoteArtifactWithInvalidOpaqueID(t *testing.T) {
	const secret = "download-proxy-secret"
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:          "download-remote-invalid",
		PlayMethod:         streamtoken.PlayMethodDownload,
		TranscodeNode:      "http://origin.invalid",
		DownloadArtifactID: "../escape",
	}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	newDownloadProxyServer(t, secret).Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/downloads/file/"+token, nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestProxyDownloadToneMapTokenRequiresCompleteAttestation(t *testing.T) {
	const secret = "download-proxy-secret"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("incomplete token reached origin")
	}))
	defer origin.Close()
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID: "download-attested", PlayMethod: streamtoken.PlayMethodToneMapDownload,
		TranscodeNode: origin.URL, DownloadArtifactID: "artifact-1",
	}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	newDownloadProxyServer(t, secret).Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/downloads/file/"+token, nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestProxyDownloadAcceptsCompleteToneMapAttestationToken(t *testing.T) {
	const secret = "download-proxy-secret"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloadprepare.SetResultHeaders(w.Header(), downloadprepare.Result{FileSize: 5, ExecutionFingerprint: "fingerprint"})
		_, _ = w.Write([]byte("bytes"))
	}))
	defer origin.Close()
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID: "download-attested", PlayMethod: streamtoken.PlayMethodToneMapDownload,
		TranscodeNode: origin.URL, DownloadArtifactID: "artifact-1",
		DownloadArtifactSize: 5, DownloadExecutionFingerprint: "fingerprint",
	}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	newDownloadProxyServer(t, secret).Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/downloads/file/"+token, nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "bytes" {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
}

func TestProxyDownloadRejectsPlaybackToken(t *testing.T) {
	const secret = "download-proxy-secret"
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:  "playback-1",
		MediaPath:  "/media/movie.mkv",
		PlayMethod: "direct",
	}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/downloads/file/"+token, nil)
	rr := httptest.NewRecorder()
	newDownloadProxyServer(t, secret).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		body, _ := io.ReadAll(rr.Result().Body)
		t.Fatalf("status = %d, body = %s", rr.Code, body)
	}
}

func TestProxyDownloadRejectsExpiredToken(t *testing.T) {
	const secret = "download-proxy-secret"
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:  "download-expired",
		MediaPath:  "/media/movie.mkv",
		PlayMethod: streamtoken.PlayMethodDownload,
	}, secret, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodHead, "/downloads/file/"+token, nil)
	rr := httptest.NewRecorder()
	newDownloadProxyServer(t, secret).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestProxyDownloadReturnsNotFoundForMissingFile(t *testing.T) {
	const secret = "download-proxy-secret"
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:  "download-missing",
		MediaPath:  filepath.Join(t.TempDir(), "gone.mp4"),
		PlayMethod: streamtoken.PlayMethodDownload,
	}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/downloads/file/"+token, nil)
	rr := httptest.NewRecorder()
	newDownloadProxyServer(t, secret).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}
