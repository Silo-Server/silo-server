package catalog

import (
	"context"
	"errors"
	"net/http"

	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
)

func TestFetchMDBListEntriesDoesNotDialPrivateHosts(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	svc := NewLibraryCollectionService(nil, nil, nil, &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			hits.Add(1)
			return nil, errors.New("HTTP client must not be used for a rejected MDBList URL")
		}),
	})

	_, err := svc.fetchMDBListEntries(context.Background(), "http://127.0.0.1:8096/", 0)
	if !errors.Is(err, collectionutil.ErrMDBListURL) {
		t.Fatalf("fetchMDBListEntries(loopback) = %v, want ErrMDBListURL", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("HTTP client was used %d times for a private URL", hits.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
