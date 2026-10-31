package watchsync

import (
	"context"
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
}

func (s *titlesStub) LibraryHas(_ context.Context, title WatchlistTitle) (bool, error) {
	return s.library[title.TMDBID], nil
}

func (s *titlesStub) AddWatchlistTitle(_ context.Context, _ int, _ string, title WatchlistTitle, addedAt time.Time) (bool, error) {
	if _, ok := s.entries[title.TMDBID]; ok {
		return false, nil
	}
	s.entries[title.TMDBID] = addedAt
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
			wantLocal:  true, wantRemote: true, wantQueued: 1,
		},
		{
			name:       "listed title already on the watchlist is queued for its request",
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
			for _, state := range tt.states {
				state.ConnectionID, state.ListKind = conn.ID, ListKindWatchlist
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
