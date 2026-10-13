package workercontrolcore

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

func TestVerifiedHTTPSCoreDispatchesOnlyApprovedHTTPWorker(t *testing.T) {
	corePub, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	nodePub, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
	var verifier workeridentity.Verifier
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := verifier.Verify(r, 7); err != nil {
			t.Errorf("worker command: %v", err)
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer worker.Close()
	verifier = workeridentity.Verifier{Origin: worker.URL, Domain: workeridentity.DomainCoreCommand, AllowHTTP: true, ResolveKey: func(int) (ed25519.PublicKey, error) { return corePub, nil }}
	for _, opted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unapproved", true: "explicitly_approved"}[opted], func(t *testing.T) {
			node := Node{ID: 7, URL: worker.URL, PublicKey: nodePub, Enabled: true, AllowHTTP: opted}
			core := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			defer core.Close()
			service, err := New(Options{Origin: core.URL, PrivateKey: coreKey, Nodes: testNodes{[]Node{node}}, Store: NewMemoryStore()})
			if err != nil {
				t.Fatal(err)
			}
			core.Config.Handler = service.Handler()
			client := &http.Client{Transport: service.WrapTransport(nil)}
			response, err := client.Get(worker.URL + "/status")
			if !opted {
				if err == nil {
					_ = response.Body.Close()
					t.Fatal("unapproved HTTP node accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			// The node uses a CA-verifying HTTPS client to contact the core.
			request, _ := http.NewRequest("POST", core.URL+"/api/internal/workers/7/snapshot", strings.NewReader(`{"boot_id":"boot","nonce":"nonce","sequence":0}`))
			if err = workeridentity.Sign(request, core.URL, workeridentity.DomainNodeProof, nodeKey, 7, false); err != nil {
				t.Fatal(err)
			}
			response, err = core.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != 200 {
				body, _ := io.ReadAll(response.Body)
				t.Fatalf("HTTPS control status=%d %s", response.StatusCode, body)
			}
		})
	}
}

func TestHTTPCorePolicyDoesNotApproveHTTPWorker(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	node := Node{ID: 7, URL: "http://worker.example", PublicKey: pub, Enabled: true}
	service, err := New(Options{Origin: "http://core.example", AllowHTTP: true, PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.PrepareRequest(httptest.NewRequest("GET", node.URL+"/status", nil)); err == nil {
		t.Fatal("core HTTP approval silently opted worker into HTTP")
	}
}
