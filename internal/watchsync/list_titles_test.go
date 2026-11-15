package watchsync

import (
	"context"
	"errors"
	"reflect"
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

func (s *titlesStub) WatchlistTitlesListed(_ context.Context, _ int, _ string, titles []WatchlistTitle) (map[WatchlistTitle]bool, error) {
	out := map[WatchlistTitle]bool{}
	for _, title := range titles {
		_, out[title] = s.entries[title.TMDBID]
	}
	return out, nil
}

func (s *titlesStub) WatchlistTitleTMDBIDs(_ context.Context, _ int, _ string, titles []WatchlistTitle) (map[WatchlistTitle][]int, error) {
	out := map[WatchlistTitle][]int{}
	for _, title := range titles {
		if _, ok := s.entries[title.TMDBID]; ok {
			out[title] = append([]int{title.TMDBID}, s.former[title.TMDBID]...)
		}
	}
	return out, nil
}

func (s *titlesStub) WatchlistTitlePromoted(_ context.Context, _ int, _ string, title WatchlistTitle) (bool, error) {
	return s.promoted[title.TMDBID], nil
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
