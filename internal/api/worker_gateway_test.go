package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

func TestWorkerGatewayRouterStartsAndPreservesSignedPath(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	const path = "/api/internal/workers/17/snapshot"
	called := false
	router := NewRouter(Dependencies{Config: cfg, WorkerHTTP: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != path || r.URL.RawQuery != "scope=exact" {
			t.Errorf("signed request rewritten: %s %s", r.Method, r.URL.String())
		}
		w.WriteHeader(http.StatusAccepted)
	})})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path+"?scope=exact", nil))
	if !called || w.Code != http.StatusAccepted {
		t.Fatalf("worker route=%d called=%v", w.Code, called)
	}
}

func TestWorkerGatewayOnlyExposesDeclaredMethodsAndPaths(t *testing.T) {
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	router := NewRouter(Dependencies{Config: cfg, WorkerHTTP: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusAccepted) })})
	for _, operation := range []struct{ method, path string }{
		{"POST", "/api/internal/workers/enroll"}, {"POST", "/api/internal/workers/output-grant"},
		{"POST", "/api/internal/workers/17/snapshot"}, {"POST", "/api/internal/workers/17/report"},
		{"GET", "/api/internal/workers/17/recipes/work"}, {"HEAD", "/api/internal/workers/17/recipes/work"},
		{"DELETE", "/api/internal/workers/17/recipes/work"}, {"POST", "/api/internal/workers/17/revoke"},
		{"POST", "/api/internal/workers/17/tokens"}, {"POST", "/api/internal/workers/17/sources/grant/renew"},
		{"GET", "/api/internal/workers/17/sources/grant"}, {"HEAD", "/api/internal/workers/17/sources/grant"},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(operation.method, operation.path, nil))
		if w.Code != http.StatusAccepted {
			t.Fatalf("declared %s %s=%d", operation.method, operation.path, w.Code)
		}
	}
	if calls != 12 {
		t.Fatalf("delegated%d declared operations", calls)
	}
	for _, operation := range []struct{ method, path string }{
		{"GET", "/api/internal/workers/enroll"}, {"DELETE", "/api/internal/workers/output-grant"},
		{"POST", "/api/internal/workers/17/recipes/work"}, {"GET", "/api/internal/workers/17/secrets"},
		{"GET", "/api/internal/workers/17/sources/grant/more"}, {"GET", "/api/internal/workers/17"},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(operation.method, operation.path, nil))
		if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("undeclared operation=%d", w.Code)
		}
	}
	if calls != 12 {
		t.Fatal("gateway exposed hidden operations")
	}
}

type gatewayNodes struct{ node workercontrolcore.Node }

func (n gatewayNodes) ByID(_ context.Context, id int) (workercontrolcore.Node, bool, error) {
	return n.node, id == n.node.ID, nil
}
func (n gatewayNodes) ByURL(_ context.Context, url string) (workercontrolcore.Node, bool, error) {
	return n.node, url == n.node.URL, nil
}

func TestWorkerGatewayRetainsCoreProofAdmission(t *testing.T) {
	_, coreKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, nodeKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := workercontrolcore.New(workercontrolcore.Options{Origin: "https://core.example", PrivateKey: coreKey, Nodes: gatewayNodes{workercontrolcore.Node{ID: 17, URL: "https://worker.example", PublicKey: pub, Enabled: true}}, Store: workercontrolcore.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Dependencies{Config: cfg, WorkerHTTP: service.Handler()})
	for _, scenario := range []struct {
		name             string
		signed, tampered bool
		status           int
	}{{"unsigned", false, false, http.StatusUnauthorized}, {"signed", true, false, http.StatusOK}, {"changed_query", true, true, http.StatusUnauthorized}} {
		t.Run(scenario.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "https://core.example/api/internal/workers/17/snapshot", strings.NewReader(`{"boot_id":"boot","nonce":"challenge","sequence":0}`))
			if scenario.signed {
				if err := workeridentity.Sign(req, "https://core.example", workeridentity.DomainNodeProof, nodeKey, 17, false); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.tampered {
				req.URL.RawQuery = "changed=1"
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != scenario.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, scenario.status, response.Body.String())
			}
		})
	}
}
