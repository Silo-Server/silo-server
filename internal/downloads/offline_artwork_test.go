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
)

func TestArtworkUpstreamFailuresAreRetryableAndOmitTheURL(t *testing.T) {
	status := http.StatusOK
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer upstream.Close()
	s := &Service{httpClient: upstream.Client()}
	signed := upstream.URL + "/poster.jpg?X-Amz-Signature=secret"

	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusServiceUnavailable, ErrAssetUnavailable},
		{http.StatusTooManyRequests, ErrAssetUnavailable},
		// The frozen v1 route keeps answering an upstream error with 404.
		{http.StatusBadGateway, ErrAssetNotFound},
		{http.StatusNotFound, ErrAssetNotFound},
		{http.StatusForbidden, ErrAssetNotFound},
	} {
		status = tc.status
		err := s.streamArtwork(context.Background(), httptest.NewRecorder(), nil, signed)
		if !errors.Is(err, tc.want) || (tc.status < 429 && errors.Is(err, ErrAssetUnavailable)) {
			t.Errorf("upstream %d: err = %v, want %v", tc.status, err, tc.want)
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
