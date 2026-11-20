package watchsync

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	droppedTestSeriesA = "series-a"
	droppedTestSeriesB = "series-b"
	droppedTestSeriesC = "series-c"
)

func TestSyncDroppedWatchingAgainUndropsOnProvider(t *testing.T) {
	h := newDroppedHarness(t)
	h.store.drop(droppedTestSeriesA, h.at(1))
	h.agree(droppedTestSeriesA, true)
	h.store.activity[droppedTestSeriesA] = h.at(2) // watched after the drop
	h.provider.batch = DroppedImportBatch{Rows: []RemoteDropped{h.remoteRow(droppedTestSeriesA, h.at(1))}, Complete: true}

	h.sync()

	if ids := keys(h.provider.undropped); !slices.Equal(ids, []string{droppedTestSeriesA}) {
		t.Fatalf("undropped on provider = %v, want [%s]", ids, droppedTestSeriesA)
	}
	if _, ok := h.store.rows[droppedTestSeriesA]; ok {
		t.Fatal("a confirmed undrop must clean up the inactive local row")
	}
	if h.state(droppedTestSeriesA) == nil {
		t.Fatal("a sent undrop must keep its agreed row until a read confirms it")
	}

	// A cached read taken before the undrop still lists the show: the undrop
	// is sent again, never imported back as a drop.
	h.sync()
	if h.store.active(droppedTestSeriesA) {
		t.Fatal("a stale read must not import the undone drop back")
	}
	if ids := keys(h.provider.undropped); !slices.Equal(ids, []string{droppedTestSeriesA}) {
		t.Fatalf("undropped on provider = %v, want the undrop resent", ids)
	}

	// A read that no longer lists the show confirms the undrop.
	h.provider.batch = DroppedImportBatch{Rows: []RemoteDropped{h.remoteRow(droppedTestSeriesB, h.at(1))}, Complete: true}
	h.store.activity[droppedTestSeriesB] = h.at(2)
	h.sync()
	if h.state(droppedTestSeriesA) != nil || len(h.provider.undropped) != 1 || h.provider.undropped[0].MediaItemID != droppedTestSeriesB {
		t.Fatalf("state = %#v undropped = %v; want series A forgotten without another write", h.state(droppedTestSeriesA), keys(h.provider.undropped))
	}
}

func TestSyncDroppedUndoSurvivesAStaleRead(t *testing.T) {
	h := newDroppedHarness(t)
	h.store.drop(droppedTestSeriesA, h.at(1))
	event := LocalDroppedEvent{UserID: h.conn.UserID, ProfileID: h.conn.ProfileID, SeriesIDs: []string{droppedTestSeriesA}}
	if err := h.service.processLocalDroppedEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	// A read confirms the provider holds the drop.
	h.provider.batch = DroppedImportBatch{Rows: []RemoteDropped{h.remoteRow(droppedTestSeriesA, h.at(1))}, Complete: true}
	h.sync()
	if s := h.state(droppedTestSeriesA); s == nil || !s.RemoteSeen {
		t.Fatalf("state = %#v, want the drop confirmed", s)
	}
	h.provider.undropped = nil
	delete(h.store.rows, droppedTestSeriesA) // the profile undoes the dismissal
	if err := h.service.processLocalDroppedEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if ids := keys(h.provider.undropped); !slices.Equal(ids, []string{droppedTestSeriesA}) {
		t.Fatalf("undropped = %v, want the undo sent", ids)
	}

	// The provider's cached list now shows the drop Silo sent earlier.
	h.provider.batch = DroppedImportBatch{Rows: []RemoteDropped{h.remoteRow(droppedTestSeriesA, h.at(1))}, Complete: true}
	h.sync()
	if h.store.active(droppedTestSeriesA) {
		t.Fatal("a stale read must not revert the undo")
	}
	if ids := keys(h.provider.undropped); !slices.Equal(ids, []string{droppedTestSeriesA}) {
		t.Fatalf("undropped = %v, want the undo resent", ids)
	}
}

func TestSyncDroppedDistrustsAnEmptyCompleteRead(t *testing.T) {
	h := newDroppedHarness(t)
	for _, id := range []string{droppedTestSeriesA, droppedTestSeriesB} {
		h.store.drop(id, h.at(1))
		h.agree(id, true)
	}
	h.provider.batch = DroppedImportBatch{Complete: true}

	result := h.sync()

	if !h.store.active(droppedTestSeriesA) || !h.store.active(droppedTestSeriesB) {
		t.Fatal("an empty read must not undrop several confirmed drops")
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "returned no dropped shows") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

func TestSyncDroppedSkipsAfterAccountRebind(t *testing.T) {
	h := newDroppedHarness(t)
	h.store.drop(droppedTestSeriesA, h.at(1))
	h.provider.batch = DroppedImportBatch{Rows: []RemoteDropped{h.remoteRow(droppedTestSeriesB, h.at(1))}, Complete: true}
	h.provider.onFetch = func() {
		key := connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)
		rebound := h.repo.connections[key]
		rebound.ProviderAccountID = "another-account"
		h.repo.connections[key] = rebound
	}

	result := h.sync()

	if h.store.active(droppedTestSeriesB) || len(h.provider.dropped) != 0 || len(h.repo.droppedStates) != 0 {
		t.Fatal("a stale run must not apply drops")
	}
	if len(result.Warnings) == 0 {
		t.Fatal("a skipped stale run should warn")
	}
}

func TestPersistConnectionRebindClearsDroppedStatesAndCursors(t *testing.T) {
	h := newDroppedHarness(t)
	h.agree(droppedTestSeriesA, true)
	conn := h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)]
	conn.SyncCursors = map[string]string{"simkl.dropped.shows": "t1", "simkl.watched.shows": "t2"}
	h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)] = conn

	saved, err := h.service.persistConnection(context.Background(), h.conn.Provider, h.conn.UserID, h.conn.ProfileID, TokenSet{AccessToken: "t"}, ProviderAccount{ID: "another-account"})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.repo.droppedStates) != 0 {
		t.Fatalf("states = %#v, want cleared", h.repo.droppedStates)
	}
	if _, ok := saved.SyncCursors["simkl.dropped.shows"]; ok || saved.SyncCursors["simkl.watched.shows"] != "t2" {
		t.Fatalf("cursors = %v", saved.SyncCursors)
	}
}

type droppedHarness struct {
	t        *testing.T
	repo     *serviceFakeRepo
	store    *fakeDroppedStore
	provider *droppedProviderStub
	media    map[string]LocalFavorite
	conn     Connection
	service  *Service
}

func newDroppedHarness(t *testing.T) *droppedHarness {
	t.Helper()
	h := &droppedHarness{
		t:        t,
		repo:     newServiceFakeRepo(),
		store:    &fakeDroppedStore{rows: map[string]time.Time{}, activity: map[string]time.Time{}},
		provider: &droppedProviderStub{},
		media: map[string]LocalFavorite{
			droppedTestSeriesA: {MediaItemID: droppedTestSeriesA, Kind: historyimport.KindSeries, TMDBID: "201", ProviderItemKey: "tmdb:201"},
			droppedTestSeriesB: {MediaItemID: droppedTestSeriesB, Kind: historyimport.KindSeries, TMDBID: "202", ProviderItemKey: "tmdb:202"},
			droppedTestSeriesC: {MediaItemID: droppedTestSeriesC, Kind: historyimport.KindSeries, TMDBID: "203", ProviderItemKey: "tmdb:203"},
		},
	}
	h.repo.listMedia = h.media
	h.conn = Connection{
		ID: "conn-dropped", Provider: h.provider.Key(), UserID: 7, ProfileID: "profile-1",
		AccessToken: "token", ProviderAccountID: "acct", SyncDroppedEnabled: true,
	}
	h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)] = h.conn
	registry := NewRegistry()
	if err := registry.Register(h.provider); err != nil {
		t.Fatal(err)
	}
	h.service = NewService(h.repo, registry).
		WithMatcher(ratingMatcherStub{media: h.media}).
		WithDroppedStore(h.store)
	h.service.now = func() time.Time { return h.at(100) }
	return h
}

func (h *droppedHarness) at(hours int) time.Time {
	return time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(hours) * time.Hour)
}

func (h *droppedHarness) sync() SyncDroppedResult {
	h.t.Helper()
	h.provider.dropped, h.provider.undropped = nil, nil
	h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)] = h.conn
	result, err := h.service.syncDropped(context.Background(), h.conn, ServerConfig{}, h.provider)
	if err != nil {
		h.t.Fatal(err)
	}
	return result
}

func (h *droppedHarness) remoteRow(seriesID string, at time.Time) RemoteDropped {
	item := h.media[seriesID]
	return RemoteDropped{
		RemoteFavorite: RemoteFavorite{Provider: "test", ProviderItemKey: item.ProviderItemKey, Kind: item.Kind, TMDBID: item.TMDBID},
		DroppedAt:      at,
	}
}

func (h *droppedHarness) agree(seriesID string, seen bool) {
	_ = h.repo.UpsertDroppedSyncStates(context.Background(), []DroppedSyncState{{
		ConnectionID: h.conn.ID, ProviderAccountID: h.conn.ProviderAccountID, SeriesID: seriesID,
		ProviderItemKey: h.media[seriesID].ProviderItemKey, RemoteSeen: seen,
	}})
}

func (h *droppedHarness) state(seriesID string) *DroppedSyncState {
	for i := range h.repo.droppedStates {
		if h.repo.droppedStates[i].SeriesID == seriesID {
			return &h.repo.droppedStates[i]
		}
	}
	return nil
}

func keys(items []LocalFavorite) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.MediaItemID)
	}
	slices.Sort(ids)
	return ids
}

// fakeDroppedStore follows catalog.DroppedSeriesRepo: a row is active while
// the series has no activity newer than its dropped_at.
type fakeDroppedStore struct {
	rows     map[string]time.Time
	activity map[string]time.Time
}

func (s *fakeDroppedStore) drop(seriesID string, at time.Time) { s.rows[seriesID] = at }

func (s *fakeDroppedStore) active(seriesID string) bool {
	at, ok := s.rows[seriesID]
	return ok && !s.activity[seriesID].After(at)
}

func (s *fakeDroppedStore) ListDropped(_ context.Context, _ int, _ string, seriesIDs []string) ([]catalog.DroppedSeries, error) {
	var out []catalog.DroppedSeries
	for id, at := range s.rows {
		if seriesIDs != nil && !slices.Contains(seriesIDs, id) {
			continue
		}
		out = append(out, catalog.DroppedSeries{SeriesID: id, DroppedAt: at, Active: s.active(id)})
	}
	return out, nil
}

func (s *fakeDroppedStore) LatestActivity(_ context.Context, _ int, _ string, seriesIDs []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	for _, id := range seriesIDs {
		if at, ok := s.activity[id]; ok {
			out[id] = at
		}
	}
	return out, nil
}

func (s *fakeDroppedStore) ImportDrop(_ context.Context, _ int, _, seriesID string, droppedAt time.Time, observed *time.Time) (bool, error) {
	current, ok := s.rows[seriesID]
	if (observed == nil) == ok || (observed != nil && !current.Equal(*observed)) {
		return false, nil
	}
	s.rows[seriesID] = droppedAt
	return true, nil
}

func (s *fakeDroppedStore) DeleteIfUnchanged(_ context.Context, _ int, _, seriesID string, observed time.Time) (bool, error) {
	if current, ok := s.rows[seriesID]; !ok || !current.Equal(observed) {
		return false, nil
	}
	delete(s.rows, seriesID)
	return true, nil
}

func (s *fakeDroppedStore) DeleteInactive(_ context.Context, _ int, _ string, seriesIDs []string) error {
	for _, id := range seriesIDs {
		if _, ok := s.rows[id]; ok && !s.active(id) {
			delete(s.rows, id)
		}
	}
	return nil
}

type droppedProviderStub struct {
	batch     DroppedImportBatch
	fetches   int
	dropped   []LocalFavorite
	undropped []LocalFavorite
	onFetch   func()
}

func (*droppedProviderStub) Key() string         { return "test" }
func (*droppedProviderStub) DisplayName() string { return "Test" }
func (*droppedProviderStub) Capabilities() Capabilities {
	return Capabilities{SyncDropped: true}
}

func (*droppedProviderStub) ConnectWithAPIKey(context.Context, string) (TokenSet, ProviderAccount, error) {
	return TokenSet{}, ProviderAccount{}, nil
}

func (p *droppedProviderStub) FetchDropped(context.Context, ServerConfig, Connection) (DroppedImportBatch, error) {
	p.fetches++
	if p.onFetch != nil {
		p.onFetch()
	}
	return p.batch, nil
}

func (p *droppedProviderStub) ExportDropped(_ context.Context, _ ServerConfig, _ Connection, items []LocalFavorite) (ExportResult, error) {
	p.dropped = append(p.dropped, items...)
	return sentAll(items), nil
}

func (p *droppedProviderStub) RemoveDropped(_ context.Context, _ ServerConfig, _ Connection, items []LocalFavorite) (ExportResult, error) {
	p.undropped = append(p.undropped, items...)
	return sentAll(items), nil
}

func sentAll(items []LocalFavorite) ExportResult {
	var result ExportResult
	for _, item := range items {
		result.Sent = append(result.Sent, item.MediaItemID, item.ProviderItemKey)
	}
	return result
}

type noopWatchState struct{}

func (noopWatchState) RecordImportedWatchIfNewerWithSource(context.Context, int, string, string, float64, float64, bool, time.Time, *time.Time, userstore.WatchHistorySource) (bool, error) {
	return false, nil
}

// rateLimitedWatchedProvider is a dropped-show provider whose watched import
// is always rate limited.
type rateLimitedWatchedProvider struct{ *droppedProviderStub }

func (rateLimitedWatchedProvider) Capabilities() Capabilities {
	return Capabilities{SyncDropped: true, ImportWatched: true}
}

func (rateLimitedWatchedProvider) FetchWatched(context.Context, ServerConfig, Connection) ([]RemoteWatch, error) {
	return nil, RateLimitedError{Provider: "test", RetryAfter: time.Minute}
}

func TestSyncDroppedKeepsADropWhoseSeriesLostItsProviderIDs(t *testing.T) {
	h := newDroppedHarness(t)
	h.store.drop(droppedTestSeriesA, h.at(1))
	h.agree(droppedTestSeriesA, true)
	// The series lost its external ids since the drop was agreed.
	h.media[droppedTestSeriesA] = LocalFavorite{MediaItemID: droppedTestSeriesA, Kind: historyimport.KindSeries}
	h.provider.batch = DroppedImportBatch{Rows: []RemoteDropped{h.remoteRow(droppedTestSeriesB, h.at(1))}}
	h.store.activity[droppedTestSeriesB] = h.at(5)

	h.sync()

	for _, item := range h.provider.undropped {
		if item.MediaItemID == droppedTestSeriesA {
			t.Fatal("a drop still active in Silo must not be undropped on the provider")
		}
	}
	if !h.store.active(droppedTestSeriesA) || h.state(droppedTestSeriesA) == nil {
		t.Fatal("the drop and its agreement must be kept")
	}
}

// newPluginDroppedHarness is a dropped-show harness whose connection and
// registry use a PluginProvider over a fake plugin client, so the merge runs
// through the plugin bridge end to end.
func newPluginDroppedHarness(t *testing.T) (*droppedHarness, *PluginProvider, *fakeWatchSyncPluginClient) {
	t.Helper()
	h := newDroppedHarness(t)
	client := &fakeWatchSyncPluginClient{}
	provider := testPluginProviderWithDescriptor(t, client, droppedTestDescriptor())
	delete(h.repo.connections, connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID))
	h.conn.Provider = provider.Key()
	h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)] = h.conn
	registry := NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	h.service.registry = registry
	return h, provider, client
}

func TestSyncRunReadsDroppedBeforeARateLimitedHistoryImport(t *testing.T) {
	h := newDroppedHarness(t)
	provider := rateLimitedWatchedProvider{h.provider}
	registry := NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	h.service.registry = registry
	h.service.WithWatchState(noopWatchState{})
	h.conn.ImportWatchedEnabled = true
	h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)] = h.conn
	h.provider.batch = DroppedImportBatch{Rows: []RemoteDropped{h.remoteRow(droppedTestSeriesA, h.at(1))}, Complete: true}

	err := h.service.SyncConnection(context.Background(), h.conn, "scheduled")

	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("sync error = %v, want the rate-limited history import to fail the run", err)
	}
	if !h.store.active(droppedTestSeriesA) {
		t.Fatal("the provider's drop must be imported before the history import hits the rate limit")
	}
}

func TestSyncDroppedPluginTombstonePreservesOtherKeys(t *testing.T) {
	for _, tc := range []struct {
		name    string
		agreed  bool
		items   func(*droppedHarness) []*pluginv1.WatchSyncRemoteState
		wantKey string
	}{
		{
			name:   "stored key removed after replacement",
			agreed: true,
			items: func(h *droppedHarness) []*pluginv1.WatchSyncRemoteState {
				return []*pluginv1.WatchSyncRemoteState{
					remoteDroppedState("new-key", "202", timestamppb.New(h.at(2))),
					droppedTombstone("old-key"),
				}
			},
			wantKey: "new-key",
		},
		{
			name:   "stored key removed before replacement",
			agreed: true,
			items: func(h *droppedHarness) []*pluginv1.WatchSyncRemoteState {
				return []*pluginv1.WatchSyncRemoteState{
					droppedTombstone("old-key"),
					remoteDroppedState("new-key", "202", timestamppb.New(h.at(2))),
				}
			},
			wantKey: "new-key",
		},
		{
			name: "newer drop removed",
			items: func(h *droppedHarness) []*pluginv1.WatchSyncRemoteState {
				return []*pluginv1.WatchSyncRemoteState{
					remoteDroppedState("old-key", "202", timestamppb.New(h.at(1))),
					remoteDroppedState("new-key", "202", timestamppb.New(h.at(2))),
					droppedTombstone("new-key"),
				}
			},
			wantKey: "old-key",
		},
		{
			name: "older drop removed",
			items: func(h *droppedHarness) []*pluginv1.WatchSyncRemoteState {
				return []*pluginv1.WatchSyncRemoteState{
					remoteDroppedState("new-key", "202", timestamppb.New(h.at(2))),
					remoteDroppedState("old-key", "202", timestamppb.New(h.at(1))),
					droppedTombstone("old-key"),
				}
			},
			wantKey: "new-key",
		},
		{
			name:   "different stored key remains unknown",
			agreed: true,
			items: func(h *droppedHarness) []*pluginv1.WatchSyncRemoteState {
				return []*pluginv1.WatchSyncRemoteState{
					remoteDroppedState("new-key", "202", timestamppb.New(h.at(2))),
					droppedTombstone("new-key"),
				}
			},
			wantKey: "old-key",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, provider, client := newPluginDroppedHarness(t)
			if tc.agreed {
				h.store.drop(droppedTestSeriesB, h.at(1))
				h.agree(droppedTestSeriesB, true)
				h.repo.droppedStates[0].ProviderItemKey = "old-key"
			}
			client.listResponse = &pluginv1.WatchSyncListRemoteStateResponse{Items: tc.items(h), NextCursor: testCursorOne}
			if _, err := h.service.syncDropped(t.Context(), h.conn, ServerConfig{}, provider); err != nil {
				t.Fatal(err)
			}
			if !h.store.active(droppedTestSeriesB) {
				t.Fatal("removing one provider key must preserve the drop under another key")
			}
			if state := h.state(droppedTestSeriesB); state == nil || !state.RemoteSeen || state.ProviderItemKey != tc.wantKey {
				t.Fatalf("agreement = %#v, want the surviving key %q", state, tc.wantKey)
			}
			key := connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)
			if cursor := h.repo.connections[key].SyncCursors[pluginDroppedCursorKey]; cursor != testCursorOne {
				t.Fatalf("saved cursor = %q, want %q", cursor, testCursorOne)
			}
		})
	}
}

func TestSyncDroppedIncompleteReadUndropsNothing(t *testing.T) {
	h := newDroppedHarness(t)
	h.store.drop(droppedTestSeriesA, h.at(1))
	h.agree(droppedTestSeriesA, true)
	h.provider.batch = DroppedImportBatch{Complete: false}

	h.sync()

	if !h.store.active(droppedTestSeriesA) || len(h.provider.dropped)+len(h.provider.undropped) != 0 {
		t.Fatalf("an incomplete read must change nothing: active=%v dropped=%v undropped=%v",
			h.store.active(droppedTestSeriesA), h.provider.dropped, h.provider.undropped)
	}
}

func TestSyncDroppedUnmatchedRowSharingAnIDIsNotAnUndrop(t *testing.T) {
	h := newDroppedHarness(t)
	h.store.drop(droppedTestSeriesA, h.at(1))
	h.agree(droppedTestSeriesA, true)
	// The provider reports series A by a TVDB id the matcher cannot place.
	row := h.remoteRow(droppedTestSeriesA, h.at(1))
	row.TMDBID = ""
	row.TVDBID = "9001"
	row.ProviderItemKey = h.media[droppedTestSeriesA].ProviderItemKey
	h.provider.batch = DroppedImportBatch{Rows: []RemoteDropped{row}, Complete: true}

	h.sync()

	if !h.store.active(droppedTestSeriesA) {
		t.Fatal("a row sharing the series' key must keep it dropped")
	}
}
