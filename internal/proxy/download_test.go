package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

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
			t.Fatalf("origin path = %q", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer "+secret {
			t.Fatalf("origin Authorization = %q", auth)
		}
		if got := r.Header.Get("Range"); got != "bytes=2-5" {
			t.Fatalf("origin Range = %q", got)
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
}

func TestProxyDownloadRelaysNodeLocalArtifactPreconditionFailure(t *testing.T) {
	const secret = "download-proxy-secret"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-Match"); got != `"old-etag"` {
			t.Fatalf("origin If-Match = %q", got)
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
