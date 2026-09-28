package downloads

import (
	"context"
	"errors"
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
		if !errors.Is(err, tc.want) {
			t.Errorf("upstream %d: err = %v, want %v", tc.status, err, tc.want)
		}
	}

	// Local artwork storage signs server-relative URLs: not worth a retry.
	if err := s.streamArtwork(context.Background(), httptest.NewRecorder(), nil, "/api/v2/artwork/p.jpg?sig=secret"); err == nil ||
		errors.Is(err, ErrAssetUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Errorf("relative URL: err = %v", err)
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
