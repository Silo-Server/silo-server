package watchsync

import (
	"context"
	"fmt"
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

func TestSyncDroppedThroughAPluginProvider(t *testing.T) {
	h, provider, client := newPluginDroppedHarness(t)
	connKey := connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)
	run := func() SyncDroppedResult {
		t.Helper()
		client.applyRequest, client.listRequests = nil, nil
		result, err := h.service.syncDropped(context.Background(), h.conn, ServerConfig{}, provider)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	// A complete snapshot holds series B under the plugin's own key, while
	// series A is dropped only in Silo.
	h.store.drop(droppedTestSeriesA, h.at(0))
	client.applyStatus = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED
	client.listResponse = &pluginv1.WatchSyncListRemoteStateResponse{
		CompleteSnapshot: true,
		NextCursor:       testCursorOne,
		Items:            []*pluginv1.WatchSyncRemoteState{remoteDroppedState("remote-b", "202", timestamppb.New(h.at(1)))},
		Warnings:         []string{"skipped 1 show without ids"},
	}
	result := run()
	if !h.store.active(droppedTestSeriesB) || !h.store.rows[droppedTestSeriesB].Equal(h.at(1)) {
		t.Fatalf("series B = %v, want imported at the plugin's drop time", h.store.rows[droppedTestSeriesB])
	}
	if s := h.state(droppedTestSeriesB); s == nil || !s.RemoteSeen || s.ProviderItemKey != "remote-b" {
		t.Fatalf("series B state = %#v, want seen under the plugin's key", s)
	}
	events := client.applyRequest.GetEvents()
	if len(events) != 1 || events[0].GetOperation() != pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_MARK_DROPPED ||
		events[0].GetMedia().GetMediaType() != pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_SERIES ||
		events[0].GetMedia().GetMediaItemId() != droppedTestSeriesA || events[0].GetMedia().GetExternalIds()["tmdb"] != "201" ||
		events[0].GetProviderItemKey() != "tmdb:201" {
		t.Fatalf("events = %#v, want series A dropped", events)
	}
	if s := h.state(droppedTestSeriesA); s == nil || s.RemoteSeen {
		t.Fatalf("series A state = %#v, want agreed but not yet seen", s)
	}
	if result.RemoteFound != 1 || result.Imported != 1 || result.Sent != 1 || !slices.Contains(result.Warnings, "skipped 1 show without ids") {
		t.Fatalf("result = %#v", result)
	}
	if cursor := h.repo.connections[connKey].SyncCursors[pluginDroppedCursorKey]; cursor != testCursorOne {
		t.Fatalf("cursor = %q, want %q", cursor, testCursorOne)
	}

	// An incremental read from the saved cursor undrops B with a tombstone
	// that names it by the plugin's key, and leaves A unknown.
	client.listResponse = &pluginv1.WatchSyncListRemoteStateResponse{
		NextCursor: "cursor-2",
		Items:      []*pluginv1.WatchSyncRemoteState{droppedTombstone("remote-b")},
	}
	result = run()
	if len(client.listRequests) != 1 || client.listRequests[0].GetCursor() != testCursorOne {
		t.Fatalf("requests = %#v, want the read resumed from the saved cursor", client.listRequests)
	}
	if h.store.active(droppedTestSeriesB) || h.state(droppedTestSeriesB) != nil {
		t.Fatal("the plugin's undrop must undrop B locally and forget its agreement")
	}
	if client.applyRequest != nil || !h.store.active(droppedTestSeriesA) || h.state(droppedTestSeriesA) == nil {
		t.Fatalf("an incremental read must leave A as agreed: request=%#v", client.applyRequest)
	}
	if result.RemoteFound != 0 || result.Imported != 1 {
		t.Fatalf("result = %#v", result)
	}
	if cursor := h.repo.connections[connKey].SyncCursors[pluginDroppedCursorKey]; cursor != "cursor-2" {
		t.Fatalf("cursor = %q, want cursor-2", cursor)
	}

	// The profile undoes A. The plugin rejects the undrop, which it may not
	// do for a series that is not dropped, so the undrop is kept for a retry.
	delete(h.store.rows, droppedTestSeriesA)
	client.listResponse = &pluginv1.WatchSyncListRemoteStateResponse{NextCursor: "cursor-3"}
	client.applyStatus = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED
	client.applyFault = &pluginv1.WatchSyncFault{SafeMessage: "provider refused"}
	result = run()
	events = client.applyRequest.GetEvents()
	if len(events) != 1 || events[0].GetOperation() != pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_UNMARK_DROPPED ||
		events[0].GetProviderItemKey() != "tmdb:201" {
		t.Fatalf("events = %#v, want series A undropped", events)
	}
	if h.state(droppedTestSeriesA) == nil || !slices.Contains(result.Warnings, "provider refused: "+droppedTestSeriesA) {
		t.Fatalf("a rejected undrop keeps its agreement: state=%#v warnings=%v", h.state(droppedTestSeriesA), result.Warnings)
	}

	client.applyStatus, client.applyFault = pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED, nil
	run()
	if h.state(droppedTestSeriesA) != nil {
		t.Fatal("an applied undrop of a drop no read confirmed forgets the agreement")
	}
}

func TestSyncDroppedPluginUndropMatchingNoAgreedDropIsIgnored(t *testing.T) {
	h, provider, client := newPluginDroppedHarness(t)
	h.store.drop(droppedTestSeriesA, h.at(1))
	h.agree(droppedTestSeriesA, true)
	// Silo forgot the agreement for an undrop it sent, and the plugin echoes
	// that undrop; another key is shared by two agreed drops.
	h.agree(droppedTestSeriesB, true)
	h.agree(droppedTestSeriesC, true)
	for i := range h.repo.droppedStates {
		if h.repo.droppedStates[i].SeriesID != droppedTestSeriesA {
			h.repo.droppedStates[i].ProviderItemKey = "shared"
		}
	}
	h.store.drop(droppedTestSeriesB, h.at(1))
	h.store.drop(droppedTestSeriesC, h.at(1))
	client.listResponse = &pluginv1.WatchSyncListRemoteStateResponse{
		Items: []*pluginv1.WatchSyncRemoteState{droppedTombstone("forgotten"), droppedTombstone("shared")},
	}

	result, err := h.service.syncDropped(context.Background(), h.conn, ServerConfig{}, provider)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{droppedTestSeriesA, droppedTestSeriesB, droppedTestSeriesC} {
		if !h.store.active(id) || h.state(id) == nil {
			t.Fatalf("series %s lost its drop to an undrop that names no single series", id)
		}
	}
	if !slices.Equal(result.Warnings, []string{"watch sync provider returned an undrop that matches more than one series"}) {
		t.Fatalf("warnings = %#v, want only the ambiguous undrop reported", result.Warnings)
	}
}

func TestSyncRunSyncsDroppedThroughAPluginProvider(t *testing.T) {
	h, _, client := newPluginDroppedHarness(t)
	client.listResponse = &pluginv1.WatchSyncListRemoteStateResponse{
		CompleteSnapshot: true,
		Items:            []*pluginv1.WatchSyncRemoteState{remoteDroppedState("remote-a", "201", nil)},
		Warnings:         []string{"skipped 2 shows without ids"},
	}

	if err := h.service.SyncConnection(context.Background(), h.conn, "scheduled"); err != nil {
		t.Fatal(err)
	}

	if !h.store.active(droppedTestSeriesA) || !h.store.rows[droppedTestSeriesA].Equal(h.at(100)) {
		t.Fatalf("series A = %v, want imported at the sync time because the plugin sent no drop time", h.store.rows[droppedTestSeriesA])
	}
	if len(h.repo.syncRuns) != 1 || !strings.Contains(h.repo.syncRuns[0].Warning, "skipped 2 shows without ids") {
		t.Fatalf("runs = %#v, want the plugin's warning on the run", h.repo.syncRuns)
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

// An incremental read can report a series dropped and then undropped, or the
// other way round. The change later in the read wins.
func TestSyncDroppedPluginChangesApplyInReadOrder(t *testing.T) {
	for _, tc := range []struct {
		name        string
		items       func(h *droppedHarness) []*pluginv1.WatchSyncRemoteState
		wantDropped bool
	}{
		{
			name: "undrop after drop",
			items: func(h *droppedHarness) []*pluginv1.WatchSyncRemoteState {
				return []*pluginv1.WatchSyncRemoteState{remoteDroppedState("remote-b", "202", timestamppb.New(h.at(2))), droppedTombstone("remote-b")}
			},
		},
		{
			name: "drop after undrop",
			items: func(h *droppedHarness) []*pluginv1.WatchSyncRemoteState {
				return []*pluginv1.WatchSyncRemoteState{droppedTombstone("remote-b"), remoteDroppedState("remote-b", "202", timestamppb.New(h.at(2)))}
			},
			wantDropped: true,
		},
	} {
		for _, agreed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/agreed=%t", tc.name, agreed), func(t *testing.T) {
				h, provider, client := newPluginDroppedHarness(t)
				if agreed {
					h.store.drop(droppedTestSeriesB, h.at(1))
					h.agree(droppedTestSeriesB, true)
					for i := range h.repo.droppedStates {
						if h.repo.droppedStates[i].SeriesID == droppedTestSeriesB {
							h.repo.droppedStates[i].ProviderItemKey = "remote-b"
						}
					}
				}
				client.listResponse = &pluginv1.WatchSyncListRemoteStateResponse{Items: tc.items(h), NextCursor: testCursorOne}
				if _, err := h.service.syncDropped(t.Context(), h.conn, ServerConfig{}, provider); err != nil {
					t.Fatal(err)
				}
				if h.store.active(droppedTestSeriesB) != tc.wantDropped {
					t.Fatalf("series B dropped = %t, want %t", h.store.active(droppedTestSeriesB), tc.wantDropped)
				}
				if (h.state(droppedTestSeriesB) != nil) != tc.wantDropped {
					t.Fatalf("series B agreement = %#v, want present = %t", h.state(droppedTestSeriesB), tc.wantDropped)
				}
				key := connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)
				if cursor := h.repo.connections[key].SyncCursors[pluginDroppedCursorKey]; cursor != testCursorOne {
					t.Fatalf("saved cursor = %q, want %q", cursor, testCursorOne)
				}
			})
		}
	}
}

func TestSyncDroppedPluginNewDropsWithAmbiguousTombstone(t *testing.T) {
	h, provider, client := newPluginDroppedHarness(t)
	client.listResponse = &pluginv1.WatchSyncListRemoteStateResponse{Items: []*pluginv1.WatchSyncRemoteState{
		remoteDroppedState("shared", "201", nil),
		remoteDroppedState("shared", "202", nil),
		droppedTombstone("shared"),
	}}
	result, err := h.service.syncDropped(t.Context(), h.conn, ServerConfig{}, provider)
	if err != nil {
		t.Fatal(err)
	}
	if !h.store.active(droppedTestSeriesA) || !h.store.active(droppedTestSeriesB) {
		t.Fatal("an ambiguous tombstone must not undo either newly observed drop")
	}
	if !slices.Equal(result.Warnings, []string{"watch sync provider returned an undrop that matches more than one series"}) {
		t.Fatalf("warnings = %#v, want the ambiguous undrop reported", result.Warnings)
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
