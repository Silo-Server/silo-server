package workercontrol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

//nolint:funlen // The proof-scope matrix shares one delegated grant and verifies replay rejection before and after its stop.
func TestDelegatedOutputHTTPRejectsProofScopeReplayAndStop(t *testing.T) {
	corePub, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	_, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
	var coreCalls atomic.Int32
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { coreCalls.Add(1); w.WriteHeader(503) }))
	defer core.Close()
	worker := httptest.NewUnstartedServer(nil)
	nodeURL := "http://" + worker.Listener.Addr().String()
	scratch := t.TempDir()
	runtime, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: core.URL, CorePublicKey: corePub, NodePrivateKey: nodeKey, NodeURL: nodeURL, AllowHTTP: true}, Scratch: scratch})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close(context.Background()) }()
	worker.Config.Handler = runtime.Handler()
	worker.Start()
	defer worker.Close()
	const work = "artifact"
	const authorityID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := os.MkdirAll(filepath.Join(scratch, downloadprepare.ArtifactDirectoryName), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, downloadprepare.ArtifactDirectoryName, work+".mp4"), []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runtime.authority.SetNonce("output-test"); err != nil {
		t.Fatal(err)
	}
	snapshot := workerwire.Snapshot{Version: 1, NodeID: 17, BootID: runtime.bootID, Nonce: "output-test", Sequence: 1, Active: map[string]uint64{work: 1}, Pending: map[string]uint64{work: 2}, ValidForMillis: 2000, Outputs: map[string]bool{authorityID: true}}
	if err := runtime.authority.Apply(snapshot, time.Now()); err != nil {
		t.Fatal(err)
	}
	runtime.generations[work] = 1
	claims := streamtoken.Claims{SessionID: "download-session", DownloadArtifactID: work, TranscodeNode: nodeURL, RoutingExecutionNodeID: 17, PlayMethod: streamtoken.PlayMethodDownload, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
	grant := workeridentity.Grant{Version: 1, Domain: workeridentity.DomainProxyOutputGrant, Audience: nodeURL, NodeID: 17, WorkID: work, Generation: 1, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	const secret = "proxy-test-secret"
	epoch := workeridentity.OutputEpoch(secret)
	mint := func(g workeridentity.Grant, key ed25519.PrivateKey) string {
		t.Helper()
		private, err := workeridentity.OutputPrivateKey(secret, g)
		if err != nil {
			t.Fatal(err)
		}
		inner, _ := json.Marshal(claims)
		g.Body, _ = json.Marshal(workeridentity.OutputAuthority{ProxyPublicKey: private.Public().(ed25519.PublicKey), Claims: inner, Epoch: epoch, AuthorityID: authorityID})
		token, err := workeridentity.EncodeGrant(g, key)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	token := mint(grant, coreKey)
	request := func(token, method, path string, signed bool) *http.Request {
		t.Helper()
		req, err := http.NewRequest(method, nodeURL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Silo-Stream-Token", token)
		req.Header.Set("Range", "bytes=2-5")
		if signed {
			private, _ := workeridentity.OutputPrivateKey(secret, grant)
			if err := workeridentity.Sign(req, nodeURL, workeridentity.DomainProxyOutputRequest, private, 17, true); err != nil {
				t.Fatal(err)
			}
		}
		return req
	}
	client := workeridentity.NewHTTPClient(true, time.Second)
	check := func(req *http.Request, want int) string {
		t.Helper()
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: status=%d body=%q want=%d", req.Method, req.URL.Path, res.StatusCode, body, want)
		}
		return string(body)
	}
	path := "/downloads/artifacts/" + work
	check(request(token, "GET", path, true), 401)
	if err := runtime.config.apply(workerwire.ExecutionConfig{Revision: "initial-output", SegmentRetentionSeconds: 600, SubtitleSyncCapacity: 1, OutputAuthorizationEpoch: epoch}); err != nil {
		t.Fatal(err)
	}
	valid := request(token, "GET", path, true)
	if body := check(valid, 206); body != "2345" {
		t.Fatalf("range=%q", body)
	}
	check(valid, 401)
	check(request(token, "GET", path, false), 401)
	check(request(token, "GET", "/status", true), 401)
	check(request(token, "DELETE", path, true), 401)
	check(request(token, "GET", "/downloads/artifacts/other", true), 401)
	for _, header := range []string{"Range", "X-Silo-Stream-Token", "X-Silo-Transcode-Segment-Generation"} {
		req := request(token, "GET", path, true)
		req.Header.Set(header, "tampered")
		check(req, 401)
	}
	_, forger, _ := ed25519.GenerateKey(rand.Reader)
	check(request(mint(grant, forger), "GET", path, true), 401)
	for _, change := range []func(*workeridentity.Grant){func(g *workeridentity.Grant) { g.NodeID = 18 }, func(g *workeridentity.Grant) { g.WorkID = "other" }, func(g *workeridentity.Grant) { g.Generation = 2 }, func(g *workeridentity.Grant) { g.Audience = "http://other.invalid" }, func(g *workeridentity.Grant) { g.ExpiresAt = time.Now().Add(-time.Second).Unix() }} {
		changed := grant
		change(&changed)
		check(request(mint(changed, coreKey), "GET", path, true), 401)
	}
	if coreCalls.Load() != 0 {
		t.Fatalf("valid/denied output performed core RPCs: %d", coreCalls.Load())
	}
	if _, err := runtime.verifyToken(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	rotated := workeridentity.OutputEpoch("rotated-proxy-secret")
	if err := runtime.config.apply(workerwire.ExecutionConfig{Revision: "rotated-output", SegmentRetentionSeconds: 600, SubtitleSyncCapacity: 1, OutputAuthorizationEpoch: rotated}); err != nil {
		t.Fatal(err)
	}
	check(request(token, "GET", path, true), 401)
	if _, err := runtime.verifyToken(t.Context(), token); err == nil {
		t.Fatal("old output epoch remained token-cached")
	}
	if err := runtime.config.apply(workerwire.ExecutionConfig{Revision: "restored-output", SegmentRetentionSeconds: 600, SubtitleSyncCapacity: 1, OutputAuthorizationEpoch: epoch}); err != nil {
		t.Fatal(err)
	}
	snapshot.Sequence = 2
	snapshot.Active = map[string]uint64{}
	snapshot.Pending = map[string]uint64{}
	if err := runtime.authority.Apply(snapshot, time.Now()); err != nil {
		t.Fatal(err)
	}
	check(request(token, "GET", path, true), 401)
	if _, err := runtime.verifyToken(t.Context(), token); err == nil {
		t.Fatal("stopped output token remained cached")
	}
}
