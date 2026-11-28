package mdblist

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient("secret-key", srv.Client())
	c.baseURL = srv.URL
	return c
}

// A list named by URL resolves to its twins, each twin is read page by page
// (a cursor page, then has_more offset pages), and the result is MDBList's
// rank order across both, with IDs from the ids block or the top-level
// fields older answers carry.
func TestListItemsResolvesTwinsAndMergesPagesInRankOrder(t *testing.T) {
	t.Parallel()

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "secret-key" {
			t.Errorf("%s: apikey missing", r.URL.Path)
		}
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/lists/alice/weekend-picks":
			_, _ = w.Write([]byte(`[{"id":11,"mediatype":"movie"},{"id":12,"mediatype":"show"}]`))
		case r.URL.Path == "/lists/11/items" && q.Get("cursor") == "" && q.Get("offset") == "":
			_, _ = w.Write([]byte(`{"movies":[
				{"id":100,"rank":1,"title":"A","imdb_id":"tt1","ids":{"tmdb":100,"imdb":"tt1"}},
				{"id":"300","rank":3,"title":"C","imdb_id":"tt3","release_year":"2020"}
			],"shows":[],"pagination":{"next_cursor":"c2","has_more":true}}`))
		case r.URL.Path == "/lists/11/items" && q.Get("cursor") == "c2":
			_, _ = w.Write([]byte(`{"movies":[{"id":500,"rank":5,"ids":{"tmdb":500}}],"shows":[],"pagination":{"has_more":false}}`))
		case r.URL.Path == "/lists/12/items" && q.Get("offset") == "":
			_, _ = w.Write([]byte(`{"movies":[],"shows":[{"id":200,"rank":2,"tvdb_id":7,"ids":{"tmdb":200,"tvdb":null}}],"pagination":{"has_more":false}}`))
		default:
			t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
			http.NotFound(w, r)
		}
	})

	entries, err := c.ListItems(context.Background(), collectionutil.MDBListRef{User: "alice", Slug: "weekend-picks"}, 0)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	var ranks []int
	for _, e := range entries {
		ranks = append(ranks, e.Rank)
	}
	if !slices.Equal(ranks, []int{1, 2, 3, 5}) {
		t.Fatalf("ranks = %v, want [1 2 3 5]", ranks)
	}
	if e := entries[1]; e.ItemType() != "series" || e.ID != 200 || e.TVDBID == nil || *e.TVDBID != 7 {
		t.Errorf("show entry = %+v, want a series with TMDB 200 and TVDB 7 from the top-level field", e)
	}
	if e := entries[2]; e.ItemType() != "movie" || e.ID != 300 || e.IMDbID != "tt3" || e.ReleaseYear != 2020 {
		t.Errorf("movie entry = %+v, want TMDB 300 and IMDb tt3 from the top-level fields", e)
	}
}

// A list read for fewer entries than it holds asks for the same page size on
// every page and trims the last one, so a cursor is never sent with a size
// other than the one it was issued for.
func TestListItemsKeepsThePageSizeAcrossCursorPages(t *testing.T) {
	t.Parallel()

	page := func(from, n int, next string) []byte {
		var b strings.Builder
		b.WriteString(`{"movies":[`)
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"id":%d,"rank":%d}`, from+i, from+i)
		}
		fmt.Fprintf(&b, `],"shows":[],"pagination":{"next_cursor":%q,"has_more":%t}}`, next, next != "")
		return []byte(b.String())
	}
	var limits []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/lists/alice/picks" {
			_, _ = w.Write([]byte(`[{"id":11,"mediatype":"movie"}]`))
			return
		}
		limits = append(limits, r.URL.Query().Get("limit"))
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write(page(1, 1000, "c2"))
			return
		}
		_, _ = w.Write(page(1001, 1000, ""))
	})

	entries, err := c.ListItems(context.Background(), collectionutil.MDBListRef{User: "alice", Slug: "picks"}, 1500)
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if !slices.Equal(limits, []string{"1000", "1000"}) || len(entries) != 1500 {
		t.Fatalf("limits = %v, entries = %d; want [1000 1000] and 1500", limits, len(entries))
	}
}

// A refusal is classified so the sync can say what to change, and the key
// never reaches the error.
func TestListItemsClassifiesRefusals(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		status     int
		body       string
		header     map[string]string
		kind       collectionutil.MDBListReadErrorKind
		reason     string
		retryAfter time.Duration
	}{
		"401":                    {status: 401, body: `{"error":"Invalid API key!"}`, kind: collectionutil.MDBListKeyRejected},
		"403 for the list":       {status: 403, body: `{"error":"This list is private"}`, kind: collectionutil.MDBListListUnreadable, reason: "This list is private"},
		"403 blaming the key":    {status: 403, body: `{"error":"Invalid API key"}`, kind: collectionutil.MDBListKeyRejected},
		"404 without a reason":   {status: 404, body: `not found`, kind: collectionutil.MDBListListUnreadable},
		"429":                    {status: 429, body: `{"error":"Daily API limit exceeded!"}`, header: map[string]string{"Retry-After": "120"}, kind: collectionutil.MDBListRateLimited, retryAfter: 2 * time.Minute},
		"200 error object":       {status: 200, body: `{"error":"List not found secret-key"}`, kind: collectionutil.MDBListListUnreadable, reason: "List not found [REDACTED]"},
		"200 empty resolve list": {status: 200, body: `[]`, kind: collectionutil.MDBListListUnreadable},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_, err := c.ListItems(context.Background(), collectionutil.MDBListRef{User: "alice", Slug: "picks"}, 0)
			assertErrorOmitsAPIKey(t, err, "secret-key")
			var readErr *collectionutil.MDBListReadError
			if !errors.As(err, &readErr) {
				t.Fatalf("err = %v, want an MDBListReadError", err)
			}
			if readErr.Kind != tc.kind || (tc.reason != "" && readErr.Reason != tc.reason) || readErr.RetryAfter != tc.retryAfter {
				t.Fatalf("err = %+v, want kind %d, reason %q, retry after %s", readErr, tc.kind, tc.reason, tc.retryAfter)
			}
		})
	}
}

// The picker lists own lists then liked ones, once each: twins share a URL
// and become one entry, an own list without a user name takes the account's
// from /user, and external lists are left out.
func TestAccountListsMergesTwinsAndFillsTheUserName(t *testing.T) {
	t.Parallel()

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lists/user":
			_, _ = w.Write([]byte(`[
				{"id":1,"name":"Picks","slug":"picks","user_name":"","mediatype":"movie","items":3,"private":true},
				{"id":2,"name":"Picks","slug":"picks","user_name":"","mediatype":"show","items":2,"dynamic":true},
				{"id":3,"name":"Imported","slug":"imported","user_name":"alice","type":"external","items":9}
			]`))
		case "/lists/user/liked":
			_, _ = w.Write([]byte(`[
				{"id":1,"name":"Picks","slug":"picks","user_name":"alice","mediatype":"movie","items":3},
				{"id":4,"name":"Horror","slug":"horror","user_name":"bob","mediatype":null,"items":40}
			]`))
		case "/user":
			_, _ = w.Write([]byte(`{"username":"alice"}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})

	lists, err := c.AccountLists(context.Background())
	if err != nil {
		t.Fatalf("AccountLists: %v", err)
	}
	want := []AccountList{
		{Name: "Picks", URL: "https://mdblist.com/lists/alice/picks", MediaTypes: []string{"movie", "show"}, Items: 5, Private: true, Dynamic: true, Owned: true},
		{Name: "Horror", URL: "https://mdblist.com/lists/bob/horror", MediaTypes: []string{"movie", "show"}, Items: 40},
	}
	if !slices.EqualFunc(lists, want, func(a, b AccountList) bool {
		return a.Name == b.Name && a.URL == b.URL && slices.Equal(a.MediaTypes, b.MediaTypes) &&
			a.Items == b.Items && a.Private == b.Private && a.Dynamic == b.Dynamic && a.Owned == b.Owned
	}) {
		t.Fatalf("lists = %+v\nwant %+v", lists, want)
	}
}
