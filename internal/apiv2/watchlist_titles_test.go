package apiv2

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/watchlist"
)

// fakeWatchlistTitles is a WatchlistTitleService over a fixed entry list,
// newest first. It records the calls it saw.
type fakeWatchlistTitles struct {
	entries []watchlist.Entry
	known   map[watchlist.TitleKey]*watchlist.Title
	itemID  string

	viewers []handlers.PersonalListViewer
	added   []watchlist.Snapshot
	removed []watchlist.TitleKey
	// onList holds the titles the fake's adds put on the watchlist.
	// removedConcurrently makes an add's entry vanish at once, as a delete
	// of the same title running alongside it would.
	onList              map[watchlist.TitleKey]bool
	removedConcurrently bool
	// readdedConcurrently makes a delete see the title back on the
	// watchlist after its removal, as an add running alongside it would.
	readdedConcurrently bool
}

func (f *fakeWatchlistTitles) ListWatchlistTitlesPage(_ context.Context, viewer handlers.PersonalListViewer, after *watchlist.PageKey, limit int) ([]watchlist.Entry, error) {
	f.viewers = append(f.viewers, viewer)
	out := []watchlist.Entry{}
	for _, e := range f.entries {
		if after != nil {
			k := e.PageKey()
			if k.AddedAt.After(after.AddedAt) || (k.AddedAt.Equal(after.AddedAt) && k.TitleID >= after.TitleID) {
				continue
			}
		}
		if len(out) == limit {
			break
		}
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeWatchlistTitles) FindWatchlistTitle(_ context.Context, mediaType string, tmdbID int) (*watchlist.Title, error) {
	return f.known[watchlist.TitleKey{MediaType: mediaType, TMDBID: tmdbID}], nil
}

func (f *fakeWatchlistTitles) AddWatchlistTitle(_ context.Context, viewer handlers.PersonalListViewer, snap watchlist.Snapshot) (handlers.WatchlistTitleAdded, error) {
	f.viewers = append(f.viewers, viewer)
	f.added = append(f.added, snap)
	if f.itemID == "" && !f.removedConcurrently {
		if f.onList == nil {
			f.onList = map[watchlist.TitleKey]bool{}
		}
		f.onList[watchlist.TitleKey{MediaType: snap.MediaType, TMDBID: snap.TMDBID}] = true
	}
	return handlers.WatchlistTitleAdded{ItemID: f.itemID, AddedAt: fixedTime()}, nil
}

func (f *fakeWatchlistTitles) RemoveWatchlistTitle(_ context.Context, viewer handlers.PersonalListViewer, mediaType string, tmdbID int) (*watchlist.Title, error) {
	f.viewers = append(f.viewers, viewer)
	key := watchlist.TitleKey{MediaType: mediaType, TMDBID: tmdbID}
	f.removed = append(f.removed, key)
	delete(f.onList, key)
	return f.known[key], nil
}

func (f *fakeWatchlistTitles) WatchlistTitleOff(_ context.Context, _ handlers.PersonalListViewer, snap watchlist.Snapshot) (bool, error) {
	key := watchlist.TitleKey{MediaType: snap.MediaType, TMDBID: snap.TMDBID}
	if f.readdedConcurrently {
		return false, nil
	}
	return !f.onList[key], nil
}

func (f *fakeWatchlistTitles) WatchlistMembership(_ context.Context, _ handlers.PersonalListViewer, keys []watchlist.TitleKey, _ []string) (map[watchlist.TitleKey]bool, map[string]bool, error) {
	on := map[watchlist.TitleKey]bool{}
	for _, k := range keys {
		if f.known[k] != nil || f.onList[k] {
			on[k] = true
		}
	}
	return on, map[string]bool{}, nil
}

// fakeWatchlistRequests is a WatchlistRequestService with a fixed ceiling
// and TMDB catalog.
type fakeWatchlistRequests struct {
	disabled bool
	ceiling  string
	details  map[int]*tmdb.MediaDetail

	requested   []mediarequests.WatchlistTitle
	withdrawn   []int
	withdrawErr map[int]error
}

func (f *fakeWatchlistRequests) WatchlistCeiling(context.Context, mediarequests.Viewer) (string, error) {
	if f.disabled {
		return "", mediarequests.ErrRequestsDisabled
	}
	return f.ceiling, nil
}

func (f *fakeWatchlistRequests) WatchlistTitleDetail(_ context.Context, _ mediarequests.Viewer, _ mediarequests.MediaType, tmdbID int) (*tmdb.MediaDetail, error) {
	// As the service does: above the ceiling reads as not found.
	if d := f.details[tmdbID]; d != nil && (f.ceiling == "" || access.RatingAllowed(d.USCertification, f.ceiling)) {
		return d, nil
	}
	return nil, mediarequests.ErrNotFound
}

func (f *fakeWatchlistRequests) RequestFromWatchlist(_ context.Context, _ mediarequests.Viewer, title mediarequests.WatchlistTitle) (mediarequests.RequestState, error) {
	f.requested = append(f.requested, title)
	return mediarequests.RequestState{Status: mediarequests.StatusPending, RequestedByViewer: true}, nil
}

func (f *fakeWatchlistRequests) WithdrawWatchlistRequest(_ context.Context, _ mediarequests.Viewer, _ mediarequests.MediaType, tmdbID int) error {
	f.withdrawn = append(f.withdrawn, tmdbID)
	return f.withdrawErr[tmdbID]
}

func (f *fakeWatchlistRequests) WatchlistRequestStates(_ context.Context, _ mediarequests.Viewer, titles []mediarequests.WatchlistTitle) (map[mediarequests.WatchlistKey]mediarequests.RequestState, error) {
	out := map[mediarequests.WatchlistKey]mediarequests.RequestState{}
	for _, t := range titles {
		out[mediarequests.WatchlistKey{MediaType: t.MediaType, TMDBID: t.TMDBID}] = mediarequests.RequestState{Requestable: true}
	}
	return out, nil
}

func watchlistTitleEntry(id int64, tmdbID int, cert string, added time.Time) watchlist.Entry {
	return watchlist.Entry{
		Title:   watchlist.Title{ID: id, MediaType: "movie", TMDBID: tmdbID, Title: "Title " + strings.Repeat("x", int(id)), Certification: cert, State: watchlist.TitleActive},
		AddedAt: added,
	}
}

func watchlistTitlesDeps(titles *fakeWatchlistTitles, reqs *fakeWatchlistRequests) Dependencies {
	deps := favoritesDeps(&fakePersonalLists{watchlist: watchlistRows()})
	deps.WatchlistTitles = titles
	deps.WatchlistRequests = reqs
	return deps
}

func TestListWatchlistTitlesPagesAndFiltersByCeiling(t *testing.T) {
	base := fixedTime()
	titles := &fakeWatchlistTitles{entries: []watchlist.Entry{
		watchlistTitleEntry(4, 104, "PG", base.Add(3*time.Hour)),
		watchlistTitleEntry(3, 103, "R", base.Add(2*time.Hour)),
		watchlistTitleEntry(2, 102, "PG-13", base.Add(time.Hour)),
		watchlistTitleEntry(1, 101, "", base),
	}}
	h := newTestHandler(t, watchlistTitlesDeps(titles, &fakeWatchlistRequests{ceiling: "PG-13"}))

	var ids []int
	cursor, pages := "", 0
	for {
		path := "/api/v2/watchlist/titles?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		rec := do(t, h, http.MethodGet, path, "", viewerHeaders())
		if rec.Code != http.StatusOK {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		var page WatchlistTitleCollection
		decodeBody(t, rec.Body, &page)
		pages++
		for _, it := range page.Items {
			ids = append(ids, it.TMDBID)
		}
		if cursor = page.Page.NextCursor; cursor == "" {
			break
		}
	}
	// The R title and the uncertified one fail the PG-13 ceiling, and the
	// filtered rows do not end the paging early.
	if pages != 2 || !slices.Equal(ids, []int{104, 102}) {
		t.Fatalf("ids = %v pages = %d", ids, pages)
	}
	if v := titles.viewers[0]; v.UserID != 1 || v.ProfileID != "p-owner" {
		t.Fatalf("viewer = %+v", v)
	}
}

// A cursor is bound to its operation and to the acting account and profile.
func TestListWatchlistTitlesCursorScope(t *testing.T) {
	base := fixedTime()
	titles := &fakeWatchlistTitles{entries: []watchlist.Entry{
		watchlistTitleEntry(2, 102, "PG", base.Add(time.Hour)),
		watchlistTitleEntry(1, 101, "PG", base),
	}}
	h := newTestHandler(t, watchlistTitlesDeps(titles, &fakeWatchlistRequests{}))
	rec := do(t, h, http.MethodGet, "/api/v2/watchlist/titles?limit=1", "", viewerHeaders())
	var page WatchlistTitleCollection
	decodeBody(t, rec.Body, &page)
	if page.Page.NextCursor == "" {
		t.Fatalf("no cursor: %s", rec.Body.String())
	}
	// Another account on the same profile id.
	other := with(bearer(adminToken), "X-Profile-Id", "p-owner")
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watchlist/titles?cursor="+page.Page.NextCursor, "", other), TypeInvalidCursor)

	rec = do(t, h, http.MethodGet, "/api/v2/watchlist?limit=1", "", viewerHeaders())
	foreign := decodeCards(t, rec.Body.String()).Page.NextCursor
	if foreign == "" {
		t.Fatalf("no library watchlist cursor: %s", rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watchlist/titles?cursor="+foreign, "", viewerHeaders()), TypeInvalidCursor)
}

// /watchlist/titles is its own route, not an item id on the library
// watchlist's /watchlist/{item_id}.
func TestWatchlistTitlesRoutePrecedence(t *testing.T) {
	titles := &fakeWatchlistTitles{}
	lists := &fakePersonalLists{watchlist: watchlistRows()}
	deps := favoritesDeps(lists)
	deps.WatchlistTitles, deps.WatchlistRequests = titles, &fakeWatchlistRequests{}
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/watchlist/titles", "", viewerHeaders())
	if rec.Code != http.StatusOK || len(titles.viewers) != 1 {
		t.Fatalf("%d %s (title list calls %d)", rec.Code, rec.Body.String(), len(titles.viewers))
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestAddWatchlistTitle(t *testing.T) {
	titles := &fakeWatchlistTitles{}
	reqs := &fakeWatchlistRequests{ceiling: "PG-13", details: map[int]*tmdb.MediaDetail{
		949: {ID: 949, MediaType: "movie", Title: "Heat", ReleaseDate: "1995-12-15", USCertification: "PG-13"},
		950: {ID: 950, MediaType: "movie", Title: "Casino", ReleaseDate: "1995-11-22", USCertification: "R"},
	}}
	h := newTestHandler(t, watchlistTitlesDeps(titles, reqs))

	rec := do(t, h, http.MethodPut, "/api/v2/watchlist/titles/movie/949", "", viewerHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var entry WatchlistTitleEntry
	decodeBody(t, rec.Body, &entry)
	if entry.TMDBID != 949 || entry.ItemID != "" || entry.Request.Status != string(mediarequests.StatusPending) {
		t.Fatalf("entry = %s", rec.Body.String())
	}
	if len(titles.added) != 1 || titles.added[0].Title != "Heat" || len(reqs.requested) != 1 {
		t.Fatalf("added = %+v requested = %+v", titles.added, reqs.requested)
	}

	// Above the ceiling and unknown to TMDB are both 404, and nothing is saved.
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/watchlist/titles/movie/950", "", viewerHeaders()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/watchlist/titles/movie/951", "", viewerHeaders()), TypeNotFound)
	// A title a watchlist already tracks is checked on its stored rating.
	titles.known = map[watchlist.TitleKey]*watchlist.Title{
		{MediaType: "movie", TMDBID: 952}: {ID: 9, MediaType: "movie", TMDBID: 952, Title: "Stored", Certification: "NC-17"},
	}
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/watchlist/titles/movie/952", "", viewerHeaders()), TypeNotFound)
	if len(titles.added) != 1 || len(reqs.requested) != 1 {
		t.Fatalf("a refused add saved: added = %+v requested = %+v", titles.added, reqs.requested)
	}

	// A title the library has goes to the library watchlist and is not requested.
	titles.itemID = "movie:heat-1995"
	rec = do(t, h, http.MethodPut, "/api/v2/watchlist/titles/movie/949", "", viewerHeaders())
	decodeBody(t, rec.Body, &entry)
	if rec.Code != http.StatusOK || entry.ItemID != "movie:heat-1995" || len(reqs.requested) != 1 {
		t.Fatalf("%d %s requested = %d", rec.Code, rec.Body.String(), len(reqs.requested))
	}
}

// Deleting by a former TMDB ID withdraws the watchlist request under both
// the named ID and the title's current one.
func TestDeleteWatchlistTitleByFormerID(t *testing.T) {
	titles := &fakeWatchlistTitles{known: map[watchlist.TitleKey]*watchlist.Title{
		{MediaType: "movie", TMDBID: 100}: {ID: 1, MediaType: "movie", TMDBID: 200, Title: "Repointed"},
	}}
	reqs := &fakeWatchlistRequests{}
	h := newTestHandler(t, watchlistTitlesDeps(titles, reqs))
	rec := do(t, h, http.MethodDelete, "/api/v2/watchlist/titles/movie/100", "", viewerHeaders())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// Once before the removal and once after it.
	if !slices.Equal(reqs.withdrawn, []int{100, 200, 100, 200}) || len(titles.removed) != 1 {
		t.Fatalf("withdrawn = %v removed = %v", reqs.withdrawn, titles.removed)
	}
	// An absent entry succeeds and withdraws under the named ID only (twice,
	// around the removal).
	reqs.withdrawn = nil
	rec = do(t, h, http.MethodDelete, "/api/v2/watchlist/titles/series/5", "", viewerHeaders())
	if rec.Code != http.StatusNoContent || !slices.Equal(reqs.withdrawn, []int{5, 5}) {
		t.Fatalf("%d %s withdrawn = %v", rec.Code, rec.Body.String(), reqs.withdrawn)
	}
}

// With requests off, all three operations answer 409 capability_disabled
// and touch nothing.
func TestWatchlistTitlesRequestsDisabled(t *testing.T) {
	titles := &fakeWatchlistTitles{}
	reqs := &fakeWatchlistRequests{disabled: true}
	h := newTestHandler(t, watchlistTitlesDeps(titles, reqs))
	for _, op := range []struct{ method, path string }{
		{http.MethodGet, "/api/v2/watchlist/titles"},
		{http.MethodPut, "/api/v2/watchlist/titles/movie/949"},
		{http.MethodDelete, "/api/v2/watchlist/titles/movie/949"},
	} {
		requireProblem(t, do(t, h, op.method, op.path, "", viewerHeaders()), TypeCapabilityDisabled)
	}
	if len(titles.viewers) != 0 || len(reqs.withdrawn) != 0 || len(reqs.requested) != 0 {
		t.Fatalf("disabled operations reached the services: %+v %+v", titles, reqs)
	}
}

// The watchlist title operations answer 409 while requests are off, so the
// status must not advertise them then.
func TestRequestStatusWatchlistTitlesFollowRequestsEnabled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		disabled bool
		want     bool
	}{
		{"requests on", false, true},
		{"requests off", true, false},
	} {
		deps := watchlistTitlesDeps(&fakeWatchlistTitles{}, &fakeWatchlistRequests{})
		deps.RequestLifecycle = &fakeLifecycle{requestsDisabled: tc.disabled}
		rec := do(t, newTestHandler(t, deps), http.MethodGet, Prefix+"/requests/status", "", viewerHeaders())
		var got struct {
			Supported *bool `json:"watchlist_titles_supported"`
		}
		decodeBody(t, rec.Body, &got)
		if rec.Code != http.StatusOK || got.Supported == nil || *got.Supported != tc.want {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
}

func TestWatchlistTitlesValidation(t *testing.T) {
	h := newTestHandler(t, watchlistTitlesDeps(&fakeWatchlistTitles{}, &fakeWatchlistRequests{}))
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/watchlist/titles/person/949", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPut, "/api/v2/watchlist/titles/movie/0", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/watchlist/titles?cursor=garbage", "", viewerHeaders()), TypeInvalidCursor)
}

// Search results carry in_watchlist from the viewer's watchlist titles.
func TestSearchRequestMediaMarksWatchlist(t *testing.T) {
	deps := requestDeps(fixtureRequests())
	rec := do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/requests/search?q=heat", "", requestOwner)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"in_watchlist":false`) {
		t.Fatalf("unwired: %d %s", rec.Code, rec.Body.String())
	}
	deps.WatchlistTitles = &fakeWatchlistTitles{known: map[watchlist.TitleKey]*watchlist.Title{
		{MediaType: "movie", TMDBID: 949}: {ID: 1, MediaType: "movie", TMDBID: 949},
	}}
	deps.WatchlistRequests = &fakeWatchlistRequests{}
	rec = do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/requests/search?q=heat", "", requestOwner)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"in_watchlist":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// The request withdrawal runs under every ID the title has had, before the
// entry goes: removing the last entry drops the title's former IDs, so a
// withdrawal that fails part way must leave the entry for a retry.
func TestDeleteWatchlistTitleWithdrawsFormerIDsBeforeRemoving(t *testing.T) {
	title := &watchlist.Title{ID: 1, MediaType: "movie", TMDBID: 200, FormerTMDBIDs: []int{100, 150}, Title: "Repointed"}
	titles := &fakeWatchlistTitles{known: map[watchlist.TitleKey]*watchlist.Title{{MediaType: "movie", TMDBID: 200}: title}}
	reqs := &fakeWatchlistRequests{withdrawErr: map[int]error{150: errors.New("request store unavailable")}}
	h := newTestHandler(t, watchlistTitlesDeps(titles, reqs))

	rec := do(t, h, http.MethodDelete, "/api/v2/watchlist/titles/movie/200", "", viewerHeaders())
	if rec.Code == http.StatusNoContent {
		t.Fatal("a failed withdrawal answered success")
	}
	if len(titles.removed) != 0 {
		t.Fatalf("removed = %v, want the entry kept for a retry", titles.removed)
	}

	reqs.withdrawErr, reqs.withdrawn = nil, nil
	rec = do(t, h, http.MethodDelete, "/api/v2/watchlist/titles/movie/200", "", viewerHeaders())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !slices.Equal(reqs.withdrawn, []int{200, 100, 150, 200, 100, 150}) || len(titles.removed) != 1 {
		t.Fatalf("withdrawn = %v removed = %v; want every ID withdrawn, then one removal", reqs.withdrawn, titles.removed)
	}
}

// An add whose entry a concurrent delete took before the request landed
// withdraws the request it just made, so no request outlives the entry.
func TestAddWatchlistTitleWithdrawsWhenDeletedConcurrently(t *testing.T) {
	titles := &fakeWatchlistTitles{removedConcurrently: true}
	reqs := &fakeWatchlistRequests{details: map[int]*tmdb.MediaDetail{949: {MediaType: "movie", ID: 949, Title: "Heat", Year: 1995}}}
	h := newTestHandler(t, watchlistTitlesDeps(titles, reqs))
	rec := do(t, h, http.MethodPut, "/api/v2/watchlist/titles/movie/949", "", viewerHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(reqs.requested) != 1 || !slices.Equal(reqs.withdrawn, []int{949}) {
		t.Fatalf("requested = %v withdrawn = %v; want the request withdrawn after the entry vanished", reqs.requested, reqs.withdrawn)
	}

	// With the entry still there, nothing is withdrawn.
	titles.removedConcurrently, reqs.withdrawn, reqs.requested = false, nil, nil
	if rec := do(t, h, http.MethodPut, "/api/v2/watchlist/titles/movie/949", "", viewerHeaders()); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(reqs.withdrawn) != 0 {
		t.Fatalf("withdrawn = %v, want none", reqs.withdrawn)
	}
}

// A delete whose title an overlapping add already put back keeps the re-add's
// request: the second withdrawal runs only while the title is still off the
// watchlist.
func TestDeleteWatchlistTitleKeepsRequestOfConcurrentReadd(t *testing.T) {
	titles := &fakeWatchlistTitles{readdedConcurrently: true, known: map[watchlist.TitleKey]*watchlist.Title{
		{MediaType: "movie", TMDBID: 949}: {ID: 1, MediaType: "movie", TMDBID: 949, Title: "Heat"},
	}}
	reqs := &fakeWatchlistRequests{}
	h := newTestHandler(t, watchlistTitlesDeps(titles, reqs))
	rec := do(t, h, http.MethodDelete, "/api/v2/watchlist/titles/movie/949", "", viewerHeaders())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !slices.Equal(reqs.withdrawn, []int{949}) {
		t.Fatalf("withdrawn = %v, want only the withdrawal before the removal", reqs.withdrawn)
	}
}
