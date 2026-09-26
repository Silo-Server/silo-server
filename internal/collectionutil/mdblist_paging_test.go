package collectionutil

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type pagingEntry struct {
	Rank int `json:"rank"`
}

// fakeMDBListFeed serves a list of total entries through limit/offset the way
// mdblist.com's /json feed does, and records each request's query.
type fakeMDBListFeed struct {
	total          int
	ignorePaging   bool
	status         int
	requestQueries []string
}

func (f *fakeMDBListFeed) RoundTrip(req *http.Request) (*http.Response, error) {
	f.requestQueries = append(f.requestQueries, req.URL.RawQuery)
	if f.status != 0 {
		return &http.Response{StatusCode: f.status, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}
	offset, _ := strconv.Atoi(req.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
	if f.ignorePaging || limit <= 0 {
		offset, limit = 0, 2000
	}
	page := []pagingEntry{}
	for i := offset; i < offset+limit && i < f.total; i++ {
		page = append(page, pagingEntry{Rank: i + 1})
	}
	body, _ := json.Marshal(page)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
}

func fetchFromFeed(t *testing.T, feed *fakeMDBListFeed, maxEntries int) []pagingEntry {
	t.Helper()
	entries, err := FetchMDBListJSON[pagingEntry](context.Background(), &http.Client{Transport: feed}, "https://mdblist.com/lists/example-user/big", maxEntries)
	if err != nil {
		t.Fatalf("FetchMDBListJSON: %v", err)
	}
	for i, entry := range entries {
		if entry.Rank != i+1 {
			t.Fatalf("entry %d rank = %d, want feed order", i, entry.Rank)
		}
	}
	return entries
}

func TestFetchMDBListJSONPagesPastTheFeedDefault(t *testing.T) {
	t.Parallel()

	feed := &fakeMDBListFeed{total: 2500}
	if got := len(fetchFromFeed(t, feed, 0)); got != 2500 {
		t.Fatalf("entries = %d, want 2500", got)
	}
	want := []string{"limit=1000&offset=0", "limit=1000&offset=1000", "limit=1000&offset=2000"}
	if strings.Join(feed.requestQueries, " ") != strings.Join(want, " ") {
		t.Fatalf("requests = %v, want %v", feed.requestQueries, want)
	}
}

func TestFetchMDBListJSONStopsOnEmptyPage(t *testing.T) {
	t.Parallel()

	feed := &fakeMDBListFeed{total: 1000}
	if got := len(fetchFromFeed(t, feed, 0)); got != 1000 {
		t.Fatalf("entries = %d, want 1000", got)
	}
	if len(feed.requestQueries) != 2 {
		t.Fatalf("requests = %v, want a full page then an empty one", feed.requestQueries)
	}
}

func TestFetchMDBListJSONReadsOnlyMaxEntries(t *testing.T) {
	t.Parallel()

	feed := &fakeMDBListFeed{total: 2500}
	if got := len(fetchFromFeed(t, feed, 150)); got != 150 {
		t.Fatalf("entries = %d, want 150", got)
	}
	if len(feed.requestQueries) != 1 || feed.requestQueries[0] != "limit=150&offset=0" {
		t.Fatalf("requests = %v, want one limit=150 page", feed.requestQueries)
	}
}

func TestFetchMDBListJSONBoundsAFeedThatIgnoresPaging(t *testing.T) {
	t.Parallel()

	feed := &fakeMDBListFeed{total: MDBListMaxEntries * 2, ignorePaging: true}
	entries, err := FetchMDBListJSON[pagingEntry](context.Background(), &http.Client{Transport: feed}, "https://mdblist.com/lists/example-user/big", 0)
	if err != nil {
		t.Fatalf("FetchMDBListJSON: %v", err)
	}
	if len(entries) != MDBListMaxEntries {
		t.Fatalf("entries = %d, want the %d ceiling", len(entries), MDBListMaxEntries)
	}
	if len(feed.requestQueries) > MDBListMaxEntries/mdblistJSONPageSize {
		t.Fatalf("made %d requests, want at most %d", len(feed.requestQueries), MDBListMaxEntries/mdblistJSONPageSize)
	}
}

func TestFetchMDBListJSONReportsStatus(t *testing.T) {
	t.Parallel()

	feed := &fakeMDBListFeed{status: http.StatusNotFound}
	_, err := FetchMDBListJSON[pagingEntry](context.Background(), &http.Client{Transport: feed}, "https://mdblist.com/lists/example-user/big", 0)
	if err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("FetchMDBListJSON error = %v, want status 404", err)
	}
}
