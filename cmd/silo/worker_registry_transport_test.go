package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerproxy"
	"github.com/Silo-Server/silo-server/internal/workerregistration"
)

const registryTransportSecret = "registry-transport-test-secret"
const registryTransportCommand = `{"session_id":"legacy-work","input_path":"/private/approved-source.mkv"}`
const registryTransportOutputPath = "/transcode/legacy-work/segment/segment0.ts"
const registryTransportResponse = "legacy-response"

type registryTransportRequest struct {
	method, path, body, authorization, token, domain string
	proofErr, bodyErr                                error
}

type registryTransportFixture struct {
	registry *coreWorkerRegistry
	writer   *pgxpool.Pool
	counter  *registryQueryCounter
	nodeID   int
	origin   string
	token    string
	base     *http.Transport
	requests chan registryTransportRequest
	hits     atomic.Int64
}

func newRegistryTransportFixture(t *testing.T, handler http.Handler) *registryTransportFixture {
	t.Helper()
	registry, writer, counter, id, _ := registryCacheFixture(t)
	f := &registryTransportFixture{registry: registry, writer: writer, counter: counter, nodeID: id, requests: make(chan registryTransportRequest, 256)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		var proofErr error
		if r.Method == http.MethodPost {
			proofErr = nodeauth.Verify(r, registryTransportSecret)
		}
		body, err := io.ReadAll(r.Body)
		f.requests <- registryTransportRequest{method: r.Method, path: r.URL.Path, body: string(body), authorization: r.Header.Get("Authorization"), token: r.Header.Get("X-Silo-Stream-Token"), domain: r.Header.Get(workeridentity.HeaderDomain), proofErr: proofErr, bodyErr: err}
		if handler != nil {
			handler.ServeHTTP(w, r)
			return
		}
		_, _ = io.WriteString(w, registryTransportResponse)
	}))
	t.Cleanup(server.Close)
	// A distinct origin keeps durable retired-origin rows from colliding when
	// the OS reuses a loopback listener's port in a later test.
	f.origin = "http://transport-" + uuid.NewString() + ".example.test"
	f.base = http.DefaultTransport.(*http.Transport).Clone()
	f.base.Proxy = nil
	f.base.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	t.Cleanup(f.base.CloseIdleConnections)
	if _, err := writer.Exec(t.Context(), "UPDATE stream_nodes SET url=$1 WHERE id=$2", f.origin, id); err != nil {
		t.Fatal(err)
	}
	var err error
	f.token, err = streamtoken.Sign(streamtoken.Claims{SessionID: "legacy-work", MediaPath: "/private/approved-source.mkv", PlayMethod: "transcode", TranscodeNode: f.origin, OriginCredential: streamtoken.OriginCredential{OriginLoginSessionID: uuid.NewString()}}, registryTransportSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *registryTransportFixture) clients(t *testing.T, registry *coreWorkerRegistry) (*http.Client, *http.Client, *atomic.Int64) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := workercontrolcore.New(workercontrolcore.Options{Origin: "https://core.example.test", PrivateKey: key, Nodes: registry, Store: workercontrolcore.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := workerproxy.New("https://core.example.test", registryTransportSecret, registry)
	if err != nil {
		t.Fatal(err)
	}
	grants := &atomic.Int64{}
	outputs.Grant = func(context.Context, string, string) (workercontrolcore.GrantReply, error) {
		grants.Add(1)
		return workercontrolcore.GrantReply{}, workercontrolcore.ErrDenied
	}
	return &http.Client{Transport: service.WrapTransport(f.base)}, &http.Client{Transport: outputs.Wrap(f.base)}, grants
}

func (f *registryTransportFixture) replica(t *testing.T) (*coreWorkerRegistry, *registryQueryCounter) {
	t.Helper()
	config := f.writer.Config().Copy()
	counter := &registryQueryCounter{}
	config.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return &coreWorkerRegistry{repo: workerregistration.New(pool, nil)}, counter
}

func (f *registryTransportFixture) command(t *testing.T, origin string) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, origin+"/transcode/start", strings.NewReader(registryTransportCommand))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	if err = nodeauth.Sign(r, origin, registryTransportSecret); err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *registryTransportFixture) output(t *testing.T, origin string) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+registryTransportOutputPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("X-Silo-Stream-Token", f.token)
	return r
}

func (f *registryTransportFixture) observed(t *testing.T) registryTransportRequest {
	t.Helper()
	select {
	case got := <-f.requests:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("legacy HTTP server did not observe request")
		return registryTransportRequest{}
	}
}

func (f *registryTransportFixture) legacy(t *testing.T, client *http.Client, request *http.Request) {
	t.Helper()
	wantAuthorization := request.Header.Get("Authorization")
	wantToken := request.Header.Get("X-Silo-Stream-Token")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != registryTransportResponse {
		t.Fatalf("legacy response: status=%d body=%q error=%v", response.StatusCode, body, err)
	}
	got := f.observed(t)
	if got.method != request.Method || got.path != request.URL.Path || got.authorization != wantAuthorization || got.token != wantToken || got.domain != "" || got.bodyErr != nil {
		t.Fatal("legacy transport changed the request path, authorization, token, body read, or proof domain")
	}
	if request.Method == http.MethodPost && (got.body != registryTransportCommand || got.proofErr != nil) {
		t.Fatalf("legacy source command changed: body=%q proof error=%v", got.body, got.proofErr)
	}
}

func (f *registryTransportFixture) denied(t *testing.T, commands, outputs *http.Client, origin string) {
	t.Helper()
	before := f.hits.Load()
	for _, check := range []struct {
		name    string
		client  *http.Client
		request *http.Request
	}{
		{"source command", commands, f.command(t, origin)},
		{"original viewer token", outputs, f.output(t, origin)},
	} {
		response, err := check.client.Do(check.request)
		if response != nil {
			_ = response.Body.Close()
		}
		if err == nil {
			t.Fatalf("%s reached a retired or standalone origin through legacy HTTP", check.name)
		}
	}
	if added := f.hits.Load() - before; added != 0 {
		t.Fatalf("denied admission leaked %d HTTP requests to the legacy server", added)
	}
}

func (f *registryTransportFixture) enroll(t *testing.T) {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Repository enrollment exercises the same trigger; its signed ticket and
	// single-use redemption are covered by the workerregistration tests.
	if _, err = f.writer.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key,allow_http) VALUES($1,$2,TRUE)", f.nodeID, []byte(public)); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerRegistryWarmLegacyHTTPUsesSnapshot(t *testing.T) {
	f := newRegistryTransportFixture(t, nil)
	commands, outputs, grants := f.clients(t, f.registry)
	if err := f.registry.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := f.counter.queries.Load()
	for range 64 {
		f.legacy(t, commands, f.command(t, f.origin))
		f.legacy(t, outputs, f.output(t, f.origin))
	}
	if added := f.counter.queries.Load() - before; added != 0 {
		t.Fatalf("128 warm legacy HTTP requests executed %d registry SQL queries; want zero", added)
	}
	if f.hits.Load() != 128 || grants.Load() != 0 {
		t.Fatalf("warm legacy requests=%d standalone output grants=%d", f.hits.Load(), grants.Load())
	}
}

func TestWorkerRegistryEnrollmentCommitStopsLegacyHTTPAcrossReplicas(t *testing.T) {
	f := newRegistryTransportFixture(t, nil)
	replica, replicaCounter := f.replica(t)
	commands, outputs, grants := f.clients(t, f.registry)
	otherCommands, otherOutputs, otherGrants := f.clients(t, replica)
	for _, registry := range []*coreWorkerRegistry{f.registry, replica} {
		if err := registry.refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	f.legacy(t, commands, f.command(t, f.origin))
	f.legacy(t, outputs, f.output(t, f.origin))
	f.legacy(t, otherCommands, f.command(t, f.origin))
	f.legacy(t, otherOutputs, f.output(t, f.origin))
	before, otherBefore := f.counter.queries.Load(), replicaCounter.queries.Load()
	f.enroll(t)
	// No explicit refresh or event delivery is allowed between commit and the
	// first command/output admission in either independently pooled replica.
	f.denied(t, commands, outputs, f.origin)
	f.denied(t, otherCommands, otherOutputs, f.origin)
	if f.counter.queries.Load() <= before || replicaCounter.queries.Load() <= otherBefore {
		t.Fatal("a replica admitted postcommit requests without refreshing its expired legacy snapshot")
	}
	if grants.Load() != 1 || otherGrants.Load() != 1 {
		t.Fatal("postcommit output did not use standalone grant admission in both replicas")
	}
}

func TestWorkerRegistryRetiredOriginsDenyHTTPAfterMoveDeleteAndRestart(t *testing.T) {
	f := newRegistryTransportFixture(t, nil)
	f.enroll(t)
	if err := f.registry.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	commands, outputs, _ := f.clients(t, f.registry)
	f.denied(t, commands, outputs, f.origin)
	replacement := strings.Replace(f.origin, "transport-", "replacement-", 1)
	if _, err := f.writer.Exec(t.Context(), "UPDATE stream_nodes SET url=$1 WHERE id=$2", replacement, f.nodeID); err != nil {
		t.Fatal(err)
	}
	restarted, _ := f.replica(t)
	if err := restarted.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	restartedCommands, restartedOutputs, _ := f.clients(t, restarted)
	f.denied(t, commands, outputs, f.origin)
	f.denied(t, restartedCommands, restartedOutputs, f.origin)
	if node, found, err := restarted.ByURL(t.Context(), replacement); err != nil || !found || !node.Enabled || node.ID != f.nodeID {
		t.Fatalf("replacement origin missing: found=%v enabled=%v error=%v", found, node.Enabled, err)
	}
	if _, err := f.writer.Exec(t.Context(), "DELETE FROM stream_nodes WHERE id=$1", f.nodeID); err != nil {
		t.Fatal(err)
	}
	afterDelete, _ := f.replica(t)
	if err := afterDelete.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	deletedCommands, deletedOutputs, _ := f.clients(t, afterDelete)
	for _, origin := range []string{f.origin, replacement} {
		f.denied(t, restartedCommands, restartedOutputs, origin)
		f.denied(t, deletedCommands, deletedOutputs, origin)
	}
	if f.hits.Load() != 0 {
		t.Fatalf("retired or standalone origin received %d legacy HTTP requests", f.hits.Load())
	}
}

func TestWorkerRegistrySnapshotExpiryDoesNotCancelLegacyResponseBody(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	f := newRegistryTransportFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != registryTransportOutputPath {
			_, _ = io.WriteString(w, registryTransportResponse)
			return
		}
		_, _ = io.WriteString(w, "before-expiry:")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			_, _ = io.WriteString(w, "after-expiry")
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(finish)
	commands, outputs, _ := f.clients(t, f.registry)
	if err := f.registry.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	refreshed := time.Now()
	before := f.counter.queries.Load()
	response, err := outputs.Do(f.output(t, f.origin))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	prefix := make([]byte, len("before-expiry:"))
	if _, err = io.ReadFull(response.Body, prefix); err != nil || string(prefix) != "before-expiry:" {
		t.Fatalf("legacy response did not begin: bytes=%q error=%v", prefix, err)
	}
	f.observed(t)
	// Wait for the actual two-second admission certificate lifetime, while the
	// server deliberately holds an already admitted response body open.
	timer := time.NewTimer(time.Until(refreshed.Add(2100 * time.Millisecond)))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	f.legacy(t, commands, f.command(t, f.origin))
	if f.counter.queries.Load() <= before {
		t.Fatal("new admission reused the expired snapshot while the old response body was open")
	}
	finish()
	suffix, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(suffix) != "after-expiry" {
		t.Fatalf("snapshot expiry interrupted an admitted legacy body: status=%d suffix=%q error=%v", response.StatusCode, suffix, err)
	}
}

func TestWorkerRegistryMalformedStoredSuffixNeverAllowsLegacyHTTP(t *testing.T) {
	f := newRegistryTransportFixture(t, nil)
	if _, err := f.writer.Exec(t.Context(), "UPDATE stream_nodes SET url=$1 WHERE id=$2", f.origin+"/%ZZ", f.nodeID); err != nil {
		t.Fatal(err)
	}
	f.enroll(t)
	commands, outputs, _ := f.clients(t, f.registry)
	for _, suffix := range []string{"/%ZZ", "/source#%ZZ"} {
		t.Run(suffix, func(t *testing.T) {
			if _, err := f.writer.Exec(t.Context(), "UPDATE stream_nodes SET url=$1 WHERE id=$2", f.origin+suffix, f.nodeID); err != nil {
				t.Fatal(err)
			}
			// Force a complete refresh, including on a new process, rather than rely
			// on a previous certificate that already knew the canonical origin.
			restarted, _ := f.replica(t)
			if err := restarted.refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			currentCommands, currentOutputs, _ := f.clients(t, restarted)
			f.denied(t, currentCommands, currentOutputs, f.origin)
		})
	}
	if _, err := f.writer.Exec(t.Context(), "DELETE FROM worker_http_nodes WHERE node_id=$1", f.nodeID); err != nil {
		t.Fatal(err)
	}
	restarted, _ := f.replica(t)
	if err := restarted.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	retiredCommands, retiredOutputs, _ := f.clients(t, restarted)
	f.denied(t, retiredCommands, retiredOutputs, f.origin)
	f.denied(t, commands, outputs, f.origin)
}
