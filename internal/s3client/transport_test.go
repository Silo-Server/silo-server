package s3client

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestS3UploadRejectsMethodChangingRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var redirected atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/landing" {
					redirected.Add(1)
					w.WriteHeader(http.StatusOK)
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Location", "/landing")
				w.WriteHeader(status)
			}))
			defer srv.Close()
			c := NewClient(BucketConfig{Endpoint: srv.URL, Bucket: "b", AccessKey: "k", SecretKey: "s", PathStyle: true})
			if err := c.PutObject(t.Context(), "b", "object", []byte("payload")); err == nil {
				t.Error("upload reported success after redirect discarded its method and body")
			}
			if got := redirected.Load(); got != 0 {
				t.Errorf("followed %d redirects, want zero", got)
			}
		})
	}
}

func TestS3ClientPreservesReplayableRedirects(t *testing.T) {
	for _, status := range []int{307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var uploaded atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if r.URL.Path != "/target" {
					w.Header().Set("Location", "/target")
					w.WriteHeader(status)
					return
				}
				if err != nil || r.Method != http.MethodPut || string(body) != "payload" {
					t.Errorf("redirected upload: method=%s body=%q err=%v", r.Method, body, err)
				}
				uploaded.Store(true)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, srv.URL+"/object", strings.NewReader("payload"))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := sharedHTTPClient().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			if !uploaded.Load() {
				t.Fatal("upload did not reach redirect target")
			}
		})
	}
}

func TestS3ClientStopsReplayableRedirectLoops(t *testing.T) {
	var hops atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		w.Header().Set("Location", "/again")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/object", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := sharedHTTPClient().Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, errTooManyRedirects) {
		t.Fatalf("err = %v, want redirect limit", err)
	}
	if got := hops.Load(); got != maxRedirects {
		t.Fatalf("server saw %d requests, want %d", got, maxRedirects)
	}
}

func TestDeliveryProbeFollowsRedirectsWithSharedPool(t *testing.T) {
	if sharedDeliveryHTTPClient.Transport != sharedHTTPClient().Transport {
		t.Fatal("delivery and storage do not share a pool")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/target" {
			http.Redirect(w, r, "/target", http.StatusMovedPermanently)
			return
		}
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "x")
	}))
	defer srv.Close()
	c := NewClient(BucketConfig{Endpoint: srv.URL, PublicEndpoint: srv.URL, Bucket: "b", PathStyle: true, URLAuth: URLAuthPublic})
	if ok, err := c.ObjectAvailable(t.Context(), "b", "object"); err != nil || !ok {
		t.Fatalf("redirected delivery probe: available=%v err=%v", ok, err)
	}
}

func TestSharedTransportCapsConnections(t *testing.T) {
	tr := sharedTransport()
	if tr.MaxConnsPerHost <= 0 || tr.MaxConnsPerHost != tr.MaxIdleConnsPerHost {
		t.Fatalf("connection caps = %d/%d", tr.MaxConnsPerHost, tr.MaxIdleConnsPerHost)
	}
	a := NewClient(BucketConfig{Role: "a"})
	b := NewClient(BucketConfig{Role: "b"})
	ta := a.s3Client.Options().HTTPClient.(observedHTTPClient).inner.(*http.Client).Transport
	tb := b.s3Client.Options().HTTPClient.(observedHTTPClient).inner.(*http.Client).Transport
	if ta != tb {
		t.Fatal("clients do not share transport")
	}
}

func TestBurstNeverClosesUnusedConnections(t *testing.T) {
	type connectionState struct {
		active bool
		closed bool
		state  http.ConnState
	}
	var mu sync.Mutex
	connections := make(map[net.Conn]*connectionState)
	inHandler := 0
	release := make(chan struct{})
	changed := make(chan struct{}, 1)
	notify := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			mu.Lock()
			ok := cond()
			mu.Unlock()
			if ok {
				return
			}
			select {
			case <-changed:
			case <-deadline:
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}

	// Every handler parks until the test releases the round, so the cap's
	// worth of connections are provably busy at the same time and the rest of
	// the burst has to queue for one instead of dialing.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inHandler++
		gate := release
		mu.Unlock()
		notify()
		<-gate
		w.WriteHeader(http.StatusOK)
	}))
	srv.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		mu.Lock()
		cs := connections[conn]
		if cs == nil {
			cs = &connectionState{}
			connections[conn] = cs
		}
		cs.state = state
		switch state {
		case http.StateActive:
			cs.active = true
		case http.StateClosed, http.StateHijacked:
			cs.closed = true
		}
		mu.Unlock()
		notify()
	}
	srv.Start()
	defer srv.Close()

	c := NewClient(BucketConfig{Endpoint: srv.URL, Bucket: "b", AccessKey: "k", SecretKey: "s", PathStyle: true, Role: "burst"})
	for round := 0; round < 4; round++ {
		mu.Lock()
		inHandler = 0
		release = make(chan struct{})
		gate := release
		mu.Unlock()
		unblock := sync.OnceFunc(func() { close(gate) })
		// Also release handlers if waitFor fails before the normal release.
		defer unblock()

		var wg sync.WaitGroup
		for range 3 * s3MaxConnsPerHost {
			wg.Go(func() {
				if _, err := c.ObjectExists(t.Context(), "b", "object"); err != nil {
					t.Errorf("ObjectExists: %v", err)
				}
			})
		}
		waitFor("the cap's worth of requests to be in flight", func() bool { return inHandler >= s3MaxConnsPerHost })
		mu.Lock()
		open := 0
		for _, cs := range connections {
			if !cs.closed {
				open++
			}
		}
		mu.Unlock()
		// Release before reporting so a failure never leaves handlers parked
		// and the server's shutdown waiting on them.
		unblock()
		wg.Wait()
		if open != s3MaxConnsPerHost {
			t.Fatalf("round %d: %d open connections with %d requests parked, want exactly %d", round, open, s3MaxConnsPerHost, s3MaxConnsPerHost)
		}
		// Let every connection return to the idle pool so the next round races
		// pending dials against a full pool, which is where surplus dials came from.
		waitFor("all connections to go idle", func() bool {
			for _, cs := range connections {
				if !cs.closed && cs.state != http.StateIdle {
					return false
				}
			}
			return true
		})
	}

	mu.Lock()
	defer mu.Unlock()
	if len(connections) > s3MaxConnsPerHost {
		t.Fatalf("saw %d connections, want at most %d", len(connections), s3MaxConnsPerHost)
	}
	for conn, cs := range connections {
		if !cs.active {
			t.Errorf("connection %v was dialed but never carried a request", conn.RemoteAddr())
		}
	}
}
