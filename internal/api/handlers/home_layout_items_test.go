package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func homeRowIDs(rows []sections.SectionWithItems) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// TestFetchHomeRowsWithinKeepsReadyRowsInOrder pins that the layout keeps the
// rows that answered in time, in layout order, and leaves out a row whose
// fetch failed and one still running when the budget runs out. The slow row
// is canceled once the layout stops waiting for it.
func TestFetchHomeRowsWithinKeepsReadyRowsInOrder(t *testing.T) {
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
		return sections.SectionWithItems{ResolvedSection: s, Items: []*models.MediaItem{{ContentID: "movie:" + s.ID}}}, nil
	}

	rows := fetchHomeRowsWithin(t.Context(), resolved, 200*time.Millisecond, fetch)
	if got, want := homeRowIDs(rows), []string{"first", "last"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	select {
	case <-slowCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow row's fetch was not canceled after the budget")
	}
}

// TestFetchHomeRowsWithinRunsRowsInParallel pins that the budget covers all
// rows together: rows that each need most of the budget still all arrive.
func TestFetchHomeRowsWithinRunsRowsInParallel(t *testing.T) {
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
		return sections.SectionWithItems{ResolvedSection: s}, nil
	}
	rows := fetchHomeRowsWithin(t.Context(), resolved, 5*time.Second, fetch)
	if got, want := homeRowIDs(rows), []string{"a", "b", "c", "d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// TestBuildHomeSectionRowsBatchedMatchesSingleRow pins that building several
// Home rows together answers each exactly as building it alone does, which
// is what HomeSectionItems does: the hide-watched filter applies per row and
// the cross-row duplicate filter does not run, so a title in two rows stays
// in both.
func TestBuildHomeSectionRowsBatchedMatchesSingleRow(t *testing.T) {
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

	resolved := []sections.ResolvedSection{
		{ID: "featured", SectionType: sections.SectionRecentlyAdded, Featured: true, ItemLimit: 2},
		{ID: "ordinary", SectionType: sections.SectionRecentlyAdded, ItemLimit: 2},
	}
	movie := func(id string) *models.MediaItem {
		return &models.MediaItem{ContentID: id, Type: "movie", Title: id}
	}
	// fetched answers fresh rows, as the fetcher does, with the widened
	// candidate window a hide-watched fetch asks for.
	fetched := func(ids ...string) []sections.SectionWithItems {
		all := map[string][]*models.MediaItem{
			"featured": {movie("movie:watched"), movie("movie:shared"), movie("movie:extra")},
			"ordinary": {movie("movie:watched"), movie("movie:shared"), movie("movie:other"), movie("movie:extra")},
		}
		var rows []sections.SectionWithItems
		for _, s := range homeSectionsForFetch(resolved, true) {
			for _, id := range ids {
				if s.ID == id {
					rows = append(rows, sections.SectionWithItems{ResolvedSection: s, Items: all[id], TotalCount: len(all[id])})
				}
			}
		}
		return restoreHomeSectionDisplayLimits(rows, resolved)
	}
	viewer := SectionViewer{Access: catalog.AccessFilter{UserID: 1, ProfileID: "profile-1"}}

	batched := handler.buildHomeSectionRows(ctx, fetched("featured", "ordinary"), resolved, true, viewer)
	if len(batched) != 2 {
		t.Fatalf("batched rows = %d, want 2", len(batched))
	}
	for i, s := range resolved {
		single := handler.buildHomeSectionRows(ctx, fetched(s.ID), []sections.ResolvedSection{s}, true, viewer)
		if len(single) != 1 || !reflect.DeepEqual(batched[i], single[0]) {
			t.Fatalf("batched row %q = %+v, want it as built alone %+v", s.ID, batched[i], single)
		}
	}
	cards := func(row SectionView) []string {
		ids := make([]string, 0, len(row.Items))
		for _, item := range row.Items {
			ids = append(ids, item.ContentID)
		}
		return ids
	}
	if got, want := cards(batched[0]), []string{"movie:watched", "movie:shared"}; !reflect.DeepEqual(got, want) {
		t.Errorf("featured row = %v, want %v (featured rows keep watched items)", got, want)
	}
	if got, want := cards(batched[1]), []string{"movie:shared", "movie:other"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ordinary row = %v, want %v (watched item cut, shared title kept)", got, want)
	}
}

// TestHomeLayoutWithItemsMatchesSectionItemsDB pins that the rows the home
// layout carries are the first rows of its layout, each equal to what
// HomeSectionItems answers for it, and that include_items 0 answers the
// layout alone.
func TestHomeLayoutWithItemsMatchesSectionItemsDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()

	store, err := pgstore.NewPostgresProvider(f.pool).ForUser(ctx, f.account)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "owner", Name: "owner"}); err != nil {
		t.Fatal(err)
	}
	var rows []userstore.SectionOverride
	for i := range 2 {
		c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: fmt.Sprintf("Row %d", i), CollectionType: "manual"})
		if err != nil {
			t.Fatal(err)
		}
		for pos, id := range f.ids[1:] {
			if err := store.AddCollectionItem(ctx, c.ID, id, pos); err != nil {
				t.Fatal(err)
			}
		}
		// Ahead of every default and admin row, so both are carried.
		position, limit := -100+i, 20
		rows = append(rows, userstore.SectionOverride{
			ID: fmt.Sprintf("row-%d", i), Scope: "home", Position: &position, ItemLimit: &limit, IsUserAdded: true,
			UserSectionType: string(sections.SectionCollection), UserTitle: c.Name,
			UserConfig: fmt.Sprintf(`{"user_collection_id":%q}`, c.ID),
		})
	}
	if err := store.SaveSectionOverrides(ctx, "owner", "home", "", rows); err != nil {
		t.Fatal(err)
	}

	provider := pagingIntegrationProvider{account: f.account, store: store}
	fetcher := sections.NewFetcher(f.pool)
	fetcher.StoreProvider = provider
	h := NewSectionHandler(sections.NewRepository(f.pool), fetcher)
	h.StoreProvider = provider
	reqCtx := apimw.SetProfileID(apimw.SetClaims(ctx, &auth.Claims{UserID: f.account}), "owner")
	viewer := SectionViewer{Access: catalog.AccessFilter{UserID: f.account}}

	plain, err := h.HomeLayoutWithItems(reqCtx, viewer, 0)
	if err != nil {
		t.Fatal(err)
	}
	if plain.IncludedSections == nil || len(plain.IncludedSections) != 0 {
		t.Fatalf("include_items 0 answered %d rows (nil=%v), want an empty list", len(plain.IncludedSections), plain.IncludedSections == nil)
	}
	legacy, err := h.HomeLayout(reqCtx)
	if err != nil || !reflect.DeepEqual(legacy, plain.Layout) {
		t.Fatalf("HomeLayout = %+v, %v; want the same layout %+v", legacy, err, plain.Layout)
	}

	// A generous budget: this pins equality, not timing.
	withItems, err := h.homeLayoutWithItems(reqCtx, viewer, MaxHomeLayoutIncludedSections, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withItems.Layout, plain.Layout) {
		t.Fatalf("layout with items = %+v, want %+v", withItems.Layout, plain.Layout)
	}
	want := min(MaxHomeLayoutIncludedSections, len(plain.Layout.Sections))
	if len(withItems.IncludedSections) != want {
		t.Fatalf("included rows = %d, want %d", len(withItems.IncludedSections), want)
	}
	sawItems := false
	for i, row := range withItems.IncludedSections {
		if row.ID != plain.Layout.Sections[i].ID {
			t.Fatalf("included row %d = %q, want layout row %q", i, row.ID, plain.Layout.Sections[i].ID)
		}
		single, err := h.HomeSectionItems(reqCtx, row.ID, viewer)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(row, single) {
			t.Fatalf("included row %q = %+v, want the HomeSectionItems answer %+v", row.ID, row, single)
		}
		sawItems = sawItems || len(row.Items) > 0
	}
	if !sawItems {
		t.Fatal("no included row carried items; the fixture rows did not resolve")
	}
}
