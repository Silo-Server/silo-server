package usercollections

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
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

	_, err := svc.fetchMDBListEntries(context.Background(), "http://127.0.0.1:8096/")
	if !errors.Is(err, collectionutil.ErrMDBListURL) {
		t.Fatalf("fetchMDBListEntries(loopback) = %v, want ErrMDBListURL", err)
	}
	if transport.hits.Load() != 0 {
		t.Fatalf("HTTP client was used %d times for a private URL", transport.hits.Load())
	}

	_, err = svc.fetchMDBListEntries(context.Background(), "http://169.254.169.254/latest/meta-data/")
	if !errors.Is(err, collectionutil.ErrMDBListURL) {
		t.Fatalf("fetchMDBListEntries(link-local) = %v, want ErrMDBListURL", err)
	}
	if transport.hits.Load() != 0 {
		t.Fatalf("HTTP client was used %d times for a private URL", transport.hits.Load())
	}
}

func TestCanonicalMDBListURLRejectsPrivateHosts(t *testing.T) {
	t.Parallel()

	if _, err := CanonicalMDBListURL("http://10.0.0.1/lists/x/y"); !errors.Is(err, collectionutil.ErrMDBListURL) {
		t.Fatalf("CanonicalMDBListURL(rfc1918) = %v, want ErrMDBListURL", err)
	}
	got, err := CanonicalMDBListURL("https://mdblist.com/lists/example-user/watchlist")
	if err != nil {
		t.Fatalf("CanonicalMDBListURL(valid) = %v", err)
	}
	if !strings.HasSuffix(got, "/json") {
		t.Fatalf("canonical URL = %q, want /json suffix", got)
	}
}
