package workersource

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
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type sourceOwners struct {
	one, two                   *Authority
	serverOne, serverTwo       *httptest.Server
	source                     workerwire.Source
	path                       string
	workerKey                  ed25519.PrivateKey
	ownerProofs, foreignProofs atomic.Int64
}

type foreignMountView struct{ Store }

func (s foreignMountView) Get(ctx context.Context, grant string) (Record, error) {
	r, err := s.Store.Get(ctx, grant)
	r.Identity.Device ^= 1
	r.Path += ".not-mounted-on-this-core"
	return r, err
}

func newSourceOwners(t *testing.T) *sourceOwners {
	t.Helper()
	f := &sourceOwners{}
	base, path, _ := testAuthority(t, "0123456789", Limits{})
	f.path = path
	_, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	workerPublic, workerKey, _ := ed25519.GenerateKey(rand.Reader)
	f.workerKey = workerKey
	verifier := &workeridentity.Verifier{Origin: base.options.Origin, Domain: workeridentity.DomainSourceRequest, ResolveKey: func(int) (ed25519.PublicKey, error) { return workerPublic, nil }}
	f.serverOne = httptest.NewUnstartedServer(nil)
	f.serverTwo = httptest.NewUnstartedServer(nil)
	t.Cleanup(f.serverOne.Close)
	t.Cleanup(f.serverTwo.Close)
	options := base.options
	options.OwnerID = "owner-one"
	options.OwnerURL = "http://" + f.serverOne.Listener.Addr().String()
	options.OwnerKey = coreKey
	options.VerifyRequest = func(r *http.Request, node int) error { f.ownerProofs.Add(1); return verifier.Verify(r, node) }
	var err error
	f.one, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.one.Close)
	options.OwnerID = "owner-two"
	options.OwnerURL = "http://" + f.serverTwo.Listener.Addr().String()
	options.VerifyRequest = func(*http.Request, int) error { f.foreignProofs.Add(1); return ErrDenied }
	f.two, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.two.Close)
	f.two.options.Store = foreignMountView{options.Store}
	f.serverOne.Config.Handler = f.one.Handler()
	f.serverTwo.Config.Handler = f.two.Handler()
	f.serverOne.Start()
	f.serverTwo.Start()
	f.source, err = f.one.Acquire(t.Context(), 7, "work", 3, path, "media")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

type ownerTransport func(*http.Request) (*http.Response, error)

func (t ownerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return t(r) }

type closingOwnerTransport struct{ closed atomic.Int64 }

func (t *closingOwnerTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, ErrUnavailable
}
func (t *closingOwnerTransport) CloseIdleConnections() { t.closed.Add(1) }

func TestOwnerTransportIsExplicitAndClosesIdleConnections(t *testing.T) {
	a, _, _ := testAuthority(t, "0123456789", Limits{})
	previous := http.DefaultTransport
	http.DefaultTransport = ownerTransport(func(*http.Request) (*http.Response, error) { return nil, ErrUnavailable })
	defer func() { http.DefaultTransport = previous }()
	if _, err := New(a.options); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unsupported default transport: %v", err)
	}
	transport := &closingOwnerTransport{}
	options := a.options
	options.OwnerTransport = transport
	other, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
	if transport.closed.Load() != 1 {
		t.Fatal("idle owner connections were not closed")
	}
}

type blockedOwnerBody struct{ ctx context.Context } //nolint:containedctx // The test response body must block until the caller's RPC context expires.

func (b blockedOwnerBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (blockedOwnerBody) Close() error               { return nil }

func TestOwnerRenewBoundsBlockedResponseBody(t *testing.T) {
	f := newSourceOwners(t)
	f.two.ownerClient.Transport = ownerTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: blockedOwnerBody{r.Context()}, Request: r}, nil
	})
	finished := make(chan error, 1)
	go func() { _, err := f.two.Renew(context.Background(), 7, "work", 3, f.source.Grant); finished <- err }()
	select {
	case err := <-finished:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("blocked body result: %v", err)
		}
	case <-time.After(3 * sourceCheckTimeout):
		t.Fatal("owner renewal response body outlived its deadline")
	}
	if _, err := f.one.options.Store.Get(t.Context(), f.source.Grant); err != nil {
		t.Fatalf("blocked response deleted grant: %v", err)
	}
}

func TestOwnerRenewRejectsReplayedSignedReply(t *testing.T) {
	f := newSourceOwners(t)
	base := f.two.ownerClient.Transport
	var captured []byte
	f.two.ownerClient.Transport = ownerTransport(func(r *http.Request) (*http.Response, error) {
		if captured != nil {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(captured)), Request: r}, nil
		}
		response, err := base.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		captured, err = io.ReadAll(response.Body)
		_ = response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(captured))
		return response, err
	})
	if _, err := f.two.Renew(t.Context(), 7, "work", 3, f.source.Grant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.two.Renew(t.Context(), 7, "work", 3, f.source.Grant); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("old signed reply authorized a new renewal: %v", err)
	}
	if _, err := f.one.options.Store.Get(t.Context(), f.source.Grant); err != nil {
		t.Fatalf("bad reply deleted grant: %v", err)
	}
}

func (f *sourceOwners) signedRead(t *testing.T, method string) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), method, f.source.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-Silo-Source-Grant", f.source.Grant)
	r.Header.Set("If-Match", f.source.ETag)
	r.Header.Set("Range", "bytes=1-3")
	if err = workeridentity.Sign(r, f.source.Origin, workeridentity.DomainSourceRequest, f.workerKey, 7, false); err != nil {
		t.Fatal(err)
	}
	r.Host = r.URL.Host
	r.URL.Scheme = "http"
	r.URL.Host = f.serverTwo.Listener.Addr().String()
	return r
}

func TestSourceOwnerRelaysRangeAndHeadAndConsumesProofOnce(t *testing.T) {
	f := newSourceOwners(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := f.signedRead(t, method)
		response, err := f.serverTwo.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		want := 206
		if method == http.MethodHead {
			want = 200
		}
		if response.StatusCode != want || method == http.MethodGet && string(data) != "123" {
			t.Fatalf("relay %s HTTP%d %q", method, response.StatusCode, data)
		}
		response, err = f.serverTwo.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 401 {
			t.Fatalf("replayed proof HTTP%d", response.StatusCode)
		}
	}
	if f.foreignProofs.Load() != 0 || f.ownerProofs.Load() != 4 {
		t.Fatalf("proofs owner=%d foreign=%d", f.ownerProofs.Load(), f.foreignProofs.Load())
	}
	encoded, _ := json.Marshal(f.source)
	if bytes.Contains(encoded, []byte(f.serverOne.Listener.Addr().String())) || bytes.Contains(encoded, []byte(f.path)) {
		t.Fatal("owner endpoint/path leaked into source descriptor")
	}
}

func TestDirectRenewUsesSignedOwnerRPC(t *testing.T) {
	f := newSourceOwners(t)
	before, err := f.one.options.Store.Get(t.Context(), f.source.Grant)
	if err != nil {
		t.Fatal(err)
	}
	// Ensure extending the real store changes the observable lease deadline.
	f.one.options.Now = func() time.Time { return time.Now().Add(time.Second) }
	renewed, err := f.two.Renew(t.Context(), 7, "work", 3, f.source.Grant)
	if err != nil || renewed != f.source {
		t.Fatalf("owner renewal=%+v err=%v", renewed, err)
	}
	after, err := f.one.options.Store.Get(t.Context(), f.source.Grant)
	if err != nil || !after.ExpiresAt.After(before.ExpiresAt) {
		t.Fatalf("owner lease not extended: %v", err)
	}
	if f.ownerProofs.Load() != 0 || f.foreignProofs.Load() != 0 {
		t.Fatal("core RPC consumed a worker proof")
	}
}

func TestOwnerRenewRejectsWorkerKeyAndReplay(t *testing.T) {
	f := newSourceOwners(t)
	call := func(key ed25519.PrivateKey) (*http.Request, int) {
		body, _ := json.Marshal(ownerRenewRequest{f.one.options.OwnerID})
		r, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, f.source.URL+"/renew", bytes.NewReader(body))
		r.Header.Set("X-Silo-Source-Grant", f.source.Grant)
		r.Header.Set("If-Match", f.source.ETag)
		r.Header.Set(ownerHeader, f.one.options.OwnerID)
		r.Header.Set(workeridentity.HeaderWorkID, f.source.WorkID)
		r.Header.Set(workeridentity.HeaderGeneration, strconv.FormatUint(f.source.Generation, 10))
		if err := workeridentity.Sign(r, f.source.Origin, ownerRenewDomain, key, 7, false); err != nil {
			t.Fatal(err)
		}
		r.Host = r.URL.Host
		r.URL.Scheme = "http"
		r.URL.Host = f.serverOne.Listener.Addr().String()
		response, err := f.serverOne.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		return r, response.StatusCode
	}
	if _, status := call(f.workerKey); status != 401 {
		t.Fatalf("worker-signed owner RPC HTTP%d", status)
	}
	r, status := call(f.one.options.OwnerKey)
	if status != 200 {
		t.Fatalf("core RPC HTTP%d", status)
	}
	r.Body, _ = r.GetBody()
	response, err := f.serverOne.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("replayed owner RPC HTTP%d", response.StatusCode)
	}
}

func TestOwnerDownOrRestartWithholdsWithoutDeletingGrant(t *testing.T) {
	f := newSourceOwners(t)
	f.one.options.OwnerID = "restarted-owner"
	response, err := f.serverTwo.Client().Do(f.signedRead(t, http.MethodGet))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatalf("restarted owner HTTP%d", response.StatusCode)
	}
	if _, err = f.two.Renew(t.Context(), 7, "work", 3, f.source.Grant); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("restarted owner renewal %v", err)
	}
	f.serverOne.Close()
	response, err = f.serverTwo.Client().Do(f.signedRead(t, http.MethodGet))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatalf("down owner HTTP%d", response.StatusCode)
	}
	if _, err = f.one.options.Store.Get(context.Background(), f.source.Grant); err != nil {
		t.Fatalf("owner outage deleted grant: %v", err)
	}
}
