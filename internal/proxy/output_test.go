package proxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerproxy"
)

type outputTestNodes struct{ node workercontrolcore.Node }

func (n outputTestNodes) ByURL(_ context.Context, url string) (workercontrolcore.Node, bool, error) {
	return n.node, url == n.node.URL, nil
}
func (n outputTestNodes) ByID(_ context.Context, id int) (workercontrolcore.Node, bool, error) {
	return n.node, id == n.node.ID, nil
}

func TestStandaloneArtifactRelayPreservesTokenAndVerifiesReceipt(t *testing.T) {
	const secret = "artifact-output-test-secret"
	const work = "artifact-output"
	const workerFingerprint = "worker-frozen-fingerprint"
	const clientFingerprint = "client-frozen-fingerprint"
	corePublic, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	nodePublic, _, _ := ed25519.GenerateKey(rand.Reader)
	var wrongFingerprint, wrongSize atomic.Bool
	var grantCalls, workerCalls atomic.Int32
	worker := httptest.NewUnstartedServer(nil)
	node := workercontrolcore.Node{ID: 7, URL: "http://" + worker.Listener.Addr().String(), PublicKey: nodePublic, Enabled: true, AllowHTTP: true}
	verifier := &workeridentity.Verifier{Origin: node.URL, Domain: workeridentity.DomainProxyOutputRequest, AllowHTTP: true}
	worker.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grant, err := workeridentity.DecodeGrant(r.Header.Get("X-Silo-Stream-Token"), workeridentity.DomainProxyOutputGrant, node.URL, corePublic, time.Now())
		var authority workeridentity.OutputAuthority
		if err != nil || json.Unmarshal(grant.Body, &authority) != nil || verifier.VerifyKey(r, 7, authority.ProxyPublicKey) != nil {
			w.WriteHeader(401)
			return
		}
		workerCalls.Add(1)
		if r.URL.Path != "/downloads/artifacts/"+work || r.Header.Get("Range") != "bytes=2-5" {
			t.Error("artifact route/range changed")
			w.WriteHeader(400)
			return
		}
		fingerprint := workerFingerprint
		if wrongFingerprint.Load() {
			fingerprint = "wrong"
		}
		size := int64(10)
		if wrongSize.Load() {
			size = 11
		}
		downloadprepare.SetResultHeaders(w.Header(), downloadprepare.Result{ArtifactID: work, FileSize: size, ExecutionFingerprint: fingerprint})
		w.Header().Set("Content-Range", "bytes 2-5/10")
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(206)
		if r.Method != "HEAD" {
			_, _ = io.WriteString(w, "2345")
		}
	})
	worker.Start()
	defer worker.Close()
	token := artifactRelayToken(t, streamtoken.Claims{SessionID: "download", PlayMethod: streamtoken.PlayMethodToneMapDownload, TranscodeNode: node.URL, RoutingExecutionNodeID: 7, DownloadArtifactID: work, DownloadArtifactSize: 10, DownloadExecutionFingerprint: clientFingerprint}, secret)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/internal/workers/output-grant" || nodeauth.Verify(r, secret) != nil {
			w.WriteHeader(401)
			return
		}
		var input struct {
			Token   string `json:"token"`
			NodeURL string `json:"node_url"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Token != token || input.NodeURL != node.URL {
			w.WriteHeader(403)
			return
		}
		claims, err := streamtoken.Verify(input.Token, secret)
		if err != nil {
			w.WriteHeader(403)
			return
		}
		grant := workeridentity.Grant{Version: 1, Domain: workeridentity.DomainProxyOutputGrant, Audience: node.URL, NodeID: 7, WorkID: work, Generation: 1, ExpiresAt: time.Now().Add(time.Minute).Unix()}
		key, err := workeridentity.OutputPrivateKey(secret, grant)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		raw, _ := json.Marshal(claims)
		grant.Body, _ = json.Marshal(workeridentity.OutputAuthority{ProxyPublicKey: key.Public().(ed25519.PublicKey), Claims: raw, Epoch: workeridentity.OutputEpoch(secret), ArtifactFingerprint: workerFingerprint, ClientFingerprint: clientFingerprint})
		encoded, err := workeridentity.EncodeGrant(grant, coreKey)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		grantCalls.Add(1)
		_ = json.NewEncoder(w).Encode(workercontrolcore.GrantReply{Grant: encoded})
	}))
	defer core.Close()
	transport, err := workerproxy.New(core.URL, secret, outputTestNodes{node})
	if err != nil {
		t.Fatal(err)
	}
	nodehttp.Configure(transport.Wrap)
	defer nodehttp.Configure(nil)
	server := newDownloadProxyServer(t, secret)
	serve := artifactRelayRequester(t, server.Handler(), token)
	serve("GET", 206)
	serve("HEAD", 206)
	wrongFingerprint.Store(true)
	serve("GET", 502)
	wrongFingerprint.Store(false)
	wrongSize.Store(true)
	serve("GET", 502)
	wrongSize.Store(false)
	serve("GET", 206)
	if grantCalls.Load() != 1 || workerCalls.Load() != 5 {
		t.Fatalf("cached grant/worker requests=%d/%d", grantCalls.Load(), workerCalls.Load())
	}
}

func artifactRelayRequester(t *testing.T, handler http.Handler, token string) func(string, int) {
	t.Helper()
	return func(method string, want int) {
		t.Helper()
		request := httptest.NewRequest(method, "/downloads/file/"+token, nil)
		request.Header.Set("Range", "bytes=2-5")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, request)
		if rec.Code != want {
			t.Fatalf("%s relay: status=%d body=%q want=%d", method, rec.Code, rec.Body.String(), want)
		}
		if want == 206 && method != "HEAD" && rec.Body.String() != "2345" {
			t.Fatalf("relay bytes=%q", rec.Body.String())
		}
		if want == 502 && rec.Body.String() == "2345" {
			t.Fatal("refused receipt released media")
		}
	}
}

func artifactRelayToken(t *testing.T, claims streamtoken.Claims, secret string) string {
	t.Helper()
	token, err := streamtoken.Sign(claims, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
