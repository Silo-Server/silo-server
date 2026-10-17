package workercontrol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

type legacyStopNodes struct{}

func (legacyStopNodes) ByID(context.Context, int) (workercontrolcore.Node, bool, error) {
	return workercontrolcore.Node{}, false, nil
}
func (legacyStopNodes) ByURL(context.Context, string) (workercontrolcore.Node, bool, error) {
	return workercontrolcore.Node{}, false, nil
}

func TestRemoteStopPreservesLegacySignatureWithWorkerTransport(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := workercontrolcore.New(workercontrolcore.Options{Origin: "https://core.example.test", PrivateKey: key, Nodes: legacyStopNodes{}, Store: workercontrolcore.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	nodehttp.Configure(service.WrapTransport)
	t.Cleanup(func() { nodehttp.Configure(nil) })
	var signed atomic.Bool
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		signed.Store(request.Method == http.MethodDelete && request.URL.Path == "/transcode/legacy-work" && nodeauth.Verify(request, "legacy-test-secret") == nil && request.Header.Get(workeridentity.HeaderDomain) == "")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(legacy.Close)
	manager := playback.NewTranscodeManager()
	manager.JWTSecretFn = func() string { return "legacy-test-secret" }
	if err := manager.CancelRemoteTranscode(t.Context(), "legacy-work", legacy.URL); err != nil {
		t.Fatal(err)
	}
	if !signed.Load() {
		t.Fatal("registered-worker hook changed the unregistered legacy proof")
	}
}
