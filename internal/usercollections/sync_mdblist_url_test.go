package usercollections

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
)

type countingRoundTripper struct {
	hits atomic.Int32
}

func (t *countingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	t.hits.Add(1)
	return nil, errors.New("HTTP client must not be used for a rejected MDBList URL")
}

func TestFetchMDBListEntriesDoesNotDialPrivateHosts(t *testing.T) {
	t.Parallel()

	transport := &countingRoundTripper{}
	svc := NewService(nil, nil, nil, &http.Client{Transport: transport}, slog.New(slog.DiscardHandler))

	_, err := svc.fetchMDBListEntries(context.Background(), "http://127.0.0.1:8096/", 0)
	if !errors.Is(err, collectionutil.ErrMDBListURL) {
		t.Fatalf("fetchMDBListEntries(loopback) = %v, want ErrMDBListURL", err)
	}
	if transport.hits.Load() != 0 {
		t.Fatalf("HTTP client was used %d times for a private URL", transport.hits.Load())
	}

	_, err = svc.fetchMDBListEntries(context.Background(), "http://169.254.169.254/latest/meta-data/", 0)
	if !errors.Is(err, collectionutil.ErrMDBListURL) {
		t.Fatalf("fetchMDBListEntries(link-local) = %v, want ErrMDBListURL", err)
	}
	if transport.hits.Load() != 0 {
		t.Fatalf("HTTP client was used %d times for a private URL", transport.hits.Load())
	}
}
