package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func homeRowViewIDs(rows []SectionView) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// fetchedHomeRow answers a row with one card named after it, as a fetch would.
func fetchedHomeRow(s sections.ResolvedSection) sections.SectionWithItems {
	return sections.SectionWithItems{ResolvedSection: s, Items: []*models.MediaItem{{ContentID: "movie:" + s.ID, Type: "movie", Title: s.ID}}}
}

// TestIncludedHomeRowsKeepsReadyRowsInOrder pins that the layout keeps the
// rows that were built in time, in layout order, and leaves out a row whose
// fetch failed and one still running when the budget runs out. The slow row
// is canceled once the layout stops waiting for it.
func TestIncludedHomeRowsKeepsReadyRowsInOrder(t *testing.T) {
	resolved := []sections.ResolvedSection{{ID: "slow"}, {ID: "first"}, {ID: "broken"}, {ID: "last"}}
	slowCancelled := make(chan struct{})
	fetch := func(ctx context.Context, s sections.ResolvedSection) (sections.SectionWithItems, error) {
		switch s.ID {
		case "slow":
			<-ctx.Done()
			close(slowCancelled)
			return sections.SectionWithItems{}, ctx.Err()
		case "broken":
			return sections.SectionWithItems{}, errors.New("connection reset by peer")
		}
		return fetchedHomeRow(s), nil
	}

	rows := (&SectionHandler{}).includedHomeRows(t.Context(), resolved, false, SectionViewer{}, 200*time.Millisecond, fetch)
	if got, want := homeRowViewIDs(rows), []string{"first", "last"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	select {
	case <-slowCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow row's fetch was not canceled after the budget")
	}
}

// TestIncludedHomeRowsRunsRowsInParallel pins that the budget covers all
// rows together: rows that each need most of the budget still all arrive.
func TestIncludedHomeRowsRunsRowsInParallel(t *testing.T) {
	resolved := []sections.ResolvedSection{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	started := make(chan struct{}, len(resolved))
	release := make(chan struct{})
	go func() {
		// Release the rows only once every one of them is running.
		for range resolved {
			<-started
		}
		close(release)
	}()
	fetch := func(ctx context.Context, s sections.ResolvedSection) (sections.SectionWithItems, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return sections.SectionWithItems{}, ctx.Err()
		}
		return fetchedHomeRow(s), nil
	}
	rows := (&SectionHandler{}).includedHomeRows(t.Context(), resolved, false, SectionViewer{}, 5*time.Second, fetch)
	if got, want := homeRowViewIDs(rows), []string{"a", "b", "c", "d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// blockingPlayableTargets stands in for a card lookup that hangs on one
// row's items until its caller gives up.
type blockingPlayableTargets struct {
	blockOn  string
	canceled chan struct{}
}

func (b blockingPlayableTargets) ResolvePlayableTargets(ctx context.Context, query catalog.PlayableTargetQuery) (map[string]string, error) {
	if slices.ContainsFunc(query.Items, func(in catalog.PlayableTargetInput) bool { return in.ContentID == b.blockOn }) {
		<-ctx.Done()
		close(b.canceled)
		return nil, ctx.Err()
	}
	return map[string]string{}, nil
}

// TestIncludedHomeRowsBudgetCoversCardLookups pins that the budget covers a
// row's whole build, not only its fetch: a row fetched at once whose card
// lookups hang is left out, the layout answers within the budget with the
// other rows, and the hung lookup is canceled.
func TestIncludedHomeRowsBudgetCoversCardLookups(t *testing.T) {
	resolved := []sections.ResolvedSection{{ID: "stuck"}, {ID: "ready"}}
	lookups := blockingPlayableTargets{blockOn: "movie:stuck", canceled: make(chan struct{})}
	h := &SectionHandler{playableTargets: lookups}
	fetch := func(_ context.Context, s sections.ResolvedSection) (sections.SectionWithItems, error) {
		return fetchedHomeRow(s), nil
	}

	const budget = 200 * time.Millisecond
	start := time.Now()
	rows := h.includedHomeRows(t.Context(), resolved, false, SectionViewer{}, budget, fetch)
	// Generous slack for a loaded CI machine; an unbounded build would hang
	// until the test's own timeout.
	if elapsed := time.Since(start); elapsed > budget+2*time.Second {
		t.Fatalf("layout rows took %s, want about the %s budget", elapsed, budget)
	}
	if got, want := homeRowViewIDs(rows), []string{"ready"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if len(rows[0].Items) != 1 || rows[0].Items[0].ContentID != "movie:ready" {
		t.Fatalf("ready row cards = %+v, want movie:ready", rows[0].Items)
	}
	select {
	case <-lookups.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the stuck row's card lookup was not canceled after the budget")
	}
}

// TestIncludedHomeRowsTurnsPanicIntoMissingRow pins that a row whose build
// panics on its own goroutine is left out instead of crashing the server.
func TestIncludedHomeRowsTurnsPanicIntoMissingRow(t *testing.T) {
	resolved := []sections.ResolvedSection{{ID: "boom"}, {ID: "fine"}}
	fetch := func(_ context.Context, s sections.ResolvedSection) (sections.SectionWithItems, error) {
		if s.ID == "boom" {
			panic("fetch exploded")
		}
		return fetchedHomeRow(s), nil
	}
	rows := (&SectionHandler{}).includedHomeRows(t.Context(), resolved, false, SectionViewer{}, 5*time.Second, fetch)
	if got, want := homeRowViewIDs(rows), []string{"fine"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// TestHomeSectionRowAppliesHideWatchedPerRow pins the shared row builder the
// per-row endpoint and the layout use: the hide-watched filter applies per
// row (featured rows keep watched items) and the cross-row duplicate filter
// does not run, so a title in two rows stays in both.
func TestHomeSectionRowAppliesHideWatchedPerRow(t *testing.T) {
	ctx, handler := hideWatchedHomeRowHandler(t)
	movie := func(id string) *models.MediaItem {
		return &models.MediaItem{ContentID: id, Type: "movie", Title: id}
	}
	viewer := SectionViewer{Access: catalog.AccessFilter{UserID: 1, ProfileID: "profile-1"}}
	for _, tc := range []struct {
		section sections.ResolvedSection
		fetched []*models.MediaItem
		want    []string
	}{
		{
			section: sections.ResolvedSection{ID: "featured", SectionType: sections.SectionRecentlyAdded, Featured: true, ItemLimit: 2},
			fetched: []*models.MediaItem{movie("movie:watched"), movie("movie:shared"), movie("movie:extra")},
			want:    []string{"movie:watched", "movie:shared"},
		},
		{
			section: sections.ResolvedSection{ID: "ordinary", SectionType: sections.SectionRecentlyAdded, ItemLimit: 2},
			fetched: []*models.MediaItem{movie("movie:watched"), movie("movie:shared"), movie("movie:other"), movie("movie:extra")},
			want:    []string{"movie:shared", "movie:other"},
		},
	} {
		// The fetch asks for the widened candidate window.
		fetch := homeSectionsForFetch([]sections.ResolvedSection{tc.section}, true)[0]
		row := handler.homeSectionRow(ctx, tc.section, sections.SectionWithItems{ResolvedSection: fetch, Items: tc.fetched, TotalCount: len(tc.fetched)}, true, viewer)
		if row.ItemLimit != tc.section.ItemLimit {
			t.Errorf("%s item_limit = %d, want the display limit %d", tc.section.ID, row.ItemLimit, tc.section.ItemLimit)
		}
		if got := homeRowCardIDs(row); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s row = %v, want %v", tc.section.ID, got, tc.want)
		}
	}
}

// hideWatchedHomeRowHandler answers a context for profile-1, which hides
// watched items from Home and has watched movie:watched, and a handler that
// reads its store.
func hideWatchedHomeRowHandler(t *testing.T) (context.Context, *SectionHandler) {
	t.Helper()
	ctx := context.Background()
	store := newPlaybackTestStore(t)
	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key: settingskeys.HomeHideWatchedItems, Scope: settingscontract.ScopeProfile, ProfileID: "profile-1",
	}, json.RawMessage(`true`)); err != nil {
		t.Fatalf("enable Home preference: %v", err)
	}
	if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{
		ProfileID: "profile-1", MediaItemID: "movie:watched", Completed: true, Source: userstore.WatchHistorySourceManual,
	}); err != nil {
		t.Fatalf("mark item watched: %v", err)
	}
	handler := &SectionHandler{StoreProvider: testUserStoreProvider{store: store}}
	ctx = apimw.SetProfileID(apimw.SetClaims(ctx, &auth.Claims{UserID: 1}), "profile-1")
	if !handler.homeHidesWatchedItems(ctx) {
		t.Fatal("the profile's Home preference did not resolve to on")
	}
	return ctx, handler
}

func homeRowCardIDs(row SectionView) []string {
	ids := make([]string, 0, len(row.Items))
	for _, item := range row.Items {
		ids = append(ids, item.ContentID)
	}
	return ids
}

// TestHomeRowReportsHasMore pins has_more on the Home row reads: a capped
// read fills the cap after the Not interested and hide-watched filters and
// reports whether the row holds more cards than it answers, and an uncapped
// read never does. The source stands in for sections.Fetcher: it answers the
// first ItemLimit cards (an admin curated list answers all of them) and then
// drops the Not interested card without refilling.
func TestHomeRowReportsHasMore(t *testing.T) {
	ctx, handler := hideWatchedHomeRowHandler(t)
	movies := func(ids ...string) []*models.MediaItem {
		items := make([]*models.MediaItem, 0, len(ids))
		for _, id := range ids {
			items = append(items, &models.MediaItem{ContentID: "movie:" + id, Type: "movie", Title: id})
		}
		return items
	}
	for _, tc := range []struct {
		name        string
		section     sections.ResolvedSection
		hideWatched bool
		cap         int
		source      []*models.MediaItem
		wantCards   []string
		wantMore    bool
	}{
		{name: "row that ends at the cap",
			section: sections.ResolvedSection{ID: "row", SectionType: sections.SectionRecentlyAdded, ItemLimit: 100},
			cap:     3, source: movies("a", "b", "c"),
			wantCards: []string{"movie:a", "movie:b", "movie:c"}},
		{name: "row past the cap",
			section: sections.ResolvedSection{ID: "row", SectionType: sections.SectionRecentlyAdded, ItemLimit: 100},
			cap:     3, source: movies("a", "b", "c", "d"),
			wantCards: []string{"movie:a", "movie:b", "movie:c"}, wantMore: true},
		{name: "curated list longer than its own limit",
			section: sections.ResolvedSection{ID: "picks", SectionType: sections.SectionAdminCuratedList, ItemLimit: 3},
			cap:     20, source: movies("a", "b", "c", "d"),
			wantCards: []string{"movie:a", "movie:b", "movie:c"}, wantMore: true},
		{name: "Not interested card removed",
			section: sections.ResolvedSection{ID: "top", SectionType: sections.SectionCriticallyAcclaimed, ItemLimit: 100},
			cap:     3, source: movies("a", "dismissed", "b", "c", "d"),
			wantCards: []string{"movie:a", "movie:b", "movie:c"}, wantMore: true},
		{name: "watched card removed",
			section:     sections.ResolvedSection{ID: "row", SectionType: sections.SectionRecentlyAdded, ItemLimit: 100},
			hideWatched: true, cap: 3, source: movies("watched", "a", "b", "c", "d"),
			wantCards: []string{"movie:a", "movie:b", "movie:c"}, wantMore: true},
		{name: "card the access filter removes after the fetch limit",
			section: sections.ResolvedSection{ID: "shelf", SectionType: sections.SectionCollection, ItemLimit: 100},
			cap:     3, source: movies("blocked", "a", "b", "c", "d"),
			wantCards: []string{"movie:a", "movie:b", "movie:c"}, wantMore: true},
		{name: "uncapped read",
			section:   sections.ResolvedSection{ID: "row", SectionType: sections.SectionRecentlyAdded, ItemLimit: 3},
			source:    movies("a", "b", "c", "d"),
			wantCards: []string{"movie:a", "movie:b", "movie:c"}},
		{name: "uncapped curated list",
			section:   sections.ResolvedSection{ID: "picks", SectionType: sections.SectionAdminCuratedList, ItemLimit: 3},
			source:    movies("a", "b", "c", "d"),
			wantCards: []string{"movie:a", "movie:b", "movie:c", "movie:d"}},
	} {
		fetch := func(_ context.Context, s sections.ResolvedSection) (sections.SectionWithItems, error) {
			items := tc.source
			if s.SectionType != sections.SectionAdminCuratedList {
				items = items[:min(s.ItemLimit, len(items))]
			}
			if sections.HidesNotInterested(s.SectionType) {
				items = slices.DeleteFunc(slices.Clone(items), func(item *models.MediaItem) bool { return item.ContentID == "movie:dismissed" })
			}
			// Like an unsorted collection rail, which limits its members
			// before the access filter runs.
			items = slices.DeleteFunc(slices.Clone(items), func(item *models.MediaItem) bool { return item.ContentID == "movie:blocked" })
			return sections.SectionWithItems{ResolvedSection: s, Items: items, TotalCount: len(items)}, nil
		}
		viewer := SectionViewer{Access: catalog.AccessFilter{UserID: 1, ProfileID: "profile-1"}, ItemLimitCap: tc.cap}
		rows := handler.includedHomeRows(ctx, []sections.ResolvedSection{tc.section}, tc.hideWatched, viewer, 5*time.Second, fetch)
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows, want 1", tc.name, len(rows))
		}
		wantLimit := tc.section.ItemLimit
		if tc.cap > 0 {
			wantLimit = min(wantLimit, tc.cap)
		}
		if got := homeRowCardIDs(rows[0]); !reflect.DeepEqual(got, tc.wantCards) || rows[0].HasMore != tc.wantMore || rows[0].ItemLimit != wantLimit {
			t.Errorf("%s: cards %v, has_more %v, item_limit %d; want %v, %v, %d", tc.name, got, rows[0].HasMore, rows[0].ItemLimit, tc.wantCards, tc.wantMore, wantLimit)
		}
	}
}
