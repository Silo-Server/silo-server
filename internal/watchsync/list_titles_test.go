package watchsync

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

type watchlistBatchProviderStub struct {
	rows     []RemoteFavorite
	exported []LocalFavorite
}

func (*watchlistBatchProviderStub) Key() string         { return "plugin:4:list" }
func (*watchlistBatchProviderStub) DisplayName() string { return "List" }
func (*watchlistBatchProviderStub) Capabilities() Capabilities {
	return Capabilities{ImportWatchlist: true, ExportWatchlist: true}
}
func (p *watchlistBatchProviderStub) ExportWatchlist(_ context.Context, _ ServerConfig, _ Connection, items []LocalFavorite) (ExportResult, error) {
	p.exported = append(p.exported, items...)
	result := ExportResult{}
	for _, item := range items {
		result.Sent = append(result.Sent, item.MediaItemID)
	}
	return result, nil
}

func (p *watchlistBatchProviderStub) FetchWatchlist(context.Context, ServerConfig, Connection) ([]RemoteFavorite, error) {
	return p.rows, nil
}

// watchlistIncrementalProviderStub sends its rows as an incremental batch.
type watchlistIncrementalProviderStub struct{ watchlistBatchProviderStub }

func (p *watchlistIncrementalProviderStub) FetchWatchlistBatch(context.Context, ServerConfig, Connection) (FavoriteImportBatch, error) {
	return FavoriteImportBatch{Rows: p.rows, Incremental: true}, nil
}

// titlesStub keeps one profile's watchlist entries outside the library by
// TMDB ID, with the TMDB IDs the library carries.
type titlesStub struct {
	library  map[int]bool
	entries  map[int]time.Time
	promoted map[int]bool
	former   map[int][]int
	queued   []WatchlistTitle
	// requested holds the TMDB IDs of the entries added with a request.
	requested []int
	// promotedTVDB marks titles promoted onto a library item that shares
	// only their TVDB ID, which sync knows from the title's row.
	promotedTVDB map[int]bool
	// addErr fails adding the titles with these TMDB IDs, after keeping
	// the entry when addKeeps is set.
	addErr   map[int]error
	addKeeps bool
}

func (s *titlesStub) LibraryHas(_ context.Context, title WatchlistTitle) (bool, error) {
	return s.library[title.TMDBID], nil
}

func (s *titlesStub) AddWatchlistTitle(_ context.Context, _ int, _ string, title WatchlistTitle, addedAt time.Time, request bool) (bool, error) {
	if err := s.addErr[title.TMDBID]; err != nil {
		if !s.addKeeps {
			return false, err
		}
		s.entries[title.TMDBID] = addedAt
		return true, err
	}
	if _, ok := s.entries[title.TMDBID]; ok {
		return false, nil
	}
	s.entries[title.TMDBID] = addedAt
	if request {
		s.requested = append(s.requested, title.TMDBID)
	}
	return true, nil
}

func (s *titlesStub) RemoveWatchlistTitle(_ context.Context, _ int, _ string, title WatchlistTitle) error {
	delete(s.entries, title.TMDBID)
	return nil
}

func (s *titlesStub) RequestQueuedWatchlistTitles(_ context.Context, _ int, _ string, titles []WatchlistTitle) error {
	s.queued = append(s.queued, titles...)
	return nil
}

func (s *titlesStub) ListWatchlistTitles(context.Context, int, string) ([]WatchlistTitle, error) {
	out := make([]WatchlistTitle, 0, len(s.entries))
	for id, at := range s.entries {
		out = append(out, WatchlistTitle{Kind: "movie", TMDBID: id, AddedAt: at})
	}
	return out, nil
}

func (s *titlesStub) WatchlistTitleIDs(_ context.Context, _ int, _ string, titles []WatchlistTitle) (map[WatchlistTitle][]string, error) {
	out := map[WatchlistTitle][]string{}
	for _, title := range titles {
		if _, ok := s.entries[title.TMDBID]; ok {
			for _, id := range append([]int{title.TMDBID}, s.former[title.TMDBID]...) {
				out[title] = append(out[title], "tmdb:"+strconv.Itoa(id))
			}
		}
	}
	return out, nil
}

// PromotedWatchlistItems names a title promoted by TMDB ID "item-<tmdb>" and
// one promoted by TVDB ID "show-<tvdb>".
func (s *titlesStub) PromotedWatchlistItems(_ context.Context, _ int, _ string, title WatchlistTitle, ids []string) ([]string, error) {
	if s.promoted[title.TMDBID] {
		return []string{"item-" + strconv.Itoa(title.TMDBID)}, nil
	}
	for _, id := range ids {
		if tvdb, ok := strings.CutPrefix(id, "tvdb:"); ok {
			if n, _ := strconv.Atoi(tvdb); s.promotedTVDB[n] {
				return []string{"show-" + tvdb}, nil
			}
		}
	}
	return nil, nil
}

// A provider's watchlist title the library doesn't have becomes a watchlist
// entry outside the library, follows the provider's removals, and a local
// removal or a promotion onto the library watchlist retires its row.
// watchlistTitleImportCase is one TestServiceWatchlistImportKeepsTitlesOutsideLibrary case.
type watchlistTitleImportCase struct {
	name        string
	library     map[int]bool
	promoted    map[int]bool
	former      map[int][]int
	entries     map[int]time.Time
	states      []ListItemState
	rows        []RemoteFavorite
	wantEntry   bool
	wantResult  ImportListResult
	wantLocal   bool
	wantRemote  bool
	wantNoState bool
	wantQueued  int
	// synced means an earlier import already kept titles outside the
	// library, so this one requests the titles it adds.
	synced bool
}

// A title the profile removed while it had two rows, one per TMDB ID it was
// listed under, comes back with a listing under a third TMDB ID dated after
// the removal. Neither old row's removal may take the new add off the
// provider.
func TestServiceWatchlistNewerListingSupersedesEveryPendingRow(t *testing.T) {
	store, repo, conn := favoriteImportFixture(t)
	conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
	removedAt := time.Now().Add(-time.Hour)
	for _, id := range []string{"watchlist-title:movie:550", "watchlist-title:movie:600"} {
		repo.listItemStates = append(repo.listItemStates, ListItemState{
			ConnectionID: conn.ID, ListKind: ListKindWatchlist, MediaItemID: id, Kind: "movie",
			ProviderItemKey: "imdb:tt0137523", RemotePresent: true, LastRemovedLocalAt: &removedAt,
		})
	}
	titles := &titlesStub{entries: map[int]time.Time{}}
	service := NewService(repo, NewRegistry()).
		WithMatcher(unmatchedMatcherStub{}).
		WithUserStoreProvider(staticStoreProvider{store: store})
	service.SetWatchlistTitles(titles)
	provider := &watchlistBatchProviderStub{rows: []RemoteFavorite{{
		ProviderItemKey: "imdb:tt0137523", Kind: "movie", Title: "Fight Club",
		IMDbID: "tt0137523", TMDBID: "551", FavoritedAt: time.Now(),
	}}}
	if _, err := service.importList(context.Background(), conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
		t.Fatal(err)
	}
	if _, added := titles.entries[551]; !added {
		t.Fatal("the newer listing wasn't added")
	}
	for _, id := range []string{"watchlist-title:movie:550", "watchlist-title:movie:600"} {
		if old := listItemStateOf(repo, id); !old.LocalPresent && old.RemotePresent {
			t.Fatalf("%s removal still pending: %#v", id, old)
		}
	}
}

// A removal waiting for the provider holds the title in its other form too:
// a removed entry outside the library holds the library item the listing now
// matches, and a removed library item holds the entry outside the library the
// listing becomes once the library loses the item. A listing newer than the
// removal is a new add, and the removal no longer goes out.
func TestServiceWatchlistRemovalHoldsTheTitlesOtherForm(t *testing.T) {
	const titleRow = "watchlist-title:movie:550"
	removedAt := time.Now().Add(-time.Hour)
	for _, tt := range []struct {
		name     string
		pending  ListItemState
		matcher  mediaMatcher
		listedAt time.Time
		wantAdd  bool
	}{
		{
			name:    "title row holds a library match",
			pending: ListItemState{MediaItemID: titleRow, ProviderItemKey: "tmdb:550", TitleIDs: []string{"tmdb:550", "imdb:tt0137523"}},
			matcher: matchedMatcherStub{mediaItemID: testMovieMediaID},
		},
		{
			name:     "newer listing of a library match",
			pending:  ListItemState{MediaItemID: titleRow, ProviderItemKey: "tmdb:550", TitleIDs: []string{"tmdb:550", "imdb:tt0137523"}},
			matcher:  matchedMatcherStub{mediaItemID: testMovieMediaID},
			listedAt: time.Now(), wantAdd: true,
		},
		{
			name:    "library row holds a title outside the library",
			pending: ListItemState{MediaItemID: testMovieMediaID, ProviderItemKey: "imdb:tt0137523"},
			matcher: unmatchedMatcherStub{},
		},
		{
			name:     "newer listing of a title outside the library",
			pending:  ListItemState{MediaItemID: testMovieMediaID, ProviderItemKey: "imdb:tt0137523"},
			matcher:  unmatchedMatcherStub{},
			listedAt: time.Now(), wantAdd: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, repo, conn := favoriteImportFixture(t)
			conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
			pending := tt.pending
			pending.ConnectionID, pending.ListKind, pending.Kind = conn.ID, ListKindWatchlist, "movie"
			pending.RemotePresent, pending.LastRemovedLocalAt = true, &removedAt
			repo.listItemStates = append(repo.listItemStates, pending)
			titles := &titlesStub{entries: map[int]time.Time{}}
			service := NewService(repo, NewRegistry()).
				WithMatcher(tt.matcher).
				WithUserStoreProvider(staticStoreProvider{store: store})
			service.SetWatchlistTitles(titles)
			provider := &watchlistBatchProviderStub{rows: []RemoteFavorite{{
				ProviderItemKey: "imdb:tt0137523", Kind: "movie", Title: "Fight Club",
				IMDbID: "tt0137523", TMDBID: "550", FavoritedAt: tt.listedAt, FavoritedAtGuessed: tt.listedAt.IsZero(),
			}}}
			if _, err := service.importList(context.Background(), conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
				t.Fatal(err)
			}
			onLibraryList, err := store.InWatchlist(context.Background(), conn.ProfileID, testMovieMediaID)
			if err != nil {
				t.Fatal(err)
			}
			_, titleKept := titles.entries[550]
			if added := onLibraryList || titleKept; added != tt.wantAdd {
				t.Fatalf("library item listed %v, title kept %v, want added %v", onLibraryList, titleKept, tt.wantAdd)
			}
			state := listItemStateOf(repo, pending.MediaItemID)
			if stillPending := !state.LocalPresent && state.RemotePresent; stillPending == tt.wantAdd {
				t.Fatalf("pending row = %#v, want its removal pending %v", state, !tt.wantAdd)
			}
		})
	}
}

// An import that fails partway still tracks the entries it already added,
// including one whose add kept the entry and then failed, so the provider
// dropping one before the next run takes it off the watchlist.
func TestServiceWatchlistImportTracksEntriesAddedBeforeAFailure(t *testing.T) {
	for _, tt := range []struct {
		name     string
		addKeeps bool
		failing  int
	}{
		{name: "a later row fails", failing: 680},
		{name: "the add keeps the entry and fails", addKeeps: true, failing: 550},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, repo, conn := favoriteImportFixture(t)
			conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
			titles := &titlesStub{
				entries:  map[int]time.Time{},
				addErr:   map[int]error{tt.failing: errors.New("database unavailable")},
				addKeeps: tt.addKeeps,
			}
			service := NewService(repo, NewRegistry()).
				WithMatcher(unmatchedMatcherStub{}).
				WithUserStoreProvider(staticStoreProvider{store: store})
			service.SetWatchlistTitles(titles)
			provider := &watchlistBatchProviderStub{rows: []RemoteFavorite{
				{ProviderItemKey: "tmdb:550", Kind: "movie", Title: "Fight Club", TMDBID: "550"},
				{ProviderItemKey: "tmdb:680", Kind: "movie", Title: "Pulp Fiction", TMDBID: "680"},
			}}

			if _, err := service.importList(context.Background(), conn, ServerConfig{}, provider, service.watchlistBinding()); err == nil {
				t.Fatal("import succeeded, want the failed add's error")
			}
			if _, ok := titles.entries[550]; !ok {
				t.Fatal("the import didn't add the title")
			}

			// The provider drops the title before a run succeeds.
			titles.addErr = nil
			provider.rows = provider.rows[1:]
			if _, err := service.importList(context.Background(), conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
				t.Fatal(err)
			}
			if _, ok := titles.entries[550]; ok {
				t.Fatal("the title the provider dropped is still on the watchlist")
			}
		})
	}
}

// A title saved before its sync row write failed still counts as imported
// and as a change to the local list.
func TestServiceWatchlistImportCountsATitleWhoseRowWriteFails(t *testing.T) {
	store, repo, conn := favoriteImportFixture(t)
	conn.ImportWatchlistEnabled = true
	repo.upsertListItemErr = errors.New("database unavailable")
	titles := &titlesStub{entries: map[int]time.Time{}}
	service := NewService(repo, NewRegistry()).
		WithMatcher(unmatchedMatcherStub{}).
		WithUserStoreProvider(staticStoreProvider{store: store})
	service.SetWatchlistTitles(titles)
	provider := &watchlistBatchProviderStub{rows: []RemoteFavorite{{ProviderItemKey: "tmdb:550", Kind: "movie", Title: "Fight Club", TMDBID: "550"}}}

	result, err := service.importList(context.Background(), conn, ServerConfig{}, provider, service.watchlistBinding())
	if err == nil {
		t.Fatal("import succeeded, want the row write's error")
	}
	if result.Imported != 1 || !result.changedLocalList() {
		t.Fatalf("result = %#v, want the saved title counted", result)
	}
}

// watchlistRemoverStub records the watchlist removals it is sent.
type watchlistRemoverStub struct {
	watchlistBatchProviderStub
	removed []LocalFavorite
}

func (p *watchlistRemoverStub) RemoveWatchlist(_ context.Context, _ ServerConfig, _ Connection, items []LocalFavorite) (ExportResult, error) {
	p.removed = append(p.removed, items...)
	result := ExportResult{}
	for _, item := range items {
		result.Sent = append(result.Sent, item.MediaItemID)
	}
	return result, nil
}

// A profile removed a title's entry outside the library, then added the
// library item for it to the watchlist before the removal reached the
// provider. The provider lists the title under another key than the entry's
// row (imdb against tmdb), so the export's key guard doesn't hold the
// removal, and an incremental provider leaves the unchanged listing out of
// its batch. The item is a re-add: the removal doesn't go out, and the item
// takes the row's place, so the provider dropping the title still takes it
// off (#4286). Without the item, the removal goes out.
func TestServiceWatchlistReAddedLibraryItemSupersedesATitleRemoval(t *testing.T) {
	const titleRow = "watchlist-title:movie:550"
	for _, tt := range []struct {
		name        string
		reAdded     bool
		wantRemoved bool
	}{
		{name: "library item re-added", reAdded: true},
		{name: "no library item", wantRemoved: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store, repo, conn := favoriteImportFixture(t)
			conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
			removedAt := time.Now().Add(-time.Hour).UTC()
			repo.listItemStates = append(repo.listItemStates, ListItemState{
				ConnectionID: conn.ID, ListKind: ListKindWatchlist, MediaItemID: titleRow, Kind: "movie",
				ProviderItemKey: "tmdb:550", TitleIDs: []string{"tmdb:550", "imdb:tt0137523"},
				RemotePresent: true, LastRemovedLocalAt: &removedAt,
			})
			if tt.reAdded {
				if _, err := store.AddToWatchlistAt(ctx, conn.ProfileID, "item-550", time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			service := NewService(repo, NewRegistry()).
				WithMatcher(matchedMatcherStub{mediaItemID: "item-550"}).
				WithUserStoreProvider(staticStoreProvider{store: store})
			service.SetWatchlistTitles(&titlesStub{entries: map[int]time.Time{}, promoted: map[int]bool{550: tt.reAdded}})
			provider := &watchlistRemoverStub{}
			if _, err := service.removePendingListItems(ctx, conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
				t.Fatal(err)
			}
			if removed := len(provider.removed) > 0; removed != tt.wantRemoved {
				t.Fatalf("removals sent = %v, want the title's removal sent %v", provider.removed, tt.wantRemoved)
			}
			if state := listItemStateOf(repo, titleRow); state.RemotePresent {
				t.Fatalf("title row = %#v, want it settled on the provider side", state)
			}
			if !tt.reAdded {
				return
			}
			// Export is off, so nothing else tracks the listing: the item
			// holds it now.
			dropped := &watchlistIncrementalProviderStub{watchlistBatchProviderStub{rows: []RemoteFavorite{{
				ProviderItemKey: "tmdb:550", Kind: "movie", Removed: true,
			}}}}
			if _, err := service.importList(ctx, conn, ServerConfig{}, dropped, service.watchlistBinding()); err != nil {
				t.Fatal(err)
			}
			if listed, err := store.InWatchlist(ctx, conn.ProfileID, "item-550"); err != nil || listed {
				t.Fatalf("library item listed = %v (%v) after the provider dropped the title, want it removed", listed, err)
			}
		})
	}
}

func listItemStateOf(repo *serviceFakeRepo, mediaItemID string) *ListItemState {
	for i := range repo.listItemStates {
		if repo.listItemStates[i].MediaItemID == mediaItemID {
			return &repo.listItemStates[i]
		}
	}
	return nil
}
