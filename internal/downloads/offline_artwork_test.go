package downloads

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestArtworkUpstreamFailuresAreRetryableAndOmitTheURL(t *testing.T) {
	status := http.StatusOK
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer upstream.Close()
	s := &Service{httpClient: upstream.Client()}
	signed := upstream.URL + "/poster.jpg?X-Amz-Signature=secret"

	// Every upstream error stays not-found, which the frozen v1 route answers
	// with 404; only 429 and 5xx are also worth a retry.
	for _, tc := range []struct {
		status      int
		unavailable bool
	}{
		{http.StatusServiceUnavailable, true},
		{http.StatusTooManyRequests, true},
		{http.StatusBadGateway, true},
		{http.StatusNotFound, false},
		{http.StatusForbidden, false},
	} {
		status = tc.status
		err := s.streamArtwork(context.Background(), httptest.NewRecorder(), nil, signed)
		if !errors.Is(err, ErrAssetNotFound) || errors.Is(err, ErrAssetUnavailable) != tc.unavailable {
			t.Errorf("upstream %d: err = %v, want unavailable %v", tc.status, err, tc.unavailable)
		}
	}

	// A server-relative URL (local artwork storage signs these) or a hostless
	// one is broken, not the store: not worth a retry.
	for _, broken := range []string{"/api/v2/artwork/p.jpg?sig=secret", "http:///poster.jpg"} {
		if err := s.streamArtwork(context.Background(), httptest.NewRecorder(), nil, broken); err == nil ||
			errors.Is(err, ErrAssetUnavailable) || strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: err = %v", broken, err)
		}
	}

	// A client that left isn't the store failing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.streamArtwork(ctx, httptest.NewRecorder(), nil, signed); !errors.Is(err, context.Canceled) || errors.Is(err, ErrAssetUnavailable) {
		t.Errorf("canceled request: err = %v", err)
	}

	// A store that can't be reached: the error keeps the cause, not the URL.
	upstream.Close()
	err := s.streamArtwork(context.Background(), httptest.NewRecorder(), nil, signed)
	if !errors.Is(err, ErrAssetUnavailable) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "poster.jpg") {
		t.Fatalf("unreachable store: err = %v", err)
	}
}

func TestArtworkFailureLogOmitsThePresignedURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Go's client quotes a Location it can't parse in its error.
		w.Header().Set("Location", "/poster%zz.jpg?X-Amz-Signature=redirect-secret")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()
	s := &Service{httpClient: upstream.Client()}
	err := s.streamArtwork(context.Background(), httptest.NewRecorder(), nil, upstream.URL+"/poster.jpg")
	if !errors.Is(err, ErrAssetUnavailable) {
		t.Fatalf("err = %v", err)
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	logArtworkUnavailable(context.Background(), "d1", "poster", err)
	logArtworkUnavailable(context.Background(), "d1", "poster", fmt.Errorf("%w: %w", artworkStatusError(http.StatusBadGateway), ErrAssetUnavailable))
	if out := buf.String(); strings.Contains(out, "redirect-secret") || !strings.Contains(out, `"upstream_status":502`) {
		t.Fatalf("log = %s", out)
	}
}

func TestOfflineDepsKeepTheArtworkTimeout(t *testing.T) {
	s := &Service{}
	s.SetOfflineDeps(nil, nil, nil)
	if s.artworkHTTPClient() != artworkClient {
		t.Fatal("artwork fetches lost their timeout")
	}
	// A deadline over the whole exchange would also count time spent writing
	// to a slow client; only the store's own waits are bounded.
	if artworkClient.Timeout != 0 || artworkClient.Transport.(*http.Transport).ResponseHeaderTimeout == 0 {
		t.Fatal("artwork client timeouts changed")
	}
}

func TestArtworkStoreStoppingMidBodyIsRetryable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Promise bytes, then end the response without sending them.
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	s := &Service{httpClient: upstream.Client()}
	if err := s.streamArtwork(context.Background(), httptest.NewRecorder(), nil, upstream.URL+"/poster.jpg"); !errors.Is(err, ErrAssetUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// brokenClientWriter fails every write, as a connection the client dropped does.
type brokenClientWriter struct{ *httptest.ResponseRecorder }

func (brokenClientWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestArtworkClientWriteFailureIsNotAStoreFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("image"))
	}))
	defer upstream.Close()
	s := &Service{httpClient: upstream.Client()}
	err := s.streamArtwork(context.Background(), brokenClientWriter{httptest.NewRecorder()}, nil, upstream.URL+"/poster.jpg")
	if err == nil || errors.Is(err, ErrAssetUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// shortStall shortens the artwork stall timeout for one test.
func shortStall(t *testing.T) {
	prev := artworkStallTimeout
	artworkStallTimeout = 50 * time.Millisecond
	t.Cleanup(func() { artworkStallTimeout = prev })
}

// slowClientWriter takes longer to write each chunk than the stall timeout.
type slowClientWriter struct{ *httptest.ResponseRecorder }

func (w slowClientWriter) Write(p []byte) (int, error) {
	time.Sleep(80 * time.Millisecond)
	return w.ResponseRecorder.Write(p)
}

func TestArtworkSlowClientDoesNotTimeOutTheStore(t *testing.T) {
	shortStall(t)
	image := bytes.Repeat([]byte("x"), 128<<10)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(image)
	}))
	defer upstream.Close()
	s := &Service{} // the production client
	out := slowClientWriter{httptest.NewRecorder()}
	if err := s.streamArtwork(context.Background(), out, nil, upstream.URL+"/backdrop.jpg"); err != nil || out.Body.Len() != len(image) {
		t.Fatalf("err = %v, wrote %d of %d bytes", err, out.Body.Len(), len(image))
	}
}

func TestArtworkStoreStallingMidBodyIsRetryable(t *testing.T) {
	shortStall(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		_, _ = w.Write([]byte("par"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer upstream.Close()
	s := &Service{httpClient: upstream.Client()}
	start := time.Now()
	err := s.streamArtwork(context.Background(), httptest.NewRecorder(), nil, upstream.URL+"/poster.jpg")
	if !errors.Is(err, ErrAssetUnavailable) || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestArtworkStallBeforeFirstByteDropsImageHeaders(t *testing.T) {
	shortStall(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer upstream.Close()
	s := &Service{httpClient: upstream.Client()}
	rec := httptest.NewRecorder()
	err := s.streamArtwork(context.Background(), rec, nil, upstream.URL+"/poster.jpg")
	if !errors.Is(err, ErrAssetUnavailable) || rec.Header().Get("Content-Length") != "" || rec.Header().Get("Cache-Control") != "" {
		t.Fatalf("err = %v, headers %v", err, rec.Header())
	}
}
