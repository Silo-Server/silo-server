package workercontrol

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

//nolint:funlen // The open viewer stream and shared artifact must be checked across the same authority revocation.
func TestRevokedViewerStopsOpenOutputWithoutStoppingSharedArtifact(t *testing.T) {
	public, core, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, workerKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	runtime, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, NodeURL: origin, CoreURL: "https://core.example.test", CorePublicKey: public, NodePrivateKey: workerKey, AllowHTTP: true}, Scratch: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(t.Context()) })
	const secret = "output-stream-fixture-secret"
	epoch := workeridentity.OutputEpoch(secret)
	if err := runtime.config.apply(workerwire.ExecutionConfig{Revision: "initial", SubtitleSyncCapacity: 1, OutputAuthorizationEpoch: epoch}); err != nil {
		t.Fatal(err)
	}
	viewer, other := strings.Repeat("a", 64), strings.Repeat("b", 64)
	snapshot := workerwire.Snapshot{Version: 1, NodeID: 17, BootID: runtime.bootID, Nonce: "initial", Sequence: 1, ValidForMillis: 2000, Active: map[string]uint64{"artifact": 1}, Outputs: map[string]bool{viewer: true, other: true}}
	if err := runtime.authority.SetNonce(snapshot.Nonce); err != nil {
		t.Fatal(err)
	}
	if err := runtime.authority.Apply(snapshot, time.Now()); err != nil {
		t.Fatal(err)
	}
	continueWrite := make(chan struct{})
	writes := make(chan error, 1)
	runtime.handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := runtime.verifyCommand(request); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Length", "100")
		_, err := io.WriteString(w, "first")
		if err != nil {
			writes <- err
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			writes <- err
			return
		}
		<-continueWrite
		_, err = io.WriteString(w, strings.Repeat("x", 95))
		writes <- err
	})
	server.Config.Handler = runtime.Handler()
	server.Start()
	defer server.Close()
	claims, err := json.Marshal(streamtoken.Claims{SessionID: "download", DownloadArtifactID: "artifact", TranscodeNode: origin, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}})
	if err != nil {
		t.Fatal(err)
	}
	grant := workeridentity.Grant{Version: 1, Domain: workeridentity.DomainProxyOutputGrant, Audience: origin, NodeID: 17, WorkID: "artifact", Generation: 1, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	proxyKey, err := workeridentity.OutputPrivateKey(secret, grant)
	if err != nil {
		t.Fatal(err)
	}
	grant.Body, err = json.Marshal(workeridentity.OutputAuthority{ProxyPublicKey: ed25519.PublicKey(proxyKey[ed25519.SeedSize:]), Claims: claims, Epoch: epoch, AuthorityID: viewer})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := workeridentity.EncodeGrant(grant, core)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+"/downloads/artifacts/artifact", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Silo-Stream-Token", encoded)
	if err := workeridentity.Sign(request, origin, workeridentity.DomainProxyOutputRequest, proxyKey, 17, true); err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	first := make([]byte, 5)
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "first" {
		t.Fatalf("initial output denied: status=%d bytes=%q err=%v", response.StatusCode, first, err)
	}
	snapshot.Sequence++
	snapshot.Outputs = map[string]bool{other: true}
	if err := runtime.authority.Apply(snapshot, time.Now()); err != nil {
		t.Fatal(err)
	}
	close(continueWrite)
	if err := <-writes; err == nil {
		t.Fatal("revoked viewer continued writing artifact bytes")
	}
	tail, err := io.ReadAll(response.Body)
	if len(tail) != 0 || err == nil {
		t.Fatalf("revoked stream was not truncated: bytes=%d err=%v", len(tail), err)
	}
	if !runtime.workAllowed("artifact", 1) || !runtime.authority.OutputAllowed(other) {
		t.Fatal("revoking a viewer stopped the shared artifact or another viewer")
	}
}
