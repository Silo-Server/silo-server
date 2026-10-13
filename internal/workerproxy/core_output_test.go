package workerproxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

type commandRecorder struct{ calls atomic.Int32 }

func (c *commandRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
}

func TestCoreOutputUsesCachedLocalGrantWithoutSendingCustomerToken(t *testing.T) {
	const secret = "core-output-regression-secret"
	const token = "customer.jwt.with-original-library-path"
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var grants, outputs atomic.Int32
	worker := httptest.NewUnstartedServer(nil)
	node := workercontrolcore.Node{ID: 17, URL: "http://" + worker.Listener.Addr().String(), Enabled: true, AllowHTTP: true}
	verifier := &workeridentity.Verifier{Origin: node.URL, Domain: workeridentity.DomainProxyOutputRequest, AllowHTTP: true}
	worker.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encoded := r.Header.Get("X-Silo-Stream-Token")
		grant, err := workeridentity.DecodeGrant(encoded, workeridentity.DomainProxyOutputGrant, node.URL, public, time.Now())
		var authority workeridentity.OutputAuthority
		if encoded == token || strings.Count(encoded, ".") != 1 || err != nil || json.Unmarshal(grant.Body, &authority) != nil || verifier.VerifyKey(r, node.ID, authority.ProxyPublicKey) != nil {
			http.Error(w, "customer token or invalid proof reached worker", http.StatusUnauthorized)
			return
		}
		outputs.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	worker.Start()
	defer worker.Close()
	transport, err := New("https://core.example.test", secret, outputNodes{node})
	if err != nil {
		t.Fatal(err)
	}
	transport.Grant = func(_ context.Context, customer, nodeURL string) (workercontrolcore.GrantReply, error) {
		grants.Add(1)
		if customer != token || nodeURL != node.URL {
			t.Error("local authority received wrong token or worker")
		}
		grant := workeridentity.Grant{Version: 1, Domain: workeridentity.DomainProxyOutputGrant, Audience: node.URL, NodeID: node.ID, WorkID: "work", Generation: 2, ExpiresAt: time.Now().Add(time.Minute).Unix()}
		key, err := workeridentity.OutputPrivateKey(secret, grant)
		if err != nil {
			return workercontrolcore.GrantReply{}, err
		}
		grant.Body, err = json.Marshal(workeridentity.OutputAuthority{ProxyPublicKey: ed25519.PublicKey(key[ed25519.SeedSize:]), Claims: json.RawMessage(`{"sid":"work","path":"source:opaque"}`), Epoch: workeridentity.OutputEpoch(secret)})
		if err != nil {
			return workercontrolcore.GrantReply{}, err
		}
		encoded, err := workeridentity.EncodeGrant(grant, private)
		return workercontrolcore.GrantReply{Grant: encoded}, err
	}
	commands := &commandRecorder{}
	router := transport.WrapOutputs(http.DefaultTransport, commands)
	for i := range 8 {
		method, path := http.MethodGet, "/transcode/work/segment/segment0.ts"
		if i == 7 {
			method, path = http.MethodPost, path+"/downloaded"
		}
		request, err := http.NewRequestWithContext(WithToken(t.Context(), token), method, node.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := router.RoundTrip(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("output status=%d", response.StatusCode)
		}
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, node.URL+"/admin/reload-config", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := router.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if grants.Load() != 1 || outputs.Load() != 8 || commands.calls.Load() != 1 {
		t.Fatalf("local grants=%d, signed outputs=%d, commands=%d", grants.Load(), outputs.Load(), commands.calls.Load())
	}
}
