package watchsync

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	ratingTestUserID    = 7
	ratingTestProfileID = "profile-1"
	ratingTestConnID    = "conn-ratings"
	ratingTestMovieA    = "movie-a"
	ratingTestMovieB    = "movie-b"
	ratingTestSeries    = "series-a"
)

func TestSyncRatingsConcurrentLocalEditWins(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 8)
	h.agree(ratingTestMovieA, 8, true)
	h.store.conflicts[ratingTestMovieA] = true // the user edits between read and write
	h.provider.batch = RatingImportBatch{
		Rows:          []RemoteRating{h.remoteRow(ratingTestMovieA, 2)},
		SnapshotKinds: []string{historyimport.KindMovie},
	}

	result := h.sync()

	if result.Imported != 0 {
		t.Fatalf("a lost compare-and-set must not count as imported: %#v", result)
	}
	if s := h.state(ratingTestMovieA); s == nil || s.SyncedRating != 8 {
		t.Fatalf("a lost compare-and-set must leave the agreed rating: %#v", s)
	}
}

func TestSyncRatingsRateLimitStopsSending(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 8)
	h.provider.exportErr = RateLimitedError{Provider: "test", RetryAfter: time.Minute}

	_, err := h.service.syncRatings(context.Background(), h.conn, ServerConfig{}, h.provider)

	if _, ok := AsRateLimited(err); !ok {
		t.Fatalf("err = %v, want rate limited", err)
	}
	if h.state(ratingTestMovieA) != nil {
		t.Fatal("a rate-limited rating stays pending")
	}
}

func TestSyncRatingsIgnoresAgreedRatingsOfAnotherAccount(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 8)
	// Agreed with the connection's previous account, then absent here.
	h.repo.ratingStates = append(h.repo.ratingStates, RatingSyncState{
		ConnectionID: h.conn.ID, ProviderAccountID: "old-account", MediaItemID: ratingTestMovieA,
		Kind: historyimport.KindMovie, ProviderItemKey: "tmdb:101", SyncedRating: 8, RemoteSeen: true,
	})
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieB, 6)}, SnapshotKinds: []string{historyimport.KindMovie}}
	h.sync()
	if got := h.store.score(ratingTestMovieA); got != 8 {
		t.Fatalf("movie A = %d; another account's agreement must not delete it", got)
	}
	if len(h.provider.exported) != 1 || h.provider.exported[0].MediaItemID != ratingTestMovieA {
		t.Fatalf("exported = %#v, want movie A sent to the new account", h.provider.exported)
	}
}

func TestDeleteConnectionWaitsForTheRatingSyncLock(t *testing.T) {
	h := newRatingHarness(t)
	if err := h.service.DeleteConnection(context.Background(), h.conn.UserID, h.conn.ProfileID, h.conn.Provider); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.repo.ratingLocks, []string{"wait:" + ratingTestConnID}) {
		t.Fatalf("rating sync locks = %v, want the disconnect to wait for the lock", h.repo.ratingLocks)
	}
	if len(h.repo.connections) != 0 {
		t.Fatal("the connection was not deleted")
	}
}

func TestSyncRatingsLeavesRatingsToARunHoldingTheLock(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 6)
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieB, 8)}, SnapshotKinds: []string{historyimport.KindMovie}}
	// Another node is reconciling this connection's ratings.
	h.repo.ratingLockBusy = map[string]bool{ratingTestConnID: true}
	result := h.sync()
	if h.provider.fetches != 0 || len(h.provider.exported) != 0 || h.store.score(ratingTestMovieB) != 0 {
		t.Fatalf("a run without the lock touched ratings: fetches=%d exported=%v movieB=%d", h.provider.fetches, h.provider.exported, h.store.score(ratingTestMovieB))
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "already syncing") {
		t.Fatalf("warnings = %v, want the skip reported", result.Warnings)
	}
	h.repo.ratingLockBusy = nil
	h.sync()
	if h.provider.fetches != 1 || h.store.score(ratingTestMovieB) != 8 {
		t.Fatalf("fetches=%d movieB=%d, want the next run to sync", h.provider.fetches, h.store.score(ratingTestMovieB))
	}
	if !slices.Contains(h.repo.ratingLocks, "try:"+ratingTestConnID) {
		t.Fatalf("rating sync locks = %v, want the scheduled run to try the lock", h.repo.ratingLocks)
	}
}

// --- harness ---

type ratingHarness struct {
	t        *testing.T
	repo     *serviceFakeRepo
	store    *fakeRatingStore
	provider *ratingProviderStub
	service  *Service
	conn     Connection
	media    map[string]LocalFavorite
	watched  map[string]bool
	signals  *recordingSignalsNotifier
}

// recordingSignalsNotifier records signal change notifications.
type recordingSignalsNotifier struct {
	calls     int
	userID    int
	profileID string
	ctxErrs   []error
}

func (n *recordingSignalsNotifier) NotifySignalsChanged(ctx context.Context, userID int, profileID string) {
	n.calls++
	n.userID, n.profileID = userID, profileID
	n.ctxErrs = append(n.ctxErrs, ctx.Err())
}

func newRatingHarness(t *testing.T) *ratingHarness {
	t.Helper()
	h := &ratingHarness{
		t:        t,
		repo:     newServiceFakeRepo(),
		store:    newFakeRatingStore(),
		provider: &ratingProviderStub{},
		watched:  map[string]bool{},
		signals:  &recordingSignalsNotifier{},
		media: map[string]LocalFavorite{
			ratingTestMovieA: {MediaItemID: ratingTestMovieA, Kind: historyimport.KindMovie, IMDbID: "tt0101", TMDBID: "101", ProviderItemKey: "tmdb:101"},
			ratingTestMovieB: {MediaItemID: ratingTestMovieB, Kind: historyimport.KindMovie, TMDBID: "102", ProviderItemKey: "tmdb:102"},
			ratingTestSeries: {MediaItemID: ratingTestSeries, Kind: historyimport.KindSeries, TMDBID: "101", ProviderItemKey: "tmdb:101"},
		},
	}
	h.repo.listMedia = h.media
	h.conn = Connection{
		ID: ratingTestConnID, Provider: h.provider.Key(), UserID: ratingTestUserID, ProfileID: ratingTestProfileID,
		AccessToken: "token", ProviderAccountID: "acct", ImportRatingsEnabled: true, ExportRatingsEnabled: true,
	}
	h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)] = h.conn
	registry := NewRegistry()
	if err := registry.Register(h.provider); err != nil {
		t.Fatal(err)
	}
	h.service = NewService(h.repo, registry).
		WithMatcher(ratingMatcherStub{media: h.media}).
		WithUserStoreProvider(staticStoreProvider{store: ratingHistoryStore{watched: h.watched}}).
		WithRatingStore(h.store).
		WithSignalsChangedNotifier(h.signals)
	return h
}

func (h *ratingHarness) sync() SyncRatingsResult {
	h.t.Helper()
	h.provider.exported, h.provider.removed = nil, nil
	// The sync re-reads the connection under its lock, so it must see the
	// toggles this test set.
	h.repo.connections[connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)] = h.conn
	result, err := h.service.syncRatings(context.Background(), h.conn, ServerConfig{}, h.provider)
	if err != nil {
		h.t.Fatal(err)
	}
	return result
}

func (h *ratingHarness) remoteRow(mediaItemID string, rating int) RemoteRating {
	item := h.media[mediaItemID]
	return RemoteRating{
		RemoteFavorite: RemoteFavorite{Provider: "test", ProviderItemKey: item.ProviderItemKey, Kind: item.Kind, TMDBID: item.TMDBID},
		Rating:         rating,
		RatedAt:        time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}
}

func (h *ratingHarness) agree(mediaItemID string, score int, seen bool) {
	item := h.media[mediaItemID]
	_ = h.repo.UpsertRatingSyncStates(context.Background(), []RatingSyncState{{
		ConnectionID: h.conn.ID, ProviderAccountID: h.conn.ProviderAccountID, MediaItemID: mediaItemID, Kind: item.Kind,
		ProviderItemKey: item.ProviderItemKey, SyncedRating: score, RemoteSeen: seen,
	}})
}

func (h *ratingHarness) state(mediaItemID string) *RatingSyncState {
	for i := range h.repo.ratingStates {
		if h.repo.ratingStates[i].MediaItemID == mediaItemID {
			return &h.repo.ratingStates[i]
		}
	}
	return nil
}

type fakeRatingStore struct {
	ratings   map[string]catalog.UserRating
	conflicts map[string]bool
	clock     time.Time
	// afterWrite runs after each applied import write when set.
	afterWrite func()
}

func newFakeRatingStore() *fakeRatingStore {
	return &fakeRatingStore{
		ratings:   map[string]catalog.UserRating{},
		conflicts: map[string]bool{},
		clock:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func (s *fakeRatingStore) set(id string, score int) {
	s.clock = s.clock.Add(time.Minute)
	s.ratings[id] = catalog.UserRating{UserID: ratingTestUserID, ProfileID: ratingTestProfileID, MediaItemID: id, Score: score, RatedAt: s.clock}
}

func (s *fakeRatingStore) remove(id string) { delete(s.ratings, id) }

func (s *fakeRatingStore) score(id string) int { return s.ratings[id].Score }

func (s *fakeRatingStore) matches(id string, observed catalog.ObservedRating) bool {
	current, ok := s.ratings[id]
	if observed.Score == 0 {
		return !ok
	}
	return ok && current.Score == observed.Score && current.RatedAt.Equal(observed.RatedAt)
}

func (s *fakeRatingStore) ListAll(_ context.Context, _ int, _ string) ([]catalog.UserRating, error) {
	all := make([]catalog.UserRating, 0, len(s.ratings))
	for _, rating := range s.ratings {
		all = append(all, rating)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].MediaItemID < all[j].MediaItemID })
	return all, nil
}

func (s *fakeRatingStore) Get(_ context.Context, _ int, _ string, id string) (*catalog.UserRating, error) {
	rating, ok := s.ratings[id]
	if !ok {
		return nil, nil
	}
	return &rating, nil
}

func (s *fakeRatingStore) SetIfUnchanged(_ context.Context, _ int, _ string, id string, observed catalog.ObservedRating, rating int, ratedAt time.Time) (bool, error) {
	if s.conflicts[id] || !s.matches(id, observed) {
		return false, nil
	}
	s.ratings[id] = catalog.UserRating{UserID: ratingTestUserID, ProfileID: ratingTestProfileID, MediaItemID: id, Score: rating, RatedAt: ratedAt}
	if s.afterWrite != nil {
		s.afterWrite()
	}
	return true, nil
}

func (s *fakeRatingStore) DeleteIfUnchanged(_ context.Context, _ int, _ string, id string, observed catalog.ObservedRating) (bool, error) {
	if s.conflicts[id] || !s.matches(id, observed) {
		return false, nil
	}
	delete(s.ratings, id)
	return true, nil
}

// ratingMatcherStub matches remote rows by kind and TMDB id.
type ratingMatcherStub struct {
	media map[string]LocalFavorite
}

func (m ratingMatcherStub) Match(_ context.Context, record historyimport.Record) (*historyimport.Match, string, error) {
	for _, item := range m.media {
		if record.TMDBID != "" && item.Kind == record.Kind && item.TMDBID == record.TMDBID {
			return &historyimport.Match{MediaItemID: item.MediaItemID}, "", nil
		}
	}
	return nil, "no match", nil
}

// ratingHistoryStore answers completed-history lookups for the watch gate.
type ratingHistoryStore struct {
	userstore.UserStore
	watched map[string]bool
}

func (s ratingHistoryStore) ListCompletedHistory(_ context.Context, query userstore.CompletedHistoryQuery) ([]userstore.WatchHistoryEntry, error) {
	if query.After != nil {
		return nil, nil
	}
	var rows []userstore.WatchHistoryEntry
	for _, id := range query.MediaItemIDs {
		if s.watched[id] {
			rows = append(rows, userstore.WatchHistoryEntry{MediaItemID: id})
		}
	}
	return rows, nil
}

type ratingProviderStub struct {
	batch      RatingImportBatch
	fetches    int
	exported   []LocalRating
	removed    []LocalFavorite
	exportErr  error
	gateMovies bool
	onExport   func()
	onRemove   func()
	onFetch    func()
	// fetchedCursors are the cursors the last FetchRatings call received.
	fetchedCursors map[string]string
	// kinds limits the rated kinds when set; nil rates every kind.
	kinds map[string]bool
}

func (p *ratingProviderStub) SyncsRatingKind(kind string) bool {
	return p.kinds == nil || p.kinds[kind]
}

func (*ratingProviderStub) Key() string         { return "test" }
func (*ratingProviderStub) DisplayName() string { return "Test" }
func (*ratingProviderStub) Capabilities() Capabilities {
	return Capabilities{ImportRatings: true, ExportRatings: true}
}

func (*ratingProviderStub) ConnectWithAPIKey(context.Context, string) (TokenSet, ProviderAccount, error) {
	return TokenSet{}, ProviderAccount{}, nil
}

func (p *ratingProviderStub) FetchRatings(_ context.Context, _ ServerConfig, conn Connection) (RatingImportBatch, error) {
	p.fetches++
	p.fetchedCursors = conn.SyncCursors
	if p.onFetch != nil {
		p.onFetch()
	}
	return p.batch, nil
}

func (p *ratingProviderStub) ExportRatings(_ context.Context, _ ServerConfig, _ Connection, items []LocalRating) (ExportResult, error) {
	if p.exportErr != nil {
		return ExportResult{}, p.exportErr
	}
	p.exported = append(p.exported, items...)
	if p.onExport != nil {
		p.onExport()
	}
	var result ExportResult
	for _, item := range items {
		result.Sent = append(result.Sent, item.MediaItemID, item.ProviderItemKey)
	}
	return result, nil
}

func (p *ratingProviderStub) RemoveRatings(_ context.Context, _ ServerConfig, _ Connection, items []LocalFavorite) (ExportResult, error) {
	p.removed = append(p.removed, items...)
	if p.onRemove != nil {
		p.onRemove()
	}
	var result ExportResult
	for _, item := range items {
		result.Sent = append(result.Sent, item.MediaItemID, item.ProviderItemKey)
	}
	return result, nil
}

func (p *ratingProviderStub) RatingExportRequiresWatched(kind string) bool {
	return p.gateMovies && kind == historyimport.KindMovie
}

func TestSyncRatingsSkipsApplyingAfterAnAccountRebind(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 6)
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieB, 7)}, SnapshotKinds: []string{historyimport.KindMovie}}
	// The connection is re-bound while the provider read is in flight.
	h.provider.onFetch = func() {
		key := connectionKey(h.conn.Provider, h.conn.UserID, h.conn.ProfileID)
		rebound := h.repo.connections[key]
		rebound.ProviderAccountID = "another-account"
		h.repo.connections[key] = rebound
	}

	result := h.sync()

	if h.store.score(ratingTestMovieB) != 0 || len(h.provider.exported) != 0 || len(h.repo.ratingStates) != 0 {
		t.Fatalf("a stale run must not apply ratings: movieB=%d exported=%#v states=%#v",
			h.store.score(ratingTestMovieB), h.provider.exported, h.repo.ratingStates)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("a skipped stale run should warn")
	}
}

func TestSyncRatingsSendsARemovalMadeDuringTheLastResend(t *testing.T) {
	h := newRatingHarness(t)
	h.agree(ratingTestMovieA, 2, true)
	h.store.set(ratingTestMovieA, 4)
	// The edits keep coming, and the last one removes the rating.
	sends, score := 0, 2
	h.provider.onExport = func() {
		sends++
		if sends > maxRatingResends {
			h.store.remove(ratingTestMovieA)
			return
		}
		score = score%10 + 1
		h.store.set(ratingTestMovieA, score)
	}
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieA, 2)}, SnapshotKinds: []string{historyimport.KindMovie}}
	h.sync()
	if len(h.provider.exported) != 1+maxRatingResends {
		t.Fatalf("exports = %d, want the first send and %d resends", len(h.provider.exported), maxRatingResends)
	}
	last := h.provider.exported[len(h.provider.exported)-1].Rating

	// The provider now holds the last sent rating. The next run must send the
	// removal rather than import that rating back.
	h.provider.onExport = nil
	h.provider.batch = RatingImportBatch{Rows: []RemoteRating{h.remoteRow(ratingTestMovieA, last)}, SnapshotKinds: []string{historyimport.KindMovie}}
	h.sync()
	if got := h.store.score(ratingTestMovieA); got != 0 {
		t.Fatalf("local rating = %d, want the removal kept", got)
	}
	if len(h.provider.removed) != 1 {
		t.Fatalf("removed = %#v, want the removal sent", h.provider.removed)
	}
}

func TestPersistConnectionAccountChangeClearsAgreedRatings(t *testing.T) {
	repo := newServiceFakeRepo()
	existing := Connection{
		ID: ratingTestConnID, Provider: "test", UserID: 1, ProfileID: "p", ProviderAccountID: "old",
		SyncCursors: map[string]string{"test.ratings.movies": "c1", "test.watched": "w1"},
	}
	repo.connections[connectionKey("test", 1, "p")] = existing
	repo.ratingStates = []RatingSyncState{{ConnectionID: ratingTestConnID, MediaItemID: ratingTestMovieA, SyncedRating: 8, RemoteSeen: true}}
	service := NewService(repo, NewRegistry())

	conn, err := service.persistConnection(context.Background(), "test", 1, "p", TokenSet{AccessToken: "a"}, ProviderAccount{ID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.ratingStates) != 0 {
		t.Fatalf("agreed ratings survived an account change: %#v", repo.ratingStates)
	}
	if conn.SyncCursors["test.ratings.movies"] != "" || conn.SyncCursors["test.watched"] != "w1" {
		t.Fatalf("cursors = %#v, want only rating cursors dropped", conn.SyncCursors)
	}

	// Reconnecting the same account keeps the agreement.
	repo.ratingStates = []RatingSyncState{{ConnectionID: ratingTestConnID, MediaItemID: ratingTestMovieA, SyncedRating: 8, RemoteSeen: true}}
	if _, err := service.persistConnection(context.Background(), "test", 1, "p", TokenSet{AccessToken: "a"}, ProviderAccount{ID: "new"}); err != nil {
		t.Fatal(err)
	}
	if len(repo.ratingStates) != 1 {
		t.Fatal("reconnecting the same account must keep agreed ratings")
	}
	// Only the switch waited for the rating sync lock.
	if !slices.Equal(repo.ratingLocks, []string{"wait:" + ratingTestConnID}) {
		t.Fatalf("rating sync locks = %v, want one wait by the account switch", repo.ratingLocks)
	}
}

func TestSyncRatingsEmptySnapshotDoesNotDelete(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 8)
	h.store.set(ratingTestMovieB, 4)
	h.agree(ratingTestMovieA, 8, true)
	h.agree(ratingTestMovieB, 4, true)
	h.provider.batch = RatingImportBatch{SnapshotKinds: []string{historyimport.KindMovie}}

	result := h.sync()

	if h.store.score(ratingTestMovieA) != 8 || h.store.score(ratingTestMovieB) != 4 {
		t.Fatalf("an empty snapshot must not delete several ratings: A=%d B=%d", h.store.score(ratingTestMovieA), h.store.score(ratingTestMovieB))
	}
	if len(result.Warnings) == 0 {
		t.Fatal("an ignored empty snapshot should warn")
	}
}

func TestSyncRatingsRecoversExactValuesAfterTheScoreMigration(t *testing.T) {
	h := newRatingHarness(t)
	h.store.set(ratingTestMovieA, 8)
	h.agree(ratingTestMovieA, 8, true)
	h.store.set(ratingTestMovieB, 6)
	h.agree(ratingTestMovieB, 6, true)
	h.provider.batch = RatingImportBatch{
		Rows:          []RemoteRating{h.remoteRow(ratingTestMovieA, 7), h.remoteRow(ratingTestMovieB, 6)},
		SnapshotKinds: []string{historyimport.KindMovie},
	}

	h.sync()

	if len(h.provider.exported) != 0 || len(h.provider.removed) != 0 {
		t.Fatalf("recovery must not write to the provider; writes = %#v / %#v", h.provider.exported, h.provider.removed)
	}
	if h.store.score(ratingTestMovieA) != 7 || h.store.score(ratingTestMovieB) != 6 {
		t.Fatalf("scores A=%d B=%d, want 7 and 6", h.store.score(ratingTestMovieA), h.store.score(ratingTestMovieB))
	}
}
