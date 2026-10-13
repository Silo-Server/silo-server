package workerproxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

type outputNodes struct{ node workercontrolcore.Node }

func (n outputNodes) ByURL(_ context.Context, url string) (workercontrolcore.Node, bool, error) {
	return n.node, url == n.node.URL, nil
}
func (n outputNodes) ByID(_ context.Context, id int) (workercontrolcore.Node, bool, error) {
	return n.node, id == n.node.ID, nil
}

func TestOutputHTTPGrantCacheSignsEverySegmentAndStopsWithoutReplay(t *testing.T) {
	const secret = "proxy-cache-test-secret"
	const customerToken = "verified-customer-token"
	corePub, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	nodePub, _, _ := ed25519.GenerateKey(rand.Reader)
	var live atomic.Bool
	live.Store(true)
	var coreCalls, workerCalls atomic.Int32
	worker := httptest.NewUnstartedServer(nil)
	node := workercontrolcore.Node{ID: 7, URL: "http://" + worker.Listener.Addr().String(), PublicKey: nodePub, Enabled: true, AllowHTTP: true}
	verifier := &workeridentity.Verifier{Origin: node.URL, Domain: workeridentity.DomainProxyOutputRequest, AllowHTTP: true}
	worker.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grant, err := workeridentity.DecodeGrant(r.Header.Get("X-Silo-Stream-Token"), workeridentity.DomainProxyOutputGrant, node.URL, corePub, time.Now())
		var authority workeridentity.OutputAuthority
		if err != nil || json.Unmarshal(grant.Body, &authority) != nil || verifier.VerifyKey(r, 7, authority.ProxyPublicKey) != nil {
			w.WriteHeader(401)
			return
		}
		workerCalls.Add(1)
		if !live.Load() {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, "segment")
	})
	worker.Start()
	defer worker.Close()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		coreCalls.Add(1)
		if nodeauth.Verify(r, secret) != nil || !live.Load() {
			w.WriteHeader(403)
			return
		}
		var input struct {
			Token   string `json:"token"`
			NodeURL string `json:"node_url"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Token != customerToken || input.NodeURL != node.URL {
			w.WriteHeader(403)
			return
		}
		grant := workeridentity.Grant{Version: 1, Domain: workeridentity.DomainProxyOutputGrant, Audience: node.URL, NodeID: 7, WorkID: "work", Generation: 1, ExpiresAt: time.Now().Add(time.Minute).Unix()}
		key, _ := workeridentity.OutputPrivateKey(secret, grant)
		grant.Body, _ = json.Marshal(workeridentity.OutputAuthority{ProxyPublicKey: key.Public().(ed25519.PublicKey), Claims: json.RawMessage(`{"sid":"work"}`), Epoch: workeridentity.OutputEpoch(secret)})
		encoded, err := workeridentity.EncodeGrant(grant, coreKey)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(workercontrolcore.GrantReply{Grant: encoded})
	}))
	defer core.Close()
	owner, err := New(core.URL, secret, outputNodes{node})
	if err != nil {
		t.Fatal(err)
	}
	transport := owner.Wrap(http.DefaultTransport)
	request := func(i int) *http.Request {
		ctx := WithToken(t.Context(), customerToken)
		r, err := http.NewRequestWithContext(ctx, "GET", node.URL+"/transcode/work/segment/segment"+strconv.Itoa(i)+".ts", nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	read := func(i int) error {
		res, err := transport.RoundTrip(request(i))
		if err != nil {
			return err
		}
		defer func() { _ = res.Body.Close() }()
		body, err := io.ReadAll(res.Body)
		if err == nil && (res.StatusCode != 200 || string(body) != "segment") {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	if err := read(0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 1; i <= 32; i++ {
		wg.Go(func() { errs <- read(i) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if coreCalls.Load() != 1 || workerCalls.Load() != 33 {
		t.Fatalf("per-segment coreRPC/replay: core=%d worker=%d", coreCalls.Load(), workerCalls.Load())
	}
	live.Store(false)
	if res, err := transport.RoundTrip(request(34)); err == nil {
		if res != nil {
			_ = res.Body.Close()
		}
		t.Fatal("stopped work reused cached grant")
	}
	if coreCalls.Load() != 2 {
		t.Fatalf("stop did not invalidate stale grant once: %d", coreCalls.Load())
	}
}

func TestUnapprovedHTTPOutputIsRejectedBeforeGrantAndNetwork(t *testing.T) {
	var coreCalls, workerCalls atomic.Int32
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { coreCalls.Add(1); w.WriteHeader(500) }))
	defer core.Close()
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { workerCalls.Add(1); w.WriteHeader(200) }))
	defer worker.Close()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	owner, err := New(core.URL, "explicit-policy-test", outputNodes{workercontrolcore.Node{ID: 7, URL: worker.URL, PublicKey: pub, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(WithToken(t.Context(), "customer-token"), http.MethodGet, worker.URL+"/transcode/work/segment/segment0.ts", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := owner.Wrap(http.DefaultTransport).RoundTrip(req); err == nil {
		if res != nil {
			_ = res.Body.Close()
		}
		t.Fatal("unapproved HTTP worker output accepted")
	}
	if coreCalls.Load() != 0 || workerCalls.Load() != 0 {
		t.Fatal("unapproved HTTP target reached grant or worker network")
	}
}
