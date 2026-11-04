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
	body           string
	requestQueries []string
}

func (f *fakeMDBListFeed) RoundTrip(req *http.Request) (*http.Response, error) {
	f.requestQueries = append(f.requestQueries, req.URL.RawQuery)
	if f.status != 0 {
		return &http.Response{StatusCode: f.status, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}
	if f.body != "" {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(f.body)), Request: req}, nil
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

// The feed answers a list it will not serve with status 200 and an error
// object instead of entries; the sync message must carry the feed's reason.
func TestFetchMDBListJSONReportsTheFeedError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "private list", body: `{"error": "This List is Private"}`, want: "mdblist: This List is Private"},
		{name: "long reason", body: `{"error": "` + strings.Repeat("x", mdblistFeedErrorMaxRunes+50) + `"}`, want: "mdblist: " + strings.Repeat("x", mdblistFeedErrorMaxRunes)},
		{name: "object without a reason", body: `{"items": []}`, want: "parsing mdblist response: json: cannot unmarshal object into Go value of type []collectionutil.pagingEntry"},
		{name: "not JSON", body: `<!DOCTYPE html>`, want: "parsing mdblist response: invalid character '<' looking for beginning of value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			feed := &fakeMDBListFeed{body: tt.body}
			_, err := FetchMDBListJSON[pagingEntry](context.Background(), &http.Client{Transport: feed}, "https://mdblist.com/lists/example-user/private", 0)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}
