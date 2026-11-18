package downloads

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
)

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
	svc := &Service{artifacts: &ArtifactManager{liveCfg: func() *config.Config { return cfg }, nodeKeys: nodeauthtest.NodeKey(secret)}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/dl/file", nil)
	rr := httptest.NewRecorder()
	err := svc.serveFileTarget(req.Context(), rr, req, &FileTarget{
		OriginNodeURL:    origin.URL,
		OriginArtifactID: "artifact-1",
	}, 7)
	if !errors.Is(err, ErrResponseCommitted) {
		t.Fatalf("serveFileTarget error = %v, want ErrResponseCommitted", err)
	}
	if rr.Code != http.StatusOK || rr.Body.String() != "123" {
		t.Fatalf("response status=%d body=%q", rr.Code, rr.Body.String())
	}
}

// keylessKeyring has no key for any transcode node.
type keylessKeyring struct{}

func (keylessKeyring) KeyFor(nodeauth.Target, string) (string, error) {
	return "", nodeauth.ErrNoNodeKey
}
