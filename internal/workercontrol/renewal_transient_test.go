package workercontrol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestTransientRenewalMustNotFenceUnexpiredGeneration(t *testing.T) {
	pub, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	var unavailable atomic.Bool
	unavailable.Store(true)
	var original workerwire.Source
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if unavailable.Load() {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		body, _ := json.Marshal(original)
		encoded, err := workeridentity.EncodeGrant(workeridentity.Grant{Version: 1, Domain: workeridentity.DomainSourceGrant, Audience: "http://worker.example.test", NodeID: 17, WorkID: "work", Generation: 4, ExpiresAt: time.Now().Add(2 * time.Second).Unix(), Body: body}, coreKey)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"grant": encoded})
	}))
	defer core.Close()
	r, e := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: core.URL, CorePublicKey: pub, NodePrivateKey: private, NodeURL: "http://worker.example.test", AllowHTTP: true}, Scratch: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close(context.Background()) }()
	if e = r.authority.SetNonce("test-nonce"); e != nil {
		t.Fatal(e)
	}
	if e = r.authority.Apply(raceSnapshot(r, 1, 4, 2000), time.Now()); e != nil {
		t.Fatal(e)
	}
	a := raceAssignment(4)
	s := a.Sources["source:media"]
	s.Origin = core.URL
	s.URL = core.URL + "/source"
	a.Sources["source:media"] = s
	original = s
	if _, e = r.openSources(a); e != nil {
		t.Fatal(e)
	}
	now := time.Now().Add(46 * time.Second)
	r.renewals.renewDue(t.Context(), now)
	t.Logf("source503 with current signed work lease: signedAllowed=%v permanentlyFenced=%v", r.authority.Allowed("work", 4), r.renewals.blocked("work", 4))
	if r.renewals.blocked("work", 4) {
		t.Fatal("transient503 permanently fenced generation before original signed authority expired")
	}
	unavailable.Store(false)
	r.renewals.renewDue(t.Context(), now.Add(2*time.Second))
	if r.renewals.blocked("work", 4) || !r.workAllowed("work", 4) {
		t.Fatal("healthy renewal could not recover same generation")
	}
	r.renewals.mu.Lock()
	entries := len(r.renewals.entries)
	r.renewals.mu.Unlock()
	if entries != 1 {
		t.Fatalf("renewal entries=%d", entries)
	}
}

func TestStageSourcesReplacesClosedHandleMaps(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(strconv.FormatBool(published), func(t *testing.T) {
			r := raceRuntime(t)
			if err := r.authority.SetNonce("test-nonce"); err != nil {
				t.Fatal(err)
			}
			if err := r.authority.Apply(raceSnapshot(r, 1, 4, 2000), time.Now()); err != nil {
				t.Fatal(err)
			}
			assignment := raceAssignment(4)
			old, err := r.stageSources(assignment)
			if err != nil {
				t.Fatal(err)
			}
			if published {
				r.publishStaged("work", 4)
			}
			if err = old["source:media"].Close(); err != nil {
				t.Fatal(err)
			}
			next, err := r.stageSources(assignment)
			if err != nil {
				t.Fatal(err)
			}
			if next["source:media"] == old["source:media"] || next["source:media"].Validate(t.Context()) != nil {
				t.Fatal("reconstruction reused closed map")
			}
		})
	}
}

type renewalBodyTransport func(*http.Request) (*http.Response, error)

func (f renewalBodyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type observedRenewalBody struct {
	io.ReadCloser
	errors chan error
}

func (b *observedRenewalBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		select {
		case b.errors <- err:
		default:
		}
	}
	return n, err
}

//nolint:funlen,gocyclo,nestif // One source lease spans the interrupted response, retry, and definitive malformed-response controls.
func TestSourceRenewalBodyFailureRecovery(t *testing.T) {
	for _, failure := range []string{"timeout", "reset", "framing-short-body", "caller-cancellation", "complete-malformed", "complete-truncated-json", "complete-wrong-type", "invalid-envelope"} {
		t.Run(failure, func(t *testing.T) {
			pub, coreKey, _ := ed25519.GenerateKey(rand.Reader)
			_, private, _ := ed25519.GenerateKey(rand.Reader)
			var healthy atomic.Bool
			var calls atomic.Int32
			var original workerwire.Source
			started, headersReceived := make(chan struct{}), make(chan struct{})
			var headersOnce sync.Once
			bodyErrors := make(chan error, 1)
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				_, _ = io.Copy(io.Discard, request.Body)
				calls.Add(1)
				if !healthy.Load() {
					w.Header().Set("Content-Type", "application/json")
					switch failure {
					case "complete-malformed":
						_, _ = io.WriteString(w, `{ "grant": ] }`)
						return
					case "complete-truncated-json":
						_, _ = io.WriteString(w, `{`)
						return
					case "complete-wrong-type":
						_, _ = io.WriteString(w, `{ "grant": 27 }`)
						return
					case "invalid-envelope":
						_, _ = io.WriteString(w, `{ "grant": "invalid" }`)
						return
					case "framing-short-body":
						w.Header().Set("Content-Length", "128")
						_, _ = io.WriteString(w, `{ "grant": "`)
						return
					}
					_, _ = io.WriteString(w, `{ "grant": "`)
					w.(http.Flusher).Flush()
					close(started)
					if failure == "reset" {
						// Reset only after the real transport has accepted HTTP200.
						// The failure must come from reading its body, not Do.
						<-headersReceived
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						if tcp, ok := conn.(*net.TCPConn); ok {
							_ = tcp.SetLinger(0)
						}
						_ = conn.Close()
						return
					}
					<-request.Context().Done()
					return
				}
				body, _ := json.Marshal(original)
				encoded, err := workeridentity.EncodeGrant(workeridentity.Grant{Version: 1, Domain: workeridentity.DomainSourceGrant, Audience: "http://worker.example.test", NodeID: 17, WorkID: "work", Generation: 4, ExpiresAt: time.Now().Add(2 * time.Second).Unix(), Body: body}, coreKey)
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"grant": encoded})
			}))
			defer core.Close()
			transport := http.DefaultTransport.(*http.Transport).Clone()
			defer transport.CloseIdleConnections()
			observed := renewalBodyTransport(func(request *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(request)
				if err == nil {
					response.Body = &observedRenewalBody{ReadCloser: response.Body, errors: bodyErrors}
					headersOnce.Do(func() { close(headersReceived) })
				}
				return response, err
			})
			r, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: core.URL, CorePublicKey: pub, NodePrivateKey: private, NodeURL: "http://worker.example.test", AllowHTTP: true}, Scratch: t.TempDir(), Transport: observed})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(context.Background()) }()
			if err = r.authority.SetNonce("test-nonce"); err != nil {
				t.Fatal(err)
			}
			if err = r.authority.Apply(raceSnapshot(r, 1, 4, 2000), time.Now()); err != nil {
				t.Fatal(err)
			}
			a := raceAssignment(4)
			original = a.Sources["source:media"]
			original.Origin, original.URL = core.URL, core.URL+"/source"
			a.Sources["source:media"] = original
			handles, err := r.openSources(a)
			if err != nil {
				t.Fatal(err)
			}
			h := handles["source:media"]
			stop, stopped := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(stopped)
				ticker := time.NewTicker(250 * time.Millisecond)
				defer ticker.Stop()
				for sequence := uint64(2); ; sequence++ {
					select {
					case <-stop:
						return
					case <-ticker.C:
						if err := r.authority.Apply(raceSnapshot(r, sequence, 4, 2000), time.Now()); err != nil {
							t.Error(err)
							return
						}
					}
				}
			}()
			defer func() { close(stop); <-stopped }()
			now := time.Now().Add(46 * time.Second)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan struct{})
			go func() { r.renewals.renewDue(ctx, now); close(done) }()
			if failure == "caller-cancellation" {
				select {
				case <-headersReceived:
					cancel()
				case <-time.After(time.Second):
					t.Fatal("partial200 not started")
				}
			}
			select {
			case <-done:
			case <-time.After(sourceRenewTimeout + time.Second):
				t.Fatal("renewal exceeded original timeout")
			}
			if !r.authority.Allowed("work", 4) {
				t.Fatal("independent signed work authority expired")
			}
			if failure == "complete-malformed" || failure == "complete-truncated-json" || failure == "complete-wrong-type" || failure == "invalid-envelope" {
				if !r.renewals.blocked("work", 4) || r.inputs.Allowed(h.URL()) {
					t.Fatal("complete malformed200 failed to retire source")
				}
				return
			}
			select {
			case bodyErr := <-bodyErrors:
				if errors.Is(bodyErr, io.EOF) {
					t.Fatal("expected actual non-EOF response body transport error")
				}
				t.Logf("HTTP200 response body error: %v", bodyErr)
			default:
				t.Fatal("failure occurred before reading HTTP200 body")
			}
			if r.renewals.blocked("work", 4) || !r.inputs.Allowed(h.URL()) {
				t.Fatal("response body interruption permanently retired healthy source")
			}
			healthy.Store(true)
			// An unavailable reply schedules the existing one-second retry; it
			// must neither spin immediately nor wait the healthy 45-second interval.
			if failure != "caller-cancellation" {
				r.renewals.renewDue(t.Context(), now.Add(sourceRenewRetryInterval-time.Nanosecond))
				if calls.Load() != 1 {
					t.Fatal("renewal retried before its existing retry interval")
				}
			}
			r.renewals.renewDue(t.Context(), now.Add(sourceRenewRetryInterval))
			if calls.Load() != 2 || !r.workAllowed("work", 4) || !r.inputs.Allowed(h.URL()) {
				t.Fatal("healthy signed renewal did not recover existing source")
			}
			r.renewals.mu.Lock()
			entry := r.renewals.entries[h]
			r.renewals.mu.Unlock()
			if entry == nil || !entry.next.Equal(now.Add(sourceRenewRetryInterval+sourceRenewInterval)) {
				t.Fatal("healthy renewal failed to restore normal interval")
			}
		})
	}
}

func TestSourceRenewalPartial200Timeout(t *testing.T) {
	corePublic, _, _ := ed25519.GenerateKey(rand.Reader)
	_, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
	started := make(chan struct{}, 1)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{ "grant": "`)
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-request.Context().Done()
	}))
	defer core.Close()
	r, err := New(t.Context(), Options{Binding: workeridentity.Binding{Version: 1, NodeID: 17, CoreURL: core.URL, CorePublicKey: corePublic, NodePrivateKey: nodeKey, NodeURL: "http://worker.example.test", AllowHTTP: true}, Scratch: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(context.Background()) }()
	if err := r.authority.SetNonce("test-nonce"); err != nil {
		t.Fatal(err)
	}
	if err := r.authority.Apply(raceSnapshot(r, 1, 4, 2000), time.Now()); err != nil {
		t.Fatal(err)
	}
	a := raceAssignment(4)
	source := a.Sources["source:media"]
	source.Origin = core.URL
	source.URL = core.URL + "/source"
	a.Sources["source:media"] = source
	if _, err := r.openSources(a); err != nil {
		t.Fatal(err)
	}
	// Keep control authority alive while only the source renewal is unavailable.
	// Its two-second timeout must not race the independent two-second lease.
	stopSnapshots, snapshotsDone := make(chan struct{}), make(chan struct{})
	snapshotErrors := make(chan error, 1)
	go func() {
		defer close(snapshotsDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for sequence := uint64(2); ; sequence++ {
			select {
			case <-stopSnapshots:
				return
			case <-ticker.C:
				if err := r.authority.Apply(raceSnapshot(r, sequence, 4, 2000), time.Now()); err != nil {
					snapshotErrors <- err
					return
				}
			}
		}
	}()
	defer func() { close(stopSnapshots); <-snapshotsDone }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r.renewals.renewDue(ctx, time.Now().Add(46*time.Second))
	if r.renewals.blocked("work", 4) {
		t.Fatal("caller cancellation permanently fenced healthy lease")
	}
	done := make(chan struct{})
	go func() { r.renewals.renewDue(t.Context(), time.Now().Add(46*time.Second)); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("renewal request not started")
	}
	select {
	case <-done:
	case <-time.After(sourceRenewTimeout + time.Second):
		t.Fatal("unavailable renewal exceeded bounded timeout")
	}
	t.Logf("after partial-200 request deadline: signedAllowed=%v permanentFence=%v", r.authority.Allowed("work", 4), r.renewals.blocked("work", 4))
	if r.renewals.blocked("work", 4) {
		t.Fatal("unavailable renewal permanently fenced healthy signed work")
	}
	select {
	case err := <-snapshotErrors:
		t.Fatalf("control authority refresh: %v", err)
	default:
	}
}
