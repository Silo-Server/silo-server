package workersource

import (
	"context"
	"crypto/sha256"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

const backpressureMediaKind = "media"

type backpressureFixture struct {
	authority *Authority
	active    atomic.Bool
	server    *httptest.Server
	client    *http.Client
	source    workerwire.Source
	path      string
	finished  chan struct{}
}

func newBackpressureFixture(t *testing.T) *backpressureFixture {
	t.Helper()
	f := &backpressureFixture{finished: make(chan struct{}, 8)}
	f.active.Store(true)
	f.path = filepath.Join(t.TempDir(), "source.mkv")
	file, err := os.Create(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(32 << 20); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.authority.Handler().ServeHTTP(w, r)
		f.finished <- struct{}{}
	}))
	f.server.Config.ConnContext = func(ctx context.Context, connection net.Conn) context.Context {
		if tcp, ok := connection.(*net.TCPConn); ok {
			_ = tcp.SetWriteBuffer(64 << 10)
			_ = tcp.SetNoDelay(true)
		}
		return ctx
	}
	f.server.Start()
	t.Cleanup(f.server.Close)
	f.authority, err = New(Options{
		Store: NewMemoryStore(), Origin: f.server.URL, Limits: Limits{MaxReaders: 1},
		PathApprover: func(_ context.Context, path, kind string) error {
			if path != f.path || kind != backpressureMediaKind {
				return ErrDenied
			}
			return nil
		},
		Active: func(context.Context, int, string, uint64) bool { return f.active.Load() },
		VerifyRequest: func(r *http.Request, node int) error {
			if r.Header.Get("X-Test-Node") != strconv.Itoa(node) {
				return ErrDenied
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.source, err = f.authority.Acquire(t.Context(), 7, "work", 3, f.path, backpressureMediaKind)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		connection, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if tcp, ok := connection.(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(1 << 20)
			_ = tcp.SetNoDelay(true)
		}
		return connection, err
	}}
	t.Cleanup(transport.CloseIdleConnections)
	f.client = &http.Client{Transport: transport, Timeout: 15 * time.Second}
	return f
}

func (f *backpressureFixture) request(t *testing.T, ctx context.Context, method string) *http.Response {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, method, f.source.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-Test-Node", "7")
	r.Header.Set("X-Silo-Source-Grant", f.source.Grant)
	r.Header.Set("If-Match", f.source.ETag)
	response, err := f.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func waitSourceHandler(t *testing.T, f *backpressureFixture) {
	t.Helper()
	select {
	case <-f.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked source reader did not release within two seconds")
	}
	if len(f.authority.readers) != 0 {
		t.Fatal("finished reader retained quota")
	}
}

func TestAuthorizedSourceBackpressureKeepsCompleteBody(t *testing.T) {
	f := newBackpressureFixture(t)
	response := f.request(t, t.Context(), http.MethodGet)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || response.ContentLength != f.source.Size {
		t.Fatalf("source response%d length%d", response.StatusCode, response.ContentLength)
	}
	// Intentionally fill the bounded TCP windows for longer than both the old
	// one-second write limit and the watchdog's renewed safety deadline.
	pause := time.NewTimer(2500 * time.Millisecond)
	defer pause.Stop()
	<-pause.C
	hash := sha256.New()
	count, err := io.Copy(hash, response.Body)
	if err != nil || count != f.source.Size {
		t.Fatalf("authorized paused source truncated: bytes%d/%d err%v", count, f.source.Size, err)
	}
	file, err := os.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	want := sha256.New()
	if _, err := io.Copy(want, file); err != nil {
		t.Fatal(err)
	}
	if string(hash.Sum(nil)) != string(want.Sum(nil)) {
		t.Fatal("paused source bytes changed")
	}
	waitSourceHandler(t, f)
	// A completed watcher must not leave a deadline on a reusable connection.
	var reused atomic.Bool
	ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }})
	head := f.request(t, ctx, http.MethodHead)
	defer func() { _ = head.Body.Close() }()
	if head.StatusCode != http.StatusOK || !reused.Load() {
		t.Fatalf("finished reader poisoned reusable connection: status%d reused%v", head.StatusCode, reused.Load())
	}
}

func TestBlockedSourceWriteRevocationAndCancellationReleaseQuota(t *testing.T) {
	for _, reason := range []string{"disabled_work", "released", "canceled", "mutated"} {
		t.Run(reason, func(t *testing.T) {
			f := newBackpressureFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			response := f.request(t, ctx, http.MethodGet)
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("source response%d", response.StatusCode)
			}
			pause := time.NewTimer(200 * time.Millisecond)
			defer pause.Stop()
			<-pause.C
			// There is no downstream body read: the producer must observe these
			// authority changes even when its bounded socket window is full.
			switch reason {
			case "disabled_work":
				f.active.Store(false)
			case "released":
				if err := f.authority.Release(t.Context(), 7, "work", 3, f.source.Grant); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			case "mutated":
				if err := os.Truncate(f.path, f.source.Size-1); err != nil {
					t.Fatal(err)
				}
			}
			waitSourceHandler(t, f)
			count, err := io.Copy(io.Discard, response.Body)
			if err == nil || count >= f.source.Size {
				t.Fatalf("invalidated paused source completed: bytes%d err%v", count, err)
			}
			if reason == "canceled" {
				head := f.request(t, t.Context(), http.MethodHead)
				defer func() { _ = head.Body.Close() }()
				if head.StatusCode != http.StatusOK {
					t.Fatalf("fetch cancellation revoked a healthy grant: %d", head.StatusCode)
				}
			}
		})
	}
}
