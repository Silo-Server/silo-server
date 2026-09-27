package watchsync

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	droppedTestSeriesA = "series-a"
	droppedTestSeriesB = "series-b"
	droppedTestSeriesC = "series-c"
)

func TestDecideDropped(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	for _, tc := range []struct {
		name                string
		local, remote, base bool
		endedByWatch        bool
		activity, remoteAt  time.Time
		want                droppedAction
	}{
		{name: "neither dropped", want: droppedAgree},
		{name: "both dropped", local: true, remote: true, base: true, want: droppedAgree},
		{name: "both dropped first sync", local: true, remote: true, want: droppedAgree},
		{name: "both undropped", base: true, want: droppedAgree},
		{name: "dropped in silo", local: true, want: droppedExportDrop},
		{name: "undropped in silo", remote: true, base: true, want: droppedExportUndrop},
		{name: "dropped remotely", remote: true, remoteAt: newer, activity: older, want: droppedImportDrop},
		{name: "dropped remotely with no activity", remote: true, remoteAt: newer, want: droppedImportDrop},
		{name: "dropped remotely at unknown time", remote: true, activity: newer, want: droppedImportDrop},
		{name: "watched in silo after the remote drop", remote: true, remoteAt: older, activity: newer, want: droppedExportUndrop},
		{name: "undropped remotely", local: true, base: true, want: droppedImportUndrop},
		{name: "watched in silo, provider still holds the older drop", remote: true, base: true, endedByWatch: true, remoteAt: older, activity: newer, want: droppedExportUndrop},
		{name: "re-dropped on the provider after the watch", remote: true, base: true, endedByWatch: true, remoteAt: newer, activity: older, want: droppedImportDrop},
		{name: "undone in silo, provider still holds the drop", remote: true, base: true, remoteAt: newer, want: droppedExportUndrop},
		{name: "watch ended the drop, provider drop time unknown", remote: true, base: true, endedByWatch: true, activity: newer, want: droppedExportUndrop},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideDropped(tc.local, tc.remote, tc.base, tc.endedByWatch, tc.activity, tc.remoteAt); got != tc.want {
				t.Fatalf("decideDropped = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSyncDroppedFirstSyncUnionsBothSides(t *testing.T) {
	h := newDroppedHarness(t)
	h.store.drop(droppedTestSeriesA, h.at(0)) // Silo only
	h.provider.batch = DroppedImportBatch{Rows: []RemoteDropped{h.remoteRow(droppedTestSeriesB, h.at(1))}, Complete: true}

	result := h.sync()

	if !h.store.active(droppedTestSeriesB) {
		t.Fatal("the provider's drop must be imported")
	}
	if got := h.store.rows[droppedTestSeriesB]; !got.Equal(h.at(1)) {
		t.Fatalf("imported dropped_at = %s, want the provider's drop time", got)
	}
	if ids := keys(h.provider.dropped); !slices.Equal(ids, []string{droppedTestSeriesA}) {
		t.Fatalf("dropped on provider = %v, want [%s]", ids, droppedTestSeriesA)
	}
	if s := h.state(droppedTestSeriesB); s == nil || !s.RemoteSeen {
		t.Fatalf("imported drop state = %#v, want seen", s)
	}
	if s := h.state(droppedTestSeriesA); s == nil || s.RemoteSeen {
		t.Fatalf("exported drop state = %#v, want agreed but not yet seen", s)
	}
	if result.Imported != 1 || result.Sent != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestSyncDroppedRemoteDropOlderThanSiloActivityUndropsRemotely(t *testing.T) {
	h := newDroppedHarness(t)
	h.store.activity[droppedTestSeriesA] = h.at(5)
	h.provider.batch = DroppedImportBatch{Rows: []RemoteDropped{h.remoteRow(droppedTestSeriesA, h.at(1))}, Complete: true}

	h.sync()

	if h.store.active(droppedTestSeriesA) {
		t.Fatal("a show watched in Silo after the remote drop must not be dropped locally")
	}
	if ids := keys(h.provider.undropped); !slices.Equal(ids, []string{droppedTestSeriesA}) {
		t.Fatalf("undropped on provider = %v, want [%s]", ids, droppedTestSeriesA)
	}
}

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

func TestSyncDroppedImportsAProviderReDropAfterALocalWatch(t *testing.T) {
	h := newDroppedHarness(t)
	h.store.drop(droppedTestSeriesA, h.at(1))
	h.agree(droppedTestSeriesA, true)
	h.repo.droppedStates[0].UpdatedAt = h.at(1)
	h.store.activity[droppedTestSeriesA] = h.at(2) // watched in Silo: the drop ended
	// Dropped again on the provider after that watch.
	h.provider.batch = DroppedImportBatch{Rows: []RemoteDropped{h.remoteRow(droppedTestSeriesA, h.at(3))}, Complete: true}

	h.sync()

	if len(h.provider.undropped) != 0 {
		t.Fatalf("undropped = %v, want the newer provider drop kept", keys(h.provider.undropped))
	}
	if !h.store.active(droppedTestSeriesA) || !h.store.rows[droppedTestSeriesA].Equal(h.at(3)) {
		t.Fatalf("local drop = %v active=%v, want re-dropped at the provider time", h.store.rows[droppedTestSeriesA], h.store.active(droppedTestSeriesA))
	}

	// The provider undrops it again: the imported drop is agreed, so the
	// undrop is imported rather than the drop being sent back.
	h.provider.batch = DroppedImportBatch{Rows: []RemoteDropped{h.remoteRow(droppedTestSeriesB, h.at(3))}, Complete: true}
	h.store.activity[droppedTestSeriesB] = h.at(4)
	h.sync()
	if h.store.active(droppedTestSeriesA) {
		t.Fatal("the provider undrop must be imported")
	}
	for _, item := range h.provider.dropped {
		if item.MediaItemID == droppedTestSeriesA {
			t.Fatal("the imported drop must not be sent back to the provider")
		}
	}
}

func TestRedroppingAfterAWatchEndedTheAgreedDropIsSent(t *testing.T) {
	h := newDroppedHarness(t)
	h.store.drop(droppedTestSeriesA, h.at(1))
	h.agree(droppedTestSeriesA, true)
	h.repo.droppedStates[0].UpdatedAt = h.at(1)
	// Watching ended the drop, then the profile dismissed the show again.
	h.store.activity[droppedTestSeriesA] = h.at(2)
	h.store.drop(droppedTestSeriesA, h.at(3))

	event := LocalDroppedEvent{UserID: h.conn.UserID, ProfileID: h.conn.ProfileID, SeriesIDs: []string{droppedTestSeriesA}}
	if err := h.service.processLocalDroppedEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if ids := keys(h.provider.dropped); !slices.Equal(ids, []string{droppedTestSeriesA}) {
		t.Fatalf("dropped = %v, want the re-drop sent", ids)
	}

	// The provider undropped the show when the watch reached it; a read
	// that no longer lists it must not delete the new drop.
	h.provider.batch = DroppedImportBatch{Complete: true}
	h.sync()
	if !h.store.active(droppedTestSeriesA) {
		t.Fatal("the re-drop must survive a read taken before the provider saw it")
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

func TestSyncDroppedRemoteUndropImportsWhenConfirmedBefore(t *testing.T) {
	h := newDroppedHarness(t)
	h.store.drop(droppedTestSeriesA, h.at(1))
	h.store.drop(droppedTestSeriesB, h.at(1))
	h.agree(droppedTestSeriesA, true)
	h.agree(droppedTestSeriesB, false) // sent but never seen on the provider
	h.provider.batch = DroppedImportBatch{Rows: []RemoteDropped{h.remoteRow(droppedTestSeriesC, h.at(1))}, Complete: true}

	h.sync()

	if h.store.active(droppedTestSeriesA) {
		t.Fatal("a confirmed drop missing from a complete read must be undropped locally")
	}
	if !h.store.active(droppedTestSeriesB) {
		t.Fatal("a drop the provider never confirmed must not be undropped locally")
	}
	// Trakt's read omits drops apps make, so an unconfirmed drop is left as
	// agreed: resending it would re-drop a show the user undropped on Trakt.
	if len(h.provider.dropped) != 0 {
		t.Fatalf("dropped on provider = %v, want no resend", keys(h.provider.dropped))
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

func TestSyncDroppedDisabledDoesNothing(t *testing.T) {
	h := newDroppedHarness(t)
	h.conn.SyncDroppedEnabled = false
	h.store.drop(droppedTestSeriesA, h.at(1))
	h.provider.batch = DroppedImportBatch{Rows: []RemoteDropped{h.remoteRow(droppedTestSeriesB, h.at(1))}, Complete: true}

	h.sync()

	if h.provider.fetches != 0 || len(h.provider.dropped) != 0 || h.store.active(droppedTestSeriesB) {
		t.Fatal("a disabled connection must not sync drops")
	}
}

func TestSyncDroppedSavesProviderCursors(t *testing.T) {
	h := newDroppedHarness(t)
	h.provider.batch = DroppedImportBatch{Complete: false, UpdatedCursors: map[string]string{"simkl.dropped.shows": "t1"}}

	h.sync()

	conn := h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)]
	if conn.SyncCursors["simkl.dropped.shows"] != "t1" {
		t.Fatalf("cursors = %v", conn.SyncCursors)
	}
}

func TestLocalDroppedEventSendsOnlyLocalChanges(t *testing.T) {
	h := newDroppedHarness(t)
	h.store.drop(droppedTestSeriesA, h.at(1))
	h.agree(droppedTestSeriesB, true) // undone in Silo: no local row

	if err := h.service.processLocalDroppedEvent(context.Background(), LocalDroppedEvent{
		UserID: h.conn.UserID, ProfileID: h.conn.ProfileID, SeriesIDs: []string{droppedTestSeriesA, droppedTestSeriesB},
	}); err != nil {
		t.Fatal(err)
	}

	if h.provider.fetches != 0 {
		t.Fatal("a local event must not read the provider")
	}
	if ids := keys(h.provider.dropped); !slices.Equal(ids, []string{droppedTestSeriesA}) {
		t.Fatalf("dropped = %v, want [%s]", ids, droppedTestSeriesA)
	}
	if ids := keys(h.provider.undropped); !slices.Equal(ids, []string{droppedTestSeriesB}) {
		t.Fatalf("undropped = %v, want [%s]", ids, droppedTestSeriesB)
	}
	if !slices.Equal(h.repo.ratingLocks, []string{"wait:" + h.conn.ID}) {
		t.Fatalf("locks = %v, want one waiting lock", h.repo.ratingLocks)
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

func TestUndoOfAnUnconfirmedDropForgetsItsAgreement(t *testing.T) {
	h := newDroppedHarness(t)
	h.store.drop(droppedTestSeriesA, h.at(1))
	event := LocalDroppedEvent{UserID: h.conn.UserID, ProfileID: h.conn.ProfileID, SeriesIDs: []string{droppedTestSeriesA}}
	if err := h.service.processLocalDroppedEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	delete(h.store.rows, droppedTestSeriesA)
	if err := h.service.processLocalDroppedEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if ids := keys(h.provider.undropped); !slices.Equal(ids, []string{droppedTestSeriesA}) {
		t.Fatalf("undropped = %v, want the undo sent", ids)
	}
	if h.state(droppedTestSeriesA) != nil {
		t.Fatal("an undo of a drop no read confirmed must forget the agreement")
	}
	// A later read that does not list the show changes nothing.
	h.provider.batch = DroppedImportBatch{Complete: true}
	h.sync()
	if len(h.provider.dropped)+len(h.provider.undropped) != 0 || h.store.active(droppedTestSeriesA) {
		t.Fatal("a settled undo must not write again")
	}
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
