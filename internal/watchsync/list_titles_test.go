package watchsync

import (
	"context"
	"errors"
	"reflect"
	"slices"
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

func watchlistTitleImportCases() []watchlistTitleImportCase {
	const titleRow = "watchlist-title:movie:550"
	listedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	fightClub := RemoteFavorite{
		ProviderItemKey: "movie:imdb:tt0137523", Kind: "movie", Title: "Fight Club", Year: 1999,
		IMDbID: "tt0137523", TMDBID: "550", FavoritedAt: listedAt,
	}
	return []watchlistTitleImportCase{
		{
			name:       "listed title the library lacks",
			rows:       []RemoteFavorite{fightClub},
			wantEntry:  true,
			wantResult: ImportListResult{Found: 1, Imported: 1, added: 1},
			wantLocal:  true, wantRemote: true,
		},
		{
			name:       "listed title the library lacks after the first sync",
			synced:     true,
			rows:       []RemoteFavorite{fightClub},
			wantEntry:  true,
			wantResult: ImportListResult{Found: 1, Imported: 1, added: 1},
			wantLocal:  true, wantRemote: true, wantQueued: 1,
		},
		{
			name:       "listed title already on the watchlist is queued for its request",
			synced:     true,
			entries:    map[int]time.Time{550: listedAt},
			states:     []ListItemState{{MediaItemID: titleRow, Kind: "movie", ProviderItemKey: fightClub.ProviderItemKey, RemotePresent: true, LocalPresent: true}},
			rows:       []RemoteFavorite{fightClub},
			wantEntry:  true,
			wantResult: ImportListResult{Found: 1, Imported: 1},
			wantLocal:  true, wantRemote: true, wantQueued: 1,
		},
		{
			name:        "listed title the library has but did not match",
			library:     map[int]bool{550: true},
			rows:        []RemoteFavorite{fightClub},
			wantResult:  ImportListResult{Found: 1, Unmatched: 1},
			wantNoState: true,
		},
		{
			name:        "listed title without a TMDB ID",
			rows:        []RemoteFavorite{{ProviderItemKey: "movie:imdb:tt0137523", Kind: "movie", IMDbID: "tt0137523"}},
			wantResult:  ImportListResult{Found: 1, Unmatched: 1},
			wantNoState: true,
		},
		{
			name:       "title the provider dropped",
			entries:    map[int]time.Time{550: listedAt},
			states:     []ListItemState{{MediaItemID: titleRow, Kind: "movie", ProviderItemKey: fightClub.ProviderItemKey, RemotePresent: true, LocalPresent: true}},
			wantResult: ImportListResult{Removed: 1},
		},
		{
			name:       "title the provider lists under a former TMDB ID",
			synced:     true,
			former:     map[int][]int{550: {100}},
			entries:    map[int]time.Time{550: listedAt},
			states:     []ListItemState{{MediaItemID: titleRow, Kind: "movie", ProviderItemKey: "tmdb:550", RemotePresent: true, LocalPresent: true}},
			rows:       []RemoteFavorite{{Kind: "movie", Title: "Fight Club", TMDBID: "100", FavoritedAt: listedAt}},
			wantEntry:  true,
			wantResult: ImportListResult{Found: 1, Imported: 1, added: 1},
			wantLocal:  true, wantRemote: true, wantQueued: 2,
		},
		{
			name:       "title removed locally waits for the provider removal",
			states:     []ListItemState{{MediaItemID: titleRow, Kind: "movie", ProviderItemKey: fightClub.ProviderItemKey, RemotePresent: true, LocalPresent: true}},
			rows:       []RemoteFavorite{fightClub},
			wantResult: ImportListResult{Found: 1},
			wantRemote: true,
		},
		{
			name:       "title removed locally after the library got it waits for the provider removal",
			library:    map[int]bool{550: true},
			states:     []ListItemState{{MediaItemID: titleRow, Kind: "movie", ProviderItemKey: fightClub.ProviderItemKey, RemotePresent: true, LocalPresent: true}},
			rows:       []RemoteFavorite{fightClub},
			wantResult: ImportListResult{Found: 1, Unmatched: 1},
			wantRemote: true,
		},
		{
			name:       "title promoted onto the library watchlist",
			library:    map[int]bool{550: true},
			promoted:   map[int]bool{550: true},
			states:     []ListItemState{{MediaItemID: titleRow, Kind: "movie", ProviderItemKey: fightClub.ProviderItemKey, RemotePresent: true, LocalPresent: true}},
			rows:       []RemoteFavorite{fightClub},
			wantResult: ImportListResult{Found: 1, Unmatched: 1},
		},
	}
}

func TestServiceWatchlistImportKeepsTitlesOutsideLibrary(t *testing.T) {
	const titleRow = "watchlist-title:movie:550"
	for _, tt := range watchlistTitleImportCases() {
		t.Run(tt.name, func(t *testing.T) {
			store, repo, conn := favoriteImportFixture(t)
			conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
			if tt.synced {
				repo.watchlistTitlesSince = map[string]time.Time{conn.ID: time.Now().Add(-time.Hour)}
			}
			for _, state := range tt.states {
				state.ConnectionID, state.ListKind, state.CreatedAt = conn.ID, ListKindWatchlist, time.Now()
				repo.listItemStates = append(repo.listItemStates, state)
			}
			titles := &titlesStub{library: tt.library, promoted: tt.promoted, former: tt.former, entries: map[int]time.Time{}}
			for id, at := range tt.entries {
				titles.entries[id] = at
			}
			service := NewService(repo, NewRegistry()).
				WithMatcher(unmatchedMatcherStub{}).
				WithUserStoreProvider(staticStoreProvider{store: store})
			service.SetWatchlistTitles(titles)

			result, err := service.importList(context.Background(), conn, ServerConfig{}, &watchlistBatchProviderStub{rows: tt.rows}, service.watchlistBinding())
			if err != nil {
				t.Fatal(err)
			}
			result.Warnings = nil
			if !reflect.DeepEqual(result, tt.wantResult) {
				t.Fatalf("result = %#v, want %#v", result, tt.wantResult)
			}
			if len(titles.queued) != tt.wantQueued {
				t.Fatalf("queued = %#v, want %d", titles.queued, tt.wantQueued)
			}
			if wantRequested := tt.synced && tt.wantResult.added > 0; (len(titles.requested) > 0) != wantRequested {
				t.Fatalf("requested = %v, want a request %v", titles.requested, wantRequested)
			}
			if _, marked := repo.watchlistTitlesSince[conn.ID]; !marked {
				t.Fatal("the import didn't record the connection's first title sync")
			}
			if _, ok := titles.entries[550]; ok != tt.wantEntry {
				t.Fatalf("entry present = %v, want %v", ok, tt.wantEntry)
			}
			state := listItemStateOf(repo, titleRow)
			if tt.wantNoState {
				if state != nil {
					t.Fatalf("state = %#v, want none", state)
				}
				return
			}
			if state == nil || state.LocalPresent != tt.wantLocal || state.RemotePresent != tt.wantRemote {
				t.Fatalf("state = %#v, want local %v remote %v", state, tt.wantLocal, tt.wantRemote)
			}
		})
	}
}

// The first import that keeps titles outside the library, on a new
// connection or the first sync after upgrading, keeps the provider's backlog
// without requesting it; titles the provider lists later are requested.
func TestServiceWatchlistImportRequestsOnlyTitlesAfterFirstSync(t *testing.T) {
	store, repo, conn := favoriteImportFixture(t)
	conn.ImportWatchlistEnabled = true
	titles := &titlesStub{entries: map[int]time.Time{}}
	service := NewService(repo, NewRegistry()).
		WithMatcher(unmatchedMatcherStub{}).
		WithUserStoreProvider(staticStoreProvider{store: store})
	service.SetWatchlistTitles(titles)
	backlog := RemoteFavorite{ProviderItemKey: "tmdb:550", Kind: "movie", Title: "Fight Club", TMDBID: "550"}
	provider := &watchlistBatchProviderStub{rows: []RemoteFavorite{backlog}}

	if _, err := service.importList(context.Background(), conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
		t.Fatal(err)
	}
	if len(titles.requested) != 0 || len(titles.queued) != 0 {
		t.Fatalf("first sync requested %v, queued %v, want the backlog kept without requests", titles.requested, titles.queued)
	}

	provider.rows = append(provider.rows, RemoteFavorite{ProviderItemKey: "tmdb:680", Kind: "movie", Title: "Pulp Fiction", TMDBID: "680"})
	if _, err := service.importList(context.Background(), conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(titles.requested, []int{680}) {
		t.Fatalf("requested = %v, want only the title listed after the first sync", titles.requested)
	}
	if len(titles.queued) != 1 || titles.queued[0].TMDBID != 680 {
		t.Fatalf("queued = %#v, want only the title listed after the first sync", titles.queued)
	}
}

// A series promoted onto a library show that shares only its TVDB ID is
// still on the watchlist, so its entry leaving is no removal for the
// provider.
func TestServiceWatchlistTitlePromotedByTVDBIsNoRemoval(t *testing.T) {
	store, repo, conn := favoriteImportFixture(t)
	conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
	titles := &titlesStub{entries: map[int]time.Time{}}
	service := NewService(repo, NewRegistry()).
		WithMatcher(unmatchedMatcherStub{}).
		WithUserStoreProvider(staticStoreProvider{store: store})
	service.SetWatchlistTitles(titles)
	provider := &watchlistBatchProviderStub{rows: []RemoteFavorite{
		{ProviderItemKey: "tmdb:1399", Kind: "series", Title: "Show", TMDBID: "1399", TVDBID: "121361"},
	}}
	if _, err := service.importList(context.Background(), conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
		t.Fatal(err)
	}

	// Promotion moves the entry onto the library show.
	delete(titles.entries, 1399)
	titles.promotedTVDB = map[int]bool{121361: true}
	if _, err := service.importList(context.Background(), conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
		t.Fatal(err)
	}
	if state := listItemStateOf(repo, "watchlist-title:series:1399"); state == nil || (!state.LocalPresent && state.RemotePresent) {
		t.Fatalf("state = %#v, want no removal pending for the provider", state)
	}
}

// A title removed in Silo stays off the watchlist when the provider, before
// the removal reaches it, lists the title under a new TMDB ID. A listing newer
// than the removal is a new add, and the removal no longer goes out.
func TestServiceWatchlistTitleRemovalHoldsAcrossATMDBIDChange(t *testing.T) {
	for _, tt := range []struct {
		name     string
		listedAt time.Time
		wantAdd  bool
	}{
		{name: "older listing"},
		{name: "newer listing", listedAt: time.Now().Add(time.Hour), wantAdd: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, repo, conn := favoriteImportFixture(t)
			conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
			repo.watchlistTitlesSince = map[string]time.Time{conn.ID: time.Now().Add(-time.Hour)}
			titles := &titlesStub{entries: map[int]time.Time{}}
			service := NewService(repo, NewRegistry()).
				WithMatcher(unmatchedMatcherStub{}).
				WithUserStoreProvider(staticStoreProvider{store: store})
			service.SetWatchlistTitles(titles)
			listing := RemoteFavorite{ProviderItemKey: "imdb:tt0137523", Kind: "movie", Title: "Fight Club", IMDbID: "tt0137523", TMDBID: "550"}
			provider := &watchlistBatchProviderStub{rows: []RemoteFavorite{listing}}
			if _, err := service.importList(context.Background(), conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
				t.Fatal(err)
			}

			// The profile removes the title, and the provider moves it to a
			// new TMDB ID.
			delete(titles.entries, 550)
			titles.requested = nil
			listing.TMDBID, listing.FavoritedAt = "551", tt.listedAt
			provider.rows = []RemoteFavorite{listing}
			if _, err := service.importList(context.Background(), conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
				t.Fatal(err)
			}
			if _, added := titles.entries[551]; added != tt.wantAdd || len(titles.requested) > 0 != tt.wantAdd {
				t.Fatalf("entries = %v, requested = %v, want added %v", titles.entries, titles.requested, tt.wantAdd)
			}
			old := listItemStateOf(repo, "watchlist-title:movie:550")
			if pending := old != nil && !old.LocalPresent && old.RemotePresent; pending == tt.wantAdd {
				t.Fatalf("old row = %#v, want its removal pending %v", old, !tt.wantAdd)
			}
		})
	}
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

// A complete read that lists a removed title under a new TMDB ID still lists
// it: the removal waits for the provider instead of being taken as done. On
// either kind of read it goes out under the key the provider lists the title
// by now.
func TestServiceWatchlistTitleRemovalWaitsForAListingUnderANewTMDBID(t *testing.T) {
	listing := RemoteFavorite{
		ProviderItemKey: "tmdb:551", Kind: "movie", Title: "Fight Club", IMDbID: "tt0137523", TMDBID: "551",
	}
	for _, tt := range []struct {
		name     string
		provider Provider
	}{
		{"complete read", &watchlistBatchProviderStub{rows: []RemoteFavorite{listing}}},
		{"incremental read", &watchlistIncrementalProviderStub{watchlistBatchProviderStub{rows: []RemoteFavorite{listing}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, repo, conn := favoriteImportFixture(t)
			conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
			removedAt := time.Now().Add(-time.Hour)
			const row = "watchlist-title:movie:550"
			repo.listItemStates = append(repo.listItemStates, ListItemState{
				ConnectionID: conn.ID, ListKind: ListKindWatchlist, MediaItemID: row, Kind: "movie",
				ProviderItemKey: "tmdb:550", RemotePresent: true, LastRemovedLocalAt: &removedAt,
				TitleIDs: []string{"imdb:tt0137523", "tmdb:550"},
			})
			titles := &titlesStub{entries: map[int]time.Time{}}
			service := NewService(repo, NewRegistry()).
				WithMatcher(unmatchedMatcherStub{}).
				WithUserStoreProvider(staticStoreProvider{store: store})
			service.SetWatchlistTitles(titles)
			if _, err := service.importList(context.Background(), conn, ServerConfig{}, tt.provider, service.watchlistBinding()); err != nil {
				t.Fatal(err)
			}
			if state := listItemStateOf(repo, row); state.LocalPresent || !state.RemotePresent || state.ProviderItemKey != "tmdb:551" {
				t.Fatalf("state = %#v, want the removal still pending under tmdb:551", state)
			}
			if len(titles.entries) != 0 {
				t.Fatalf("entries = %v, want the removed title kept off", titles.entries)
			}
		})
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

// A library item already on the watchlist isn't held behind its title's
// earlier removal: the import still tracks it, so its row keeps holding a
// removal under the same key from taking the listing off the provider.
func TestServiceWatchlistTitleRemovalLeavesAListedLibraryItemTracked(t *testing.T) {
	store, repo, conn := favoriteImportFixture(t)
	conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
	removedAt := time.Now().Add(-time.Hour)
	repo.listItemStates = append(repo.listItemStates, ListItemState{
		ConnectionID: conn.ID, ListKind: ListKindWatchlist, MediaItemID: "watchlist-title:movie:550", Kind: "movie",
		ProviderItemKey: "imdb:tt0137523", RemotePresent: true, LastRemovedLocalAt: &removedAt,
	})
	if _, err := store.AddToWatchlist(context.Background(), conn.ProfileID, testMovieMediaID); err != nil {
		t.Fatal(err)
	}
	service := NewService(repo, NewRegistry()).
		WithMatcher(matchedMatcherStub{mediaItemID: testMovieMediaID}).
		WithUserStoreProvider(staticStoreProvider{store: store})
	service.SetWatchlistTitles(&titlesStub{entries: map[int]time.Time{}})
	provider := &watchlistBatchProviderStub{rows: []RemoteFavorite{{
		ProviderItemKey: "imdb:tt0137523", Kind: "movie", Title: "Fight Club",
		IMDbID: "tt0137523", TMDBID: "550", FavoritedAtGuessed: true,
	}}}
	if _, err := service.importList(context.Background(), conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
		t.Fatal(err)
	}
	if state := listItemStateOf(repo, testMovieMediaID); state == nil || !state.LocalPresent || !state.RemotePresent {
		t.Fatalf("library item row = %#v, want it tracked on both sides", state)
	}
}

// Two title rows for one series, under two TMDB IDs, promote to one library
// show. The show's row takes the provider key of the row the provider lists,
// whichever row comes first.
func TestServiceWatchlistPromotedItemTakesTheListedRowsKey(t *testing.T) {
	store, repo, conn := favoriteImportFixture(t)
	conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
	for _, row := range []struct {
		id, key string
		listed  bool
	}{
		{"watchlist-title:series:1399", "tmdb:1399", false},
		{"watchlist-title:series:1400", "imdb:tt0944947", true},
	} {
		repo.listItemStates = append(repo.listItemStates, ListItemState{
			ConnectionID: conn.ID, ListKind: ListKindWatchlist, MediaItemID: row.id, Kind: "series",
			ProviderItemKey: row.key, LocalPresent: true, RemotePresent: row.listed,
			TitleIDs: []string{"tvdb:121361"},
		})
	}
	titles := &titlesStub{entries: map[int]time.Time{}, promotedTVDB: map[int]bool{121361: true}}
	service := NewService(repo, NewRegistry()).WithUserStoreProvider(staticStoreProvider{store: store})
	service.SetWatchlistTitles(titles)

	if err := service.reconcileLocalListTitles(context.Background(), conn, service.watchlistBinding()); err != nil {
		t.Fatal(err)
	}
	if show := listItemStateOf(repo, "show-121361"); show == nil || show.ProviderItemKey != "imdb:tt0944947" || !show.RemotePresent {
		t.Fatalf("show row = %#v, want the listed row's key", show)
	}
}

// Both title rows of a series are listed when promotion hands one of them to
// the library show; the provider then lists only the other one, which matches
// no library item. A complete read still finds the title, so the show stays
// and takes the key the provider lists it by, where a removal reaches it.
func TestServiceWatchlistPromotedItemStaysWhileTheTitleIsListedUnderAnotherKey(t *testing.T) {
	ctx := context.Background()
	store, repo, conn := favoriteImportFixture(t)
	conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
	for _, row := range []struct{ id, key string }{
		{"watchlist-title:series:1399", "tmdb:1399"},
		{"watchlist-title:series:1400", "tmdb:1400"},
	} {
		repo.listItemStates = append(repo.listItemStates, ListItemState{
			ConnectionID: conn.ID, ListKind: ListKindWatchlist, MediaItemID: row.id, Kind: "series",
			ProviderItemKey: row.key, LocalPresent: true, RemotePresent: true, TitleIDs: []string{"tvdb:121361"},
		})
	}
	if _, err := store.AddToWatchlistAt(ctx, conn.ProfileID, "show-121361", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	titles := &titlesStub{entries: map[int]time.Time{}, library: map[int]bool{1400: true}, promotedTVDB: map[int]bool{121361: true}}
	service := NewService(repo, NewRegistry()).
		WithMatcher(unmatchedMatcherStub{}).
		WithUserStoreProvider(staticStoreProvider{store: store})
	service.SetWatchlistTitles(titles)
	provider := &watchlistBatchProviderStub{rows: []RemoteFavorite{{
		ProviderItemKey: "tmdb:1400", Kind: "series", Title: "Show", TMDBID: "1400", TVDBID: "121361",
	}}}
	if _, err := service.importList(ctx, conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
		t.Fatal(err)
	}
	if entry, err := store.GetWatchlistEntry(ctx, conn.ProfileID, "show-121361"); err != nil || entry == nil {
		t.Fatalf("library watchlist entry = %v, %v; want the show kept", entry, err)
	}
	if show := listItemStateOf(repo, "show-121361"); show == nil || show.ProviderItemKey != "tmdb:1400" || !show.RemotePresent {
		t.Fatalf("show row = %#v, want it listed under tmdb:1400", show)
	}
}

// A library show whose row the provider doesn't list (an export that failed)
// takes the key of the listed title row promotion hands to it, so a removal
// under that key reaches it.
func TestServiceWatchlistPromotedItemAdoptsTheListedKey(t *testing.T) {
	store, repo, conn := favoriteImportFixture(t)
	conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
	repo.listItemStates = append(repo.listItemStates,
		ListItemState{
			ConnectionID: conn.ID, ListKind: ListKindWatchlist, MediaItemID: "show-121361", Kind: "series",
			ProviderItemKey: "imdb:tt0944947", LocalPresent: true,
		},
		ListItemState{
			ConnectionID: conn.ID, ListKind: ListKindWatchlist, MediaItemID: "watchlist-title:series:1399", Kind: "series",
			ProviderItemKey: "tmdb:1399", LocalPresent: true, RemotePresent: true, TitleIDs: []string{"tvdb:121361"},
		})
	titles := &titlesStub{entries: map[int]time.Time{}, promotedTVDB: map[int]bool{121361: true}}
	service := NewService(repo, NewRegistry()).WithUserStoreProvider(staticStoreProvider{store: store})
	service.SetWatchlistTitles(titles)

	if err := service.reconcileLocalListTitles(context.Background(), conn, service.watchlistBinding()); err != nil {
		t.Fatal(err)
	}
	if show := listItemStateOf(repo, "show-121361"); show.ProviderItemKey != "tmdb:1399" || !show.RemotePresent {
		t.Fatalf("show row = %#v, want the listed title row's key", show)
	}
}

// A row from before title IDs were kept knows its title only by the TMDB ID
// it is named for and its provider key. The library show it hands over to
// keeps that TMDB ID too, so a complete read listing only it keeps the show.
func TestServiceWatchlistPromotedItemKeepsTheRowsTMDBID(t *testing.T) {
	store, repo, conn := favoriteImportFixture(t)
	conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
	repo.listItemStates = append(repo.listItemStates, ListItemState{
		ConnectionID: conn.ID, ListKind: ListKindWatchlist, MediaItemID: "watchlist-title:series:1399", Kind: "series",
		ProviderItemKey: "tvdb:121361", LocalPresent: true, RemotePresent: true,
	})
	titles := &titlesStub{entries: map[int]time.Time{}, promotedTVDB: map[int]bool{121361: true}}
	service := NewService(repo, NewRegistry()).WithUserStoreProvider(staticStoreProvider{store: store})
	service.SetWatchlistTitles(titles)

	if err := service.reconcileLocalListTitles(context.Background(), conn, service.watchlistBinding()); err != nil {
		t.Fatal(err)
	}
	if show := listItemStateOf(repo, "show-121361"); show == nil || !slices.Contains(show.TitleIDs, "tmdb:1399") {
		t.Fatalf("show row = %#v, want it to keep tmdb:1399", show)
	}
}

// Both listed title rows of a series promote to one library show, which takes
// the first row's key. A provider removal under the other row's key still
// takes the show off the watchlist.
func TestServiceWatchlistRemovalUnderARetiredRowsKeyReachesThePromotedItem(t *testing.T) {
	ctx := context.Background()
	store, repo, conn := favoriteImportFixture(t)
	conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
	for _, row := range []struct{ id, key string }{
		{"watchlist-title:series:1399", "tmdb:1399"},
		{"watchlist-title:series:1400", "tmdb:1400"},
	} {
		repo.listItemStates = append(repo.listItemStates, ListItemState{
			ConnectionID: conn.ID, ListKind: ListKindWatchlist, MediaItemID: row.id, Kind: "series",
			ProviderItemKey: row.key, LocalPresent: true, RemotePresent: true, TitleIDs: []string{"tvdb:121361"},
		})
	}
	if _, err := store.AddToWatchlistAt(ctx, conn.ProfileID, "show-121361", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	titles := &titlesStub{entries: map[int]time.Time{}, promotedTVDB: map[int]bool{121361: true}}
	service := NewService(repo, NewRegistry()).
		WithMatcher(unmatchedMatcherStub{}).
		WithUserStoreProvider(staticStoreProvider{store: store})
	service.SetWatchlistTitles(titles)
	provider := &watchlistIncrementalProviderStub{watchlistBatchProviderStub{rows: []RemoteFavorite{
		{ProviderItemKey: "tmdb:1400", Kind: "series", Removed: true},
	}}}
	if _, err := service.importList(ctx, conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
		t.Fatal(err)
	}
	if entry, err := store.GetWatchlistEntry(ctx, conn.ProfileID, "show-121361"); err != nil || entry != nil {
		t.Fatalf("library watchlist entry = %v, %v; want the show removed", entry, err)
	}
}

// A provider removal takes a series off the watchlist in whichever form it is
// in, after promotion moved its entry onto a library show that shares only
// its TVDB ID: during the run (the entry was still there when the run read
// it), or before it, through a complete read or a removal.
func TestServiceWatchlistProviderRemovalReachesThePromotedItem(t *testing.T) {
	const show = "show-121361"
	listing := RemoteFavorite{ProviderItemKey: "tmdb:1399", Kind: "series", Title: "Show", TMDBID: "1399", TVDBID: "121361"}
	for _, tt := range []struct {
		name string
		// promotedBefore promotes before the second run reads the
		// watchlist; otherwise promotion lands during the run.
		promotedBefore bool
		rows           []RemoteFavorite
	}{
		{name: "promoted during the run"},
		{name: "promoted before the run, complete read", promotedBefore: true},
		{name: "promoted before the run, removal", promotedBefore: true,
			rows: []RemoteFavorite{{ProviderItemKey: listing.ProviderItemKey, Kind: "series", Removed: true}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store, repo, conn := favoriteImportFixture(t)
			conn.ImportWatchlistEnabled, conn.SyncWatchlistRemovalsEnabled = true, true
			titles := &titlesStub{entries: map[int]time.Time{}}
			service := NewService(repo, NewRegistry()).
				WithMatcher(unmatchedMatcherStub{}).
				WithUserStoreProvider(staticStoreProvider{store: store})
			service.SetWatchlistTitles(titles)
			provider := &watchlistBatchProviderStub{rows: []RemoteFavorite{listing}}
			if _, err := service.importList(ctx, conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
				t.Fatal(err)
			}

			// Promotion moves the entry onto the library show; with
			// promotedBefore the entry is gone before the run reads it.
			if _, err := store.AddToWatchlistAt(ctx, conn.ProfileID, show, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			titles.promotedTVDB = map[int]bool{121361: true}
			if tt.promotedBefore {
				delete(titles.entries, 1399)
			}
			provider.rows = tt.rows
			if _, err := service.importList(ctx, conn, ServerConfig{}, provider, service.watchlistBinding()); err != nil {
				t.Fatal(err)
			}
			if entry, err := store.GetWatchlistEntry(ctx, conn.ProfileID, show); err != nil || entry != nil {
				t.Fatalf("library watchlist entry = %v, %v; want the show removed with the title", entry, err)
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

func listItemStateOf(repo *serviceFakeRepo, mediaItemID string) *ListItemState {
	for i := range repo.listItemStates {
		if repo.listItemStates[i].MediaItemID == mediaItemID {
			return &repo.listItemStates[i]
		}
	}
	return nil
}

// A watchlist entry outside the library added in Silo goes to the provider
// under its TMDB ID.
func TestServiceWatchlistExportSendsTitlesOutsideLibrary(t *testing.T) {
	store, repo, conn := favoriteImportFixture(t)
	conn.ExportWatchlistEnabled = true
	titles := &titlesStub{entries: map[int]time.Time{550: time.Now().UTC()}}
	service := NewService(repo, NewRegistry()).WithUserStoreProvider(staticStoreProvider{store: store})
	service.SetWatchlistTitles(titles)
	provider := &watchlistBatchProviderStub{}

	result, err := service.exportList(context.Background(), conn, ServerConfig{}, provider, service.watchlistBinding())
	if err != nil {
		t.Fatal(err)
	}
	if result.LocalFound != 1 || result.Sent != 1 || len(provider.exported) != 1 {
		t.Fatalf("result = %#v, exported = %#v", result, provider.exported)
	}
	if item := provider.exported[0]; item.MediaItemID != "watchlist-title:movie:550" || item.TMDBID != "550" || item.ProviderItemKey != "tmdb:550" {
		t.Fatalf("exported = %#v", item)
	}
	if state := listItemStateOf(repo, "watchlist-title:movie:550"); state == nil || !state.RemotePresent || !state.LocalPresent {
		t.Fatalf("state = %#v, want exported", state)
	}
}
