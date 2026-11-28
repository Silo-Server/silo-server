package collectionutil

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCanonicalMDBListURLRejectsPrivateHosts(t *testing.T) {
	t.Parallel()

	if _, err := CanonicalMDBListURL("http://127.0.0.1:8096/"); !errors.Is(err, ErrMDBListURL) {
		t.Fatalf("CanonicalMDBListURL(loopback) = %v, want ErrMDBListURL", err)
	}

	got, err := CanonicalMDBListURL("https://mdblist.com/lists/example-user/watchlist")
	if err != nil {
		t.Fatalf("CanonicalMDBListURL(valid) = %v", err)
	}
	if got != "https://mdblist.com/lists/example-user/watchlist/json" {
		t.Fatalf("CanonicalMDBListURL = %q", got)
	}
}

// A list URL copied from the browser can carry a query or fragment, and /json
// must end the path or mdblist.com answers with the HTML list page. The query
// is the feed's too (sort=rank orders it), so it stays.
func TestCanonicalMDBListURLPutsJSONOnThePath(t *testing.T) {
	t.Parallel()

	const list = "https://mdblist.com/lists/example-user/watchlist"
	for raw, want := range map[string]string{
		list + "?sort=rank&sortorder=asc": list + "/json?sort=rank&sortorder=asc",
		list + "/?ref=share":              list + "/json?ref=share",
		list + "#top":                     list + "/json",
		list + "/json?sort=imdbpopular":   list + "/json?sort=imdbpopular",
		list + "/JSON":                    list + "/json",
		list + "/json/json/":              list + "/json",
		list + "/json?sort=rank#top":      list + "/json?sort=rank",
		// What earlier builds stored for the first and third URLs; a sync
		// normalizes the stored URL again, so those collections recover.
		list + "?sort=rank&sortorder=asc/json": list + "/json?sort=rank&sortorder=asc",
		list + "#top/json":                     list + "/json",
		list + "/json?sort=rank/json/json":     list + "/json?sort=rank",
		list + "/json?sort=rank #top":          list + "/json?sort=rank",
		list + "/json?sort=rank /json":         list + "/json?sort=rank",
	} {
		got, err := CanonicalMDBListURL(raw)
		if err != nil {
			t.Fatalf("CanonicalMDBListURL(%q) = %v", raw, err)
		}
		if got != want {
			t.Errorf("CanonicalMDBListURL(%q) = %q, want %q", raw, got, want)
		}
		// Sync normalizes the stored URL again, so a second pass must not
		// change it.
		if again, _ := CanonicalMDBListURL(got); again != got {
			t.Errorf("CanonicalMDBListURL(%q) = %q, want it unchanged", got, again)
		}
	}

	for _, raw := range []string{"https://evil.example?x=https://mdblist.com/lists/a/b", "https://mdblist.com#/lists/a/b", "?#"} {
		if got, err := CanonicalMDBListURL(raw); err == nil {
			t.Errorf("CanonicalMDBListURL(%q) = %q, want an error", raw, got)
		}
	}
}

func TestMDBListHTTPClientRejectsPrivateRedirect(t *testing.T) {
	t.Parallel()

	client := MDBListHTTPClient(nil)
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8096/json", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := client.CheckRedirect(req, []*http.Request{req}); !errors.Is(err, ErrMDBListURL) {
		t.Fatalf("CheckRedirect = %v, want ErrMDBListURL", err)
	}
}

// Only a stored URL that names a list by user and slug, or by numeric ID,
// reaches the MDBList API; anything else keeps the feed's refusal.
func TestParseMDBListRef(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]MDBListRef{
		"https://mdblist.com/lists/alice/picks":                {User: "alice", Slug: "picks"},
		"https://www.mdblist.com/lists/alice/picks/json?x=1":   {User: "alice", Slug: "picks"},
		"https://mdblist.com/lists/1176":                       {ID: 1176},
		"https://mdblist.com/lists/1176/json":                  {ID: 1176},
		"https://mdblist.com/lists/alice/external/42":          {},
		"https://mdblist.com/lists/alice":                      {},
		"https://evil.example/lists/alice/picks":               {},
		"https://mdblist.com/lists/0":                          {},
		"https://mdblist.com/lists/alice//picks":               {},
		"https://mdblist.com/lists/user/alice":                 {},
		"https://mdblist.com/lists/alice/picks/extra/segments": {},
	} {
		got, ok := ParseMDBListRef(raw)
		if ok != (want != MDBListRef{}) || got != want {
			t.Errorf("ParseMDBListRef(%q) = %+v, %t; want %+v", raw, got, ok, want)
		}
	}
}

// The public feed answers first, so a public list costs no API quota; only
// the feed's refusal object hands the list to the private reader, and an
// HTTP failure never does.
func TestFetchMDBListEntriesReadsPrivatelyOnlyOnAFeedRefusal(t *testing.T) {
	t.Parallel()

	const listURL = "https://mdblist.com/lists/alice/picks"
	for name, tc := range map[string]struct {
		status      int
		body        string
		wantPrivate bool
		wantErr     bool
	}{
		"public list":    {status: 200, body: `[{"id":1,"rank":1,"mediatype":"movie"}]`},
		"feed refusal":   {status: 200, body: `{"error":"This List is Private"}`, wantPrivate: true},
		"feed HTTP 500":  {status: 500, body: `oops`, wantErr: true},
		"feed not found": {status: 404, body: `{"error":"nope"}`, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{}}, nil
			})}
			var called *MDBListRef
			entries, err := FetchMDBListEntries(context.Background(), client, MDBListURLCandidates(listURL), 0,
				func(ref MDBListRef, feedErr *MDBListFeedError) ([]MDBListEntry, error) {
					called = &ref
					if feedErr.Message != "This List is Private" {
						t.Errorf("feedErr = %q", feedErr.Message)
					}
					return []MDBListEntry{{ID: 9}}, nil
				})
			if (called != nil) != tc.wantPrivate || (err != nil) != tc.wantErr {
				t.Fatalf("private called = %v, err = %v; want called %t, err %t", called, err, tc.wantPrivate, tc.wantErr)
			}
			if tc.wantPrivate && (*called != MDBListRef{User: "alice", Slug: "picks"} || len(entries) != 1 || entries[0].ID != 9) {
				t.Fatalf("private read got ref %+v and returned %+v", *called, entries)
			}
		})
	}
}

// When a collection's two stored URLs have drifted apart, the private read
// names the list the feed refused, whichever candidate that was and however
// the other one failed.
func TestFetchMDBListEntriesReadsTheRefusedCandidate(t *testing.T) {
	t.Parallel()

	const (
		first  = "https://mdblist.com/lists/alice/old-picks"
		second = "https://mdblist.com/lists/alice/picks"
	)
	refusal := response{200, `{"error":"This List is Private"}`}
	for name, answers := range map[string]map[string]response{
		"first fails, second refused": {first: {500, "oops"}, second: refusal},
		"first refused, second 404":   {first: refusal, second: {404, "<html>"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				a := answers["https://mdblist.com"+strings.TrimSuffix(r.URL.Path, "/json")]
				return &http.Response{StatusCode: a.status, Body: io.NopCloser(strings.NewReader(a.body)), Header: http.Header{}}, nil
			})}
			var called []MDBListRef
			_, err := FetchMDBListEntries(context.Background(), client, MDBListURLCandidates(first, second), 0,
				func(ref MDBListRef, _ *MDBListFeedError) ([]MDBListEntry, error) {
					called = append(called, ref)
					return []MDBListEntry{{ID: 9}}, nil
				})
			refused := first
			if answers[second] == refusal {
				refused = second
			}
			want, _ := ParseMDBListRef(refused)
			if err != nil || len(called) != 1 || called[0] != want {
				t.Fatalf("private reads = %+v, err = %v; want one read of %+v", called, err, want)
			}
		})
	}
}

type response struct {
	status int
	body   string
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
