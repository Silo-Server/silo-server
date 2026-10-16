package workercontrolcore

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workersource"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

//nolint:containedctx // The response-writer barrier observes its request cancellation while holding the first write.
type authorityFirstWrite struct {
	http.ResponseWriter
	once   sync.Once
	first  chan struct{}
	resume <-chan struct{}
	ctx    context.Context
}

func (w *authorityFirstWrite) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *authorityFirstWrite) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.once.Do(func() {
		_ = http.NewResponseController(w.ResponseWriter).Flush()
		close(w.first)
		select {
		case <-w.resume:
		case <-w.ctx.Done():
		}
	})
	return n, err
}

//nolint:funlen // The first-write barrier and concurrent authority refresh must stay in one ordered stream-revocation scenario.
func TestSourceHTTPInvalidationKeepsHealthyReadAndInterruptsRevokedRead(t *testing.T) {
	for _, change := range []string{"unrelated-work", "finish-admission", "stop"} {
		t.Run(change, func(t *testing.T) {
			service, store, node := authorityTestService(t)
			a := activateAuthorityWork(t, store, "streamed")
			data := bytes.Repeat([]byte("source-authority\n"), 1<<17)
			path := filepath.Join(t.TempDir(), "source.bin")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			entered, resume := make(chan struct{}), make(chan struct{})
			first, continueWrite := make(chan struct{}), make(chan struct{})
			releaseAuthority := sync.OnceFunc(func() { close(resume) })
			releaseWrite := sync.OnceFunc(func() { close(continueWrite) })
			defer releaseAuthority()
			defer releaseWrite()
			var armed atomic.Bool
			var once sync.Once
			service.options.ExecutionActive = func(context.Context, Node, workerwire.Assignment) bool {
				if armed.Load() {
					once.Do(func() {
						close(entered)
						<-resume
					})
				}
				return true
			}
			var handler http.Handler
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handler.ServeHTTP(&authorityFirstWrite{ResponseWriter: w, first: first, resume: continueWrite, ctx: r.Context()}, r)
			}))
			defer server.Close()
			service.options.Origin = server.URL
			service.options.AllowHTTP = true
			sources, err := workersource.New(workersource.Options{
				Store: workersource.NewMemoryStore(), Origin: server.URL, Active: service.WorkActive,
				PathApprover: func(_ context.Context, got, kind string) error {
					if got != path || kind != "media" {
						return ErrDenied
					}
					return nil
				},
				VerifyRequest: func(r *http.Request, id int) error {
					return service.VerifyRequest(r, id, workeridentity.DomainNodeProof)
				},
				Limits: workersource.Limits{ChunkBytes: 16 << 10},
			})
			if err != nil {
				t.Fatal(err)
			}
			handler = sources.Handler()
			source, err := sources.Acquire(t.Context(), node.ID, a.WorkID, a.Generation, path, "media")
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, source.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("If-Match", source.ETag)
			req.Header.Set("X-Silo-Source-Grant", source.Grant)
			// The fixture's generated key also supplies the enrolled node proof.
			if err = workeridentity.Sign(req, server.URL, workeridentity.DomainNodeProof, service.options.PrivateKey, node.ID, true); err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Timeout: 5 * time.Second}
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("source GET status: %d", response.StatusCode)
			}
			prefix := make([]byte, 64)
			if _, err = io.ReadFull(response.Body, prefix); err != nil {
				t.Fatal(err)
			}
			awaitAuthorityGate(t, first)
			// Force a real cache miss after bytes have already crossed TCP, then
			// pause the last authority check while the control-plane event occurs.
			armed.Store(true)
			service.invalidateCached(node.ID, a.WorkID)
			releaseWrite()
			awaitAuthorityGate(t, entered)
			switch change {
			case "unrelated-work":
				service.invalidateCached(node.ID, "other")
			case "finish-admission":
				service.finishAdmission(a)
			case "stop":
				if err = service.stop(t.Context(), node, a.WorkID); err != nil {
					t.Fatal(err)
				}
			}
			releaseAuthority()
			rest, readErr := io.ReadAll(response.Body)
			got := bytes.Join([][]byte{prefix, rest}, nil)
			if change == "stop" {
				if readErr == nil || len(got) >= len(data) {
					t.Fatal("genuinely revoked source read completed")
				}
				return
			}
			if readErr != nil || !bytes.Equal(got, data) {
				t.Fatalf("healthy source truncated during %s: bytes=%d/%d error=%v", change, len(got), len(data), readErr)
			}
		})
	}
}
