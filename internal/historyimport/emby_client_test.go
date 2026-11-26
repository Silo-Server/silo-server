package historyimport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/netguard"
)

func newUnthrottledEmbyClient() *EmbyClient {
	client := NewEmbyClient()
	client.limiter = nil
	return client
}

type recordedPaths struct {
	mu    sync.Mutex
	paths []string
}

func (r *recordedPaths) add(path string) {
	r.mu.Lock()
	r.paths = append(r.paths, path)
	r.mu.Unlock()
}

func (r *recordedPaths) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.paths))
	copy(out, r.paths)
	return out
}

func TestEmbyAuthenticateServerUser_RejectsIncompleteJSONWithoutRetry(t *testing.T) {
	t.Parallel()

	var rec recordedPaths
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"User":{"Id":"u1"}}`))
	}))
	defer server.Close()

	_, err := newUnthrottledEmbyClient().AuthenticateServerUser(trustLoopback(t.Context()), server.URL, "alice", "")
	if !errors.Is(err, ErrUnexpectedSourceResponse) {
		t.Fatalf("err = %v, want ErrUnexpectedSourceResponse", err)
	}
	if paths := rec.snapshot(); len(paths) != 1 || paths[0] != "/Users/AuthenticateByName" {
		t.Fatalf("requested paths = %v, want single request without /emby retry", paths)
	}
}

func TestEmbyConnectExchange_RejectsIncompleteJSONWithoutRetry(t *testing.T) {
	t.Parallel()

	var rec recordedPaths
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"LocalUserId":""}`))
	}))
	defer server.Close()

	_, err := newUnthrottledEmbyClient().ConnectExchange(trustLoopback(t.Context()), server.URL, "connect-user", "key")
	if !errors.Is(err, ErrUnexpectedSourceResponse) {
		t.Fatalf("err = %v, want ErrUnexpectedSourceResponse", err)
	}
	if paths := rec.snapshot(); len(paths) != 1 || paths[0] != "/Connect/Exchange" {
		t.Fatalf("requested paths = %v, want single request without /emby retry", paths)
	}
}

func TestEmbyAuthenticateServerUser_HTMLFallbackAndDecodePreservation(t *testing.T) {
	t.Parallel()

	t.Run("falls back to /emby after root HTML landing page", func(t *testing.T) {
		t.Parallel()
		var rec recordedPaths
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.add(r.URL.Path)
			if r.URL.Path == "/Users/AuthenticateByName" {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<html><body>Emby Web</body></html>"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"AccessToken":"tok","User":{"Id":"u1"}}`))
		}))
		defer server.Close()

		auth, err := newUnthrottledEmbyClient().AuthenticateServerUser(trustLoopback(t.Context()), server.URL, "alice", "pw")
		if err != nil {
			t.Fatalf("AuthenticateServerUser: %v", err)
		}
		if auth.BaseURL != server.URL+"/emby" || auth.UserID != "u1" || auth.AccessToken != "tok" {
			t.Fatalf("auth = %+v", auth)
		}
		if paths := rec.snapshot(); len(paths) != 2 {
			t.Fatalf("paths = %v, want 2 attempts", paths)
		}
	})

	t.Run("preserves decode error when /emby returns 404", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/Users/AuthenticateByName" {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<html><body>Not Emby</body></html>"))
				return
			}
			http.NotFound(w, r)
		}))
		defer server.Close()

		_, err := newUnthrottledEmbyClient().AuthenticateServerUser(trustLoopback(t.Context()), server.URL, "alice", "pw")
		if !errors.Is(err, ErrUnexpectedSourceResponse) {
			t.Fatalf("err = %v, want ErrUnexpectedSourceResponse (not fallback 404)", err)
		}
		if UpstreamHTTPStatus(err) != 0 {
			t.Fatalf("UpstreamHTTPStatus = %d, want 0", UpstreamHTTPStatus(err))
		}
	})

	t.Run("surfaces /emby 401 over root HTML", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/Users/AuthenticateByName" {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<html><body>Proxy landing</body></html>"))
				return
			}
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
		}))
		defer server.Close()

		_, err := newUnthrottledEmbyClient().AuthenticateServerUser(trustLoopback(t.Context()), server.URL, "alice", "wrong")
		if got := UpstreamHTTPStatus(err); got != http.StatusUnauthorized {
			t.Fatalf("UpstreamHTTPStatus = %d (err %v), want 401", got, err)
		}
	})
}

func TestEmbyAuthenticateServerUser_DoesNotRetryNonFallbackErrors(t *testing.T) {
	t.Parallel()

	t.Run("401 is not retried", func(t *testing.T) {
		t.Parallel()
		var rec recordedPaths
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.add(r.URL.Path)
			http.Error(w, "denied", http.StatusUnauthorized)
		}))
		defer server.Close()

		_, err := newUnthrottledEmbyClient().AuthenticateServerUser(trustLoopback(t.Context()), server.URL, "alice", "wrong")
		if got := UpstreamHTTPStatus(err); got != http.StatusUnauthorized {
			t.Fatalf("UpstreamHTTPStatus = %d (err %v), want 401", got, err)
		}
		if paths := rec.snapshot(); len(paths) != 1 {
			t.Fatalf("requested paths = %v, want 1", paths)
		}
	})

	t.Run("500 is not retried", func(t *testing.T) {
		t.Parallel()
		var rec recordedPaths
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.add(r.URL.Path)
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer server.Close()

		_, err := newUnthrottledEmbyClient().AuthenticateServerUser(trustLoopback(t.Context()), server.URL, "alice", "pw")
		if got := UpstreamHTTPStatus(err); got != http.StatusInternalServerError {
			t.Fatalf("UpstreamHTTPStatus = %d (err %v), want 500", got, err)
		}
		if paths := rec.snapshot(); len(paths) != 1 {
			t.Fatalf("requested paths = %v, want 1", paths)
		}
	})

	t.Run("canceled context is not retried", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(trustLoopback(t.Context()))
		var rec recordedPaths
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.add(r.URL.Path)
			cancel()
			http.NotFound(w, r)
		}))
		defer server.Close()

		_, err := newUnthrottledEmbyClient().AuthenticateServerUser(ctx, server.URL, "alice", "pw")
		if err == nil {
			t.Fatal("expected error after cancellation")
		}
		if paths := rec.snapshot(); len(paths) != 1 {
			t.Fatalf("requested paths = %v, want 1 after cancellation", paths)
		}
	})

	t.Run("private address refusal is not retried", func(t *testing.T) {
		t.Parallel()
		var hits atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			hits.Add(1)
		}))
		defer server.Close()

		_, err := newUnthrottledEmbyClient().AuthenticateServerUser(t.Context(), server.URL, "alice", "pw")
		if !errors.Is(err, netguard.ErrPrivateDestination) {
			t.Fatalf("err = %v, want ErrPrivateDestination", err)
		}
		if got := hits.Load(); got != 0 {
			t.Fatalf("hits = %d, want 0", got)
		}
	})
}

func TestEmbyAuthenticateServerUser_DroppedConnectionIsReachabilityError(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	}))
	defer server.Close()

	_, err := newUnthrottledEmbyClient().AuthenticateServerUser(trustLoopback(t.Context()), server.URL, "alice", "pw")
	if err == nil {
		t.Fatal("expected error when upstream drops connection")
	}
	if errors.Is(err, ErrUnexpectedSourceResponse) {
		t.Fatalf("transport EOF misclassified as ErrUnexpectedSourceResponse: %v", err)
	}
	if !IsReachabilityError(err) {
		t.Fatalf("IsReachabilityError(%v) = false, want true", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1 (must not retry /emby on dropped connection)", got)
	}
}

func TestEmbyAuthenticateServerUser_MidBodyDisconnectIsReachabilityError(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if hj, ok := w.(http.Hijacker); ok {
			if conn, buf, err := hj.Hijack(); err == nil {
				_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"AccessToken\":")
				_ = buf.Flush()
				_ = conn.Close()
			}
		}
	}))
	defer server.Close()

	_, err := newUnthrottledEmbyClient().AuthenticateServerUser(trustLoopback(t.Context()), server.URL, "alice", "pw")
	if err == nil {
		t.Fatal("expected error when upstream disconnects mid-body")
	}
	if errors.Is(err, ErrUnexpectedSourceResponse) {
		t.Fatalf("mid-body disconnect misclassified as ErrUnexpectedSourceResponse: %v", err)
	}
	if !IsReachabilityError(err) {
		t.Fatalf("IsReachabilityError(%v) = false, want true", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1 (must not retry /emby on mid-body disconnect)", got)
	}
}

func TestEmbyAuthenticateServerUser_FallsBackToEmbyPrefixOn405(t *testing.T) {
	t.Parallel()

	var rec recordedPaths
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r.URL.Path)
		if r.URL.Path == "/Users/AuthenticateByName" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/emby/Users/AuthenticateByName" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"AccessToken":"tok-405","User":{"Id":"u-405"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	auth, err := newUnthrottledEmbyClient().AuthenticateServerUser(trustLoopback(t.Context()), server.URL, "alice", "pw")
	if err != nil {
		t.Fatalf("AuthenticateServerUser returned error: %v", err)
	}
	if auth.BaseURL != server.URL+"/emby" || auth.AccessToken != "tok-405" || auth.UserID != "u-405" {
		t.Fatalf("auth = %+v", auth)
	}
	paths := rec.snapshot()
	if len(paths) != 2 || paths[0] != "/Users/AuthenticateByName" || paths[1] != "/emby/Users/AuthenticateByName" {
		t.Fatalf("requested paths = %v, want root then /emby fallback", paths)
	}
}

type failingReader struct {
	data []byte
	err  error
}

func (f *failingReader) Read(p []byte) (int, error) {
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, f.err
}

func TestDecodeLimitedJSONPreservesValueLimitOverBodyError(t *testing.T) {
	t.Parallel()

	oversized := make([]byte, 0, maxUpstreamResponseValues+16)
	oversized = append(oversized, '[')
	for range maxUpstreamResponseValues + 1 {
		oversized = append(oversized, '1', ',')
	}
	var out any
	_, err := decodeLimitedJSON(&failingReader{data: oversized, err: io.ErrUnexpectedEOF}, &out, int64(len(oversized)+64))
	if !errors.Is(err, ErrUpstreamResponseTooLarge) {
		t.Fatalf("decodeLimitedJSON err = %v, want ErrUpstreamResponseTooLarge", err)
	}
	if IsReachabilityError(err) {
		t.Fatalf("IsReachabilityError(%v) = true, want false for size limit", err)
	}

	_, canceledErr := decodeLimitedJSON(&failingReader{data: []byte(`{"AccessToken":`), err: context.Canceled}, &out, 1024)
	if !errors.Is(canceledErr, context.Canceled) || IsReachabilityError(canceledErr) {
		t.Fatalf("canceled decode err = %v (reachable=%v), want context.Canceled and not reachable", canceledErr, IsReachabilityError(canceledErr))
	}
}

func TestEmbyProviderStopsOnCanceledResumeFetch(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(trustLoopback(t.Context()))
	var mu sync.Mutex
	var requestedFavorites bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("Filters") == "IsFavorite" {
			mu.Lock()
			requestedFavorites = true
			mu.Unlock()
		}
		if r.URL.Path == "/Users/emby-user/Items/Resume" {
			cancel()
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("Filters") == "IsResumable" {
			_, _ = w.Write([]byte(`{"Items":[{"Id":"m1","Name":"Arrival","Type":"Movie","RunTimeTicks":36000000000,"UserData":{"PlaybackPositionTicks":6000000000}}],"TotalRecordCount":1}`))
			return
		}
		_, _ = w.Write([]byte(`{"Items":[],"TotalRecordCount":0}`))
	}))
	defer server.Close()

	provider := NewEmbyProvider(newUnthrottledEmbyClient(), embyLocalAuth{BaseURL: server.URL, UserID: "emby-user", AccessToken: "tok"})
	err := provider.Stream(ctx, nil, func(RecordBatch) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream err = %v, want context.Canceled", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requestedFavorites {
		t.Fatal("provider continued to IsFavorite after canceled Items/Resume")
	}
}
