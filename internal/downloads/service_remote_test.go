package downloads

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestServiceRelaysRemoteArtifactOnEstablishedRoute(t *testing.T) {
	const secret = "artifact-secret"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/downloads/artifacts/artifact-1" || r.Header.Get("Authorization") != "Bearer "+secret {
			t.Fatalf("origin request = %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if r.Header.Get("Range") != "bytes=1-3" {
			t.Fatalf("Range = %q", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Length", "3")
		w.Header().Set("Content-Range", "bytes 1-3/5")
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("123"))
	}))
	defer origin.Close()
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = secret
	svc := &Service{artifacts: &ArtifactManager{liveCfg: func() *config.Config { return cfg }}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/dl/file", nil)
	req.Header.Set("Range", "bytes=1-3")
	rr := httptest.NewRecorder()
	err := svc.serveFileTarget(req.Context(), rr, req, &FileTarget{
		OriginNodeURL:    origin.URL,
		OriginArtifactID: "artifact-1",
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusPartialContent || rr.Body.String() != "123" || rr.Header().Get("Content-Range") != "bytes 1-3/5" {
		t.Fatalf("response status=%d body=%q range=%q", rr.Code, rr.Body.String(), rr.Header().Get("Content-Range"))
	}
}

func TestServiceDoesNotAppendAPIErrorAfterRemoteBodyIsCommitted(t *testing.T) {
	const secret = "artifact-secret"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "123")
	}))
	defer origin.Close()
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = secret
	svc := &Service{artifacts: &ArtifactManager{liveCfg: func() *config.Config { return cfg }}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/dl/file", nil)
	rr := httptest.NewRecorder()
	if err := svc.serveFileTarget(req.Context(), rr, req, &FileTarget{
		OriginNodeURL:    origin.URL,
		OriginArtifactID: "artifact-1",
	}, 7); err != nil {
		t.Fatalf("serveFileTarget returned after committing response: %v", err)
	}
	if rr.Code != http.StatusOK || rr.Body.String() != "123" {
		t.Fatalf("response status=%d body=%q", rr.Code, rr.Body.String())
	}
}
