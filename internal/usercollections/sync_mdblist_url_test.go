package usercollections

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/userstore"
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
	svc := NewService(nil, nil, nil, nil, &http.Client{Transport: transport}, slog.New(slog.DiscardHandler))

	_, err := collectionutil.FetchMDBListEntries(context.Background(), svc.httpClient, []string{"http://127.0.0.1:8096/"}, 0, nil)
	if !errors.Is(err, collectionutil.ErrMDBListURL) {
		t.Fatalf("FetchMDBListEntries(loopback) = %v, want ErrMDBListURL", err)
	}
	if transport.hits.Load() != 0 {
		t.Fatalf("HTTP client was used %d times for a private URL", transport.hits.Load())
	}

	_, err = collectionutil.FetchMDBListEntries(context.Background(), svc.httpClient, []string{"http://169.254.169.254/latest/meta-data/"}, 0, nil)
	if !errors.Is(err, collectionutil.ErrMDBListURL) {
		t.Fatalf("FetchMDBListEntries(link-local) = %v, want ErrMDBListURL", err)
	}
	if transport.hits.Load() != 0 {
		t.Fatalf("HTTP client was used %d times for a private URL", transport.hits.Load())
	}
}

type recordingMDBListReader struct {
	userID    int
	profileID string
	listURL   string
	err       error
}

func (r *recordingMDBListReader) ListItems(_ context.Context, userID int, profileID, listURL string, _ int) ([]collectionutil.MDBListEntry, error) {
	r.userID, r.profileID, r.listURL = userID, profileID, listURL
	return nil, r.err
}

// A list the public feed refuses is read with the MDBList connection of the
// collection's owner profile, and a profile without one is told to connect
// MDBList rather than shown the feed's bare refusal.
func TestMatchMDBListReadsARefusedListWithTheOwnersConnection(t *testing.T) {
	t.Parallel()

	feed := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":"This List is Private"}`))}, nil
	})
	svc := NewService(nil, nil, nil, nil, &http.Client{Transport: feed}, slog.New(slog.DiscardHandler))
	reader := &recordingMDBListReader{err: collectionutil.ErrMDBListNoConnection}
	svc.MDBList = reader
	collection := &userstore.Collection{ID: "c1", ProfileID: "viewer", CreatorProfileID: "owner"}

	_, err := svc.matchMDBList(context.Background(), 7, collection, SourceConfig{URL: "https://mdblist.com/lists/alice/picks"})
	if reader.userID != 7 || reader.profileID != "owner" || reader.listURL != "https://mdblist.com/lists/alice/picks" {
		t.Fatalf("reader called with %d %q %q, want account 7, the owner profile and the list page", reader.userID, reader.profileID, reader.listURL)
	}
	if err == nil || !strings.Contains(err.Error(), "This List is Private") || !strings.Contains(err.Error(), "connect MDBList for this profile") {
		t.Fatalf("err = %v, want the feed's refusal and the connect hint", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
