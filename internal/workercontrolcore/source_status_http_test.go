package workercontrolcore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workersource"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type statusSourceAdapter struct{ *workersource.Authority }

func (a statusSourceAdapter) Release(ctx context.Context, node int, work string, generation uint64) error {
	return a.RevokeWork(ctx, node, work, generation)
}

//nolint:funlen // HTTP reads and renewals classify unavailable versus stopped against the same source generation.
func TestSourceHTTPAndRenewClassifyUnavailableVersusStopped(t *testing.T) {
	_, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	nodePublic, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	defer server.Close()
	store := NewMemoryStore()
	sourceStore := workersource.NewMemoryStore()
	var service *Service
	var pathUnavailable, executionUnavailable atomic.Bool
	path := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := workersource.New(workersource.Options{Origin: origin, Store: sourceStore, ActiveStatus: func(ctx context.Context, node int, work string, generation uint64) (bool, error) {
		return service.WorkActiveStatus(ctx, node, work, generation)
	}, PathApprover: func(context.Context, string, string) error {
		if pathUnavailable.Load() {
			return workersource.ErrUnavailable
		}
		return nil
	}, VerifyRequest: func(r *http.Request, node int) error {
		err := service.VerifyRequest(r, node, workeridentity.DomainSourceRequest)
		if errors.Is(err, ErrUnavailable) {
			return workersource.ErrUnavailable
		}
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: nodePublic, Enabled: true}
	service, err = New(Options{Origin: origin, AllowHTTP: true, PrivateKey: coreKey, Nodes: testNodes{[]Node{node}}, Store: store, Sources: statusSourceAdapter{source}, SourceHandler: source.Handler(), ExecutionStatus: func(context.Context, Node, workerwire.Assignment) (bool, error) {
		if executionUnavailable.Load() {
			return false, ErrUnavailable
		}
		return true, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = service.Handler()
	server.Start()
	assignment := activateAuthorityWork(t, store, "live")
	descriptor, err := source.Acquire(t.Context(), 7, "live", assignment.Generation, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	original, err := sourceStore.Get(t.Context(), descriptor.Grant)
	if err != nil {
		t.Fatal(err)
	}
	call := func(renew bool) (int, http.Header, []byte) {
		method, url, domain := "GET", descriptor.URL, workeridentity.DomainSourceRequest
		var body []byte
		if renew {
			method, url, domain = "POST", url+"/renew", workeridentity.DomainNodeProof
			body, _ = json.Marshal(map[string]any{"work_id": "live", "generation": assignment.Generation})
		}
		r, err := http.NewRequestWithContext(t.Context(), method, url, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-Silo-Source-Grant", descriptor.Grant)
		r.Header.Set("If-Match", descriptor.ETag)
		if !renew {
			r.Header.Set("Range", "bytes=1-3")
		}
		if err = workeridentity.Sign(r, origin, domain, nodeKey, 7, true); err != nil {
			t.Fatal(err)
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		out, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, response.Header, out
	}
	for _, dependency := range []string{"path", "execution"} {
		t.Run(dependency, func(t *testing.T) {
			if dependency == "path" {
				pathUnavailable.Store(true)
			} else {
				executionUnavailable.Store(true)
				service.invalidateCached(7, "live")
			}
			for _, renew := range []bool{false, true} {
				status, headers, _ := call(renew)
				if status != 503 || headers.Get("Retry-After") == "" {
					t.Fatalf("unknown renew=%v status=%d headers=%v", renew, status, headers)
				}
			}
			retained, err := sourceStore.Get(t.Context(), descriptor.Grant)
			if err != nil || retained.ExpiresAt != original.ExpiresAt {
				t.Fatalf("unknown deleted/extended source: %v", err)
			}
			pathUnavailable.Store(false)
			executionUnavailable.Store(false)
			service.invalidateCached(7, "live")
			if status, _, body := call(false); status != 206 || string(body) != "123" {
				t.Fatalf("recovered read=%d/%q", status, body)
			}
		})
	}
	if status, _, _ := call(true); status != 200 {
		t.Fatalf("healthy renewal=%d", status)
	}
	if err = store.Stop(t.Context(), 7, "live"); err != nil {
		t.Fatal(err)
	}
	service.invalidateCached(7, "live")
	for _, renew := range []bool{false, true} {
		status, _, _ := call(renew)
		if status != 403 {
			t.Fatalf("stopped renew=%v status=%d", renew, status)
		}
	}
}
