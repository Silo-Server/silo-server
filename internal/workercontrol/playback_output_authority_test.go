package workercontrol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
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

func TestOrdinaryPlaybackRequiresCurrentViewerOnCachedAndFreshRequests(t *testing.T) {
	public, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	_, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer core.Close()
	const origin = "http://worker.example.test"
	runtime, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, NodeURL: origin, CoreURL: core.URL, CorePublicKey: public, NodePrivateKey: nodeKey, AllowHTTP: true}, Scratch: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	const work = "playback-work"
	const secret = "playback-output-test-secret"
	epoch := workeridentity.OutputEpoch(secret)
	if err := runtime.config.apply(workerwire.ExecutionConfig{Revision: "initial", SubtitleSyncCapacity: 1, OutputAuthorizationEpoch: epoch}); err != nil {
		t.Fatal(err)
	}
	viewer, other := strings.Repeat("a", 64), strings.Repeat("b", 64)
	snapshot := workerwire.Snapshot{Version: 1, NodeID: 17, BootID: runtime.bootID, Nonce: "viewer", Sequence: 1, ValidForMillis: 2000, Active: map[string]uint64{work: 1}, Outputs: map[string]bool{viewer: true, other: true}}
	if err := runtime.authority.SetNonce(snapshot.Nonce); err != nil {
		t.Fatal(err)
	}
	if err := runtime.authority.Apply(snapshot, time.Now()); err != nil {
		t.Fatal(err)
	}
	runtime.generations[work] = 1
	claims := streamtoken.Claims{SessionID: work, UserID: 42, TranscodeNode: origin, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
	grant := workeridentity.Grant{Version: 1, Domain: workeridentity.DomainProxyOutputGrant, Audience: origin, NodeID: 17, WorkID: work, Generation: 1, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	proxyKey, err := workeridentity.OutputPrivateKey(secret, grant)
	if err != nil {
		t.Fatal(err)
	}
	mint := func(authority string) string {
		t.Helper()
		inner, _ := json.Marshal(claims)
		grant.Body, _ = json.Marshal(workeridentity.OutputAuthority{ProxyPublicKey: proxyKey.Public().(ed25519.PublicKey), Claims: inner, Epoch: epoch, AuthorityID: authority})
		encoded, err := workeridentity.EncodeGrant(grant, coreKey)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	token := mint(viewer)
	request := func(encoded string) *http.Request {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, origin+"/transcode/"+work+"/master.m3u8", nil)
		req.Header.Set("X-Silo-Stream-Token", encoded)
		if err := workeridentity.Sign(req, origin, workeridentity.DomainProxyOutputRequest, proxyKey, 17, true); err != nil {
			t.Fatal(err)
		}
		return req
	}
	initial := request(token)
	if err := runtime.verifyCommand(initial); err != nil {
		t.Fatal(err)
	}
	// This is the entry left by a successful recipe-backed token verification.
	runtime.tokens[token] = tokenEntry{claims: &claims, workID: work, generation: 1, expires: time.Now().Add(time.Minute), epoch: epoch, authorityID: viewer}
	if _, err := runtime.verifyToken(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	snapshot.Sequence++
	snapshot.Outputs = map[string]bool{other: true}
	if err := runtime.authority.Apply(snapshot, time.Now()); err != nil {
		t.Fatal(err)
	}
	t.Run("cached token", func(t *testing.T) {
		if _, err := runtime.verifyToken(t.Context(), token); err == nil {
			t.Fatal("revoked login's cached playback grant remained usable")
		}
	})
	t.Run("fresh proof", func(t *testing.T) {
		if err := runtime.verifyCommand(request(token)); err == nil {
			t.Fatal("revoked login's ordinary playback proof remained usable")
		}
	})
	t.Run("open writer", func(t *testing.T) {
		writer := &outputFencedWriter{ResponseWriter: httptest.NewRecorder(), runtime: runtime, request: initial}
		if _, err := writer.Write([]byte("next segment")); err == nil {
			t.Fatal("revoked login's open playback writer remained usable")
		}
	})
	t.Run("unbound grant", func(t *testing.T) {
		if _, _, _, err := runtime.decodeOutput(mint("")); err == nil {
			t.Fatal("ordinary playback grant without viewer authority accepted")
		}
	})
	if !runtime.workAllowed(work, 1) || !runtime.authority.OutputAllowed(other) {
		t.Fatal("revocation stopped another viewer or the producer")
	}
}
