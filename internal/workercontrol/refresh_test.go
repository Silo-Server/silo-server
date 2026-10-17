package workercontrol

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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type refreshChallenge struct {
	BootID   string `json:"boot_id"`
	Nonce    string `json:"nonce"`
	Sequence uint64 `json:"sequence"`
}

type refreshTransport func(*http.Request) (*http.Response, error)

func (f refreshTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newRefreshFixture(t *testing.T, serve func(http.ResponseWriter, *http.Request, refreshChallenge, func())) *Runtime {
	t.Helper()
	r, _ := newRefreshFixtureWithKey(t, serve, nil)
	return r
}

func newRefreshFixtureWithKey(t *testing.T, serve func(http.ResponseWriter, *http.Request, refreshChallenge, func()), configure func(*workerwire.Snapshot)) (*Runtime, ed25519.PrivateKey) {
	t.Helper()
	corePublic, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	nodePublic, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
	core := httptest.NewUnstartedServer(nil)
	origin := "http://" + core.Listener.Addr().String()
	verifier := &workeridentity.Verifier{Origin: origin, Domain: workeridentity.DomainNodeProof, AllowHTTP: true, ResolveKey: func(int) (ed25519.PublicKey, error) { return nodePublic, nil }}
	core.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := verifier.Verify(request, 17); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var input refreshChallenge
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		serve(w, request, input, func() {
			snapshot := workerwire.Snapshot{Version: 1, NodeID: 17, BootID: input.BootID, Nonce: input.Nonce, Sequence: input.Sequence + 1, Active: map[string]uint64{"work": 4}, ValidForMillis: 2000}
			if configure != nil {
				configure(&snapshot)
			}
			body, _ := json.Marshal(snapshot)
			grant, err := workeridentity.EncodeGrant(workeridentity.Grant{Version: 1, Domain: workeridentity.DomainSnapshotGrant, Audience: "http://worker.example.test", NodeID: 17, ExpiresAt: time.Now().Add(time.Minute).Unix(), Body: body}, coreKey)
			if err != nil {
				t.Error(err)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"grant": grant})
		})
	})
	core.Start()
	t.Cleanup(core.Close)
	r, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: origin, CorePublicKey: corePublic, NodePrivateKey: nodeKey, NodeURL: "http://worker.example.test", AllowHTTP: true}, Scratch: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	return r, coreKey
}

// refreshWaitContext observes when a caller starts waiting for the shared RPC.
//
//nolint:containedctx // Test wrapper preserves caller cancellation while exposing its wait through Done.
type refreshWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *refreshWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func awaitRefresh(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("signed authority refresh did not finish")
		return nil
	}
}

func TestRefreshWaiterCancellationDoesNotWaitForOtherRequest(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	r := newRefreshFixture(t, func(_ http.ResponseWriter, _ *http.Request, _ refreshChallenge, reply func()) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		reply()
	})
	t.Cleanup(unblock)
	first := make(chan error, 1)
	go func() { first <- r.refresh(t.Context()) }()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	joined := &refreshWaitContext{Context: ctx, waiting: make(chan struct{})}
	second := make(chan error, 1)
	go func() { second <- r.refresh(joined) }()
	select {
	case <-joined.waiting:
	case <-time.After(100 * time.Millisecond):
		unblock()
		_ = awaitRefresh(t, first)
		t.Fatal("concurrent caller could not wait independently for shared refresh")
	}
	cancel()
	select {
	case err := <-second:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled waiter returned %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		unblock()
		_ = awaitRefresh(t, first)
		t.Fatal("canceled waiter remained blocked behind another caller's HTTP request")
	}
	unblock()
	if err := awaitRefresh(t, first); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshInitiatorCancellationKeepsSharedSignedResponse(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	var calls atomic.Int32
	r := newRefreshFixture(t, func(_ http.ResponseWriter, _ *http.Request, _ refreshChallenge, reply func()) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		reply()
	})
	t.Cleanup(unblock)
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { first <- r.refresh(ctx) }()
	<-entered
	joined := &refreshWaitContext{Context: t.Context(), waiting: make(chan struct{})}
	second := make(chan error, 1)
	go func() { second <- r.refresh(joined) }()
	select {
	case <-joined.waiting:
	case <-time.After(100 * time.Millisecond):
		cancel()
		unblock()
		_ = awaitRefresh(t, first)
		_ = awaitRefresh(t, second)
		t.Fatal("concurrent refresh could not join the running request")
	}
	cancel()
	if err := awaitRefresh(t, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("initiating waiter returned %v", err)
	}
	unblock()
	if err := awaitRefresh(t, second); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || !r.workAllowed("work", 4) {
		t.Fatalf("cancellation discarded shared authority: requests=%d", calls.Load())
	}
}

func TestRefreshRetriesBodyTimeoutWithFreshChallenge(t *testing.T) {
	var mu sync.Mutex
	var challenges []refreshChallenge
	r := newRefreshFixture(t, func(w http.ResponseWriter, request *http.Request, input refreshChallenge, reply func()) {
		mu.Lock()
		challenges = append(challenges, input)
		first := len(challenges) == 1
		mu.Unlock()
		if first {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"grant":"`))
			w.(http.Flusher).Flush()
			<-request.Context().Done()
			return
		}
		reply()
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := r.refresh(ctx); err != nil {
		t.Fatalf("one stalled response prevented healthy retry: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(challenges) != 2 || challenges[0].Nonce == challenges[1].Nonce || challenges[0].Sequence != 0 || challenges[1].Sequence != 0 || !r.workAllowed("work", 4) {
		t.Fatalf("retry reused or advanced failed challenge: %+v", challenges)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sequence != 1 {
		t.Fatalf("failed response advanced installed sequence: %d", r.sequence)
	}
}

func TestRefreshBoundsRetriesAndPreservesExistingAuthority(t *testing.T) {
	for _, failure := range []struct {
		name string
		code int
		body string
		want int32
	}{
		{name: "unavailable", code: http.StatusServiceUnavailable, want: 2},
		{name: "rate-limited", code: http.StatusTooManyRequests, want: 2},
		{name: "denied", code: http.StatusUnauthorized, want: 1},
		{name: "complete-malformed", code: http.StatusOK, body: `{"grant":`, want: 1},
	} {
		t.Run(failure.name, func(t *testing.T) {
			var failed atomic.Bool
			var calls atomic.Int32
			r := newRefreshFixture(t, func(w http.ResponseWriter, _ *http.Request, _ refreshChallenge, reply func()) {
				if !failed.Load() {
					reply()
					return
				}
				calls.Add(1)
				w.WriteHeader(failure.code)
				_, _ = w.Write([]byte(failure.body))
			})
			if err := r.refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			failed.Store(true)
			if err := r.refresh(t.Context()); err == nil {
				t.Fatal("failed snapshot was accepted")
			}
			if calls.Load() != failure.want || !r.workAllowed("work", 4) {
				t.Fatalf("failed refresh retried incorrectly or withdrew existing lease: requests=%d", calls.Load())
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.sequence != 1 {
				t.Fatal("failed snapshot advanced installed sequence")
			}
		})
	}
}

func TestRefreshRejectsCompletedReplyAfterAttemptDeadline(t *testing.T) {
	r := newRefreshFixture(t, func(_ http.ResponseWriter, _ *http.Request, _ refreshChallenge, reply func()) { reply() })
	base := r.client.Transport
	r.client.Transport = refreshTransport(func(request *http.Request) (*http.Response, error) {
		response, err := base.RoundTrip(request)
		if err != nil {
			return nil, err
		}
		// Keep a complete signed reply available even after request cancellation,
		// as a buffered transport may do. The attempt's deadline still governs it.
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			return nil, err
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		<-request.Context().Done()
		return response, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := r.refresh(ctx); err == nil || r.workAllowed("work", 4) {
		t.Fatal("completed late snapshot renewed authority after its attempt deadline")
	}
	if ctx.Err() != nil {
		t.Fatal("refresh inherited the caller's longer deadline")
	}
}
