package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type preferenceTransactionTestProvider struct {
	store userstore.UserStore
}

func (p preferenceTransactionTestProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (preferenceTransactionTestProvider) Close() error { return nil }

func TestInterestTrackingStorePreservesSettingCapabilities(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	provider := WrapUserStoreProvider(
		preferenceTransactionTestProvider{store: userdb.NewSQLiteUserStore(db)},
		&System{},
	)
	wrapped, err := provider.ForUser(context.Background(), 1)
	if err != nil {
		t.Fatalf("ForUser: %v", err)
	}
	transactioner, ok := wrapped.(userstore.PreferenceSettingsTransactioner)
	if !ok {
		t.Fatal("interest-tracking wrapper dropped PreferenceSettingsTransactioner")
	}

	called := false
	if err := transactioner.WithPreferenceSettingsTransaction(context.Background(),
		func(userstore.PreferenceSettingsWriter) error {
			called = true
			return nil
		}); err != nil {
		t.Fatalf("WithPreferenceSettingsTransaction: %v", err)
	}
	if !called {
		t.Fatal("transaction callback was not invoked")
	}

	cas, ok := wrapped.(userstore.SettingValueCompareAndSetter)
	if !ok {
		t.Fatal("interest-tracking wrapper dropped SettingValueCompareAndSetter")
	}
	identity := userstore.SettingIdentity{Key: "ui.test", Scope: settingscontract.ScopeAccount}
	first, err := cas.CompareAndSetSettingValue(
		context.Background(), identity, json.RawMessage(`"first"`), 0)
	if err != nil {
		t.Fatalf("CompareAndSetSettingValue: %v", err)
	}

	mutationTx, ok := wrapped.(userstore.SettingMutationTransactioner)
	if !ok {
		t.Fatal("interest-tracking wrapper dropped SettingMutationTransactioner")
	}
	called = false
	if err := mutationTx.WithSettingMutationTransaction(context.Background(), "wrapped-mutation",
		func(writer userstore.SettingMutationWriter) error {
			called = true
			if _, err := writer.CompareAndSetSettingValue(
				context.Background(), identity, json.RawMessage(`"second"`), first.Revision); err != nil {
				return err
			}
			_, _, err := writer.PutSettingMutation(context.Background(), userstore.SettingMutationRecord{
				MutationID: "wrapped-mutation", RequestHash: "hash",
				Result: json.RawMessage(`{"ok":true}`), ExpiresAt: time.Now().UTC().Add(time.Hour),
			})
			return err
		}); err != nil {
		t.Fatalf("WithSettingMutationTransaction: %v", err)
	}
	if !called {
		t.Fatal("mutation transaction callback was not invoked")
	}
	stored, err := wrapped.GetSettingValue(context.Background(), identity)
	if err != nil || stored == nil || stored.Revision != 2 {
		t.Fatalf("wrapped setting after transaction = %+v (%v), want revision 2", stored, err)
	}
	receipt, err := wrapped.GetSettingMutation(context.Background(), "wrapped-mutation")
	if err != nil || receipt == nil || receipt.RequestHash != "hash" {
		t.Fatalf("wrapped receipt = %+v (%v)", receipt, err)
	}
}

// TestInterestTrackingStorePreservesWatchStateCapabilities pins the optional
// watch-state capabilities through the wrapper. These are type-asserted at
// their call sites (userstore.MarkWatchedBatch, AddVisibleHistory,
// VisibleHistoryTimestamps, jellycompat's rollup), and the decorator embeds
// the UserStore *interface* — which promotes only the methods in that
// interface. A capability the decorator does not forward explicitly is
// therefore invisible in production, and the caller silently degrades to its
// slow per-item fallback with no error and no test failure.
func TestInterestTrackingStorePreservesWatchStateCapabilities(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	inner := userdb.NewSQLiteUserStore(db)
	provider := WrapUserStoreProvider(
		preferenceTransactionTestProvider{store: inner},
		&System{},
	)
	wrapped, err := provider.ForUser(context.Background(), 1)
	if err != nil {
		t.Fatalf("ForUser: %v", err)
	}

	// Every capability the bare store advertises must survive wrapping,
	// otherwise the wrapped store is a silent performance downgrade.
	for _, capability := range []struct {
		name string
		has  func(userstore.UserStore) bool
	}{
		{"WatchedBatchWriter", func(s userstore.UserStore) bool {
			_, ok := s.(userstore.WatchedBatchWriter)
			return ok
		}},
		{"VisibleHistoryAdder", func(s userstore.UserStore) bool {
			_, ok := s.(userstore.VisibleHistoryAdder)
			return ok
		}},
		{"HistoryVisibilityStore", func(s userstore.UserStore) bool {
			_, ok := s.(userstore.HistoryVisibilityStore)
			return ok
		}},
	} {
		if !capability.has(inner) {
			t.Fatalf("test setup: bare store does not implement %s", capability.name)
		}
		if !capability.has(wrapped) {
			t.Errorf("interest-tracking wrapper dropped %s; callers silently fall back to the per-item path",
				capability.name)
		}
	}

	// SeriesEpisodeRollupStore is the exception: it must be advertised only
	// when the backing store can actually answer it. The per-user SQLite store
	// has no catalog tables, so claiming the capability would send every
	// jellycompat series request down a fast path that can only fail and log.
	if _, ok := userstore.UserStore(inner).(userstore.SeriesEpisodeRollupStore); ok {
		t.Fatal("test setup: SQLite store unexpectedly implements SeriesEpisodeRollupStore")
	}
	if _, ok := wrapped.(userstore.SeriesEpisodeRollupStore); ok {
		t.Error("wrapper advertises SeriesEpisodeRollupStore for a store that cannot perform the rollup")
	}

	// The forwarded batch write must actually reach the backing store.
	writer, ok := wrapped.(userstore.WatchedBatchWriter)
	if !ok {
		t.Fatal("wrapper dropped WatchedBatchWriter; cannot verify pass-through")
	}
	if err := wrapped.CreateProfile(context.Background(), userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	watchedAt := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	written, err := writer.MarkWatchedBatch(context.Background(), "p1",
		[]userstore.MarkWatchedTarget{
			{MediaItemID: "ep-1", DurationSeconds: 1200},
			{MediaItemID: "ep-2", DurationSeconds: 1500},
		},
		[]userstore.WatchHistoryEntry{
			{ProfileID: "p1", MediaItemID: "ep-1", WatchedAt: watchedAt, DurationSeconds: 1200, Completed: true, Source: userstore.WatchHistorySourceManual},
			{ProfileID: "p1", MediaItemID: "ep-2", WatchedAt: watchedAt, DurationSeconds: 1500, Completed: true, Source: userstore.WatchHistorySourceManual},
		},
	)
	if err != nil {
		t.Fatalf("MarkWatchedBatch through wrapper: %v", err)
	}
	if len(written) != 2 {
		t.Fatalf("MarkWatchedBatch returned %d entries, want 2", len(written))
	}
	for _, id := range []string{"ep-1", "ep-2"} {
		progress, err := wrapped.GetProgress(context.Background(), "p1", id)
		if err != nil || progress == nil || !progress.Completed {
			t.Fatalf("GetProgress(%s) = %+v (%v), want completed", id, progress, err)
		}
	}
}

// storeWithoutBatchWriter wraps a UserStore and hides the WatchedBatchWriter
// capability, standing in for a backend that has not implemented it.
type storeWithoutBatchWriter struct {
	userstore.UserStore
}

// TestInterestTrackingStoreQueuesMutationsOnBatchFallback covers the branch
// taken when the backing store lacks WatchedBatchWriter. The decorator hands
// the work to the generic helper against s.UserStore — the *inner* store — so
// the decorator's own MarkWatched hook never runs and nothing queues an
// interest recompute. Without this, marking a series watched on such a backend
// updates progress and history but leaves profile_series_interest stale until
// some unrelated mutation or the rebuild task happens to touch the series.
func TestInterestTrackingStoreQueuesMutationsOnBatchFallback(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	inner := storeWithoutBatchWriter{UserStore: userdb.NewSQLiteUserStore(db)}
	if _, ok := userstore.UserStore(inner).(userstore.WatchedBatchWriter); ok {
		t.Fatal("test setup: inner store must not implement WatchedBatchWriter")
	}

	updater := &InterestUpdater{pending: map[interestMutation]int{}}
	store := &interestTrackingStore{
		UserStore: inner,
		userID:    1,
		system:    &System{},
		updater:   updater,
	}
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	watchedAt := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if _, err := store.MarkWatchedBatch(context.Background(), "p1",
		[]userstore.MarkWatchedTarget{
			{MediaItemID: "ep-1", DurationSeconds: 1200},
			{MediaItemID: "ep-2", DurationSeconds: 1500},
		},
		[]userstore.WatchHistoryEntry{
			{ProfileID: "p1", MediaItemID: "ep-1", WatchedAt: watchedAt, DurationSeconds: 1200, Completed: true, Source: userstore.WatchHistorySourceManual},
			{ProfileID: "p1", MediaItemID: "ep-2", WatchedAt: watchedAt, DurationSeconds: 1500, Completed: true, Source: userstore.WatchHistorySourceManual},
		},
	); err != nil {
		t.Fatalf("MarkWatchedBatch (fallback path): %v", err)
	}

	updater.mu.Lock()
	queued := make(map[string]struct{}, len(updater.pending))
	for mutation := range updater.pending {
		queued[mutation.itemID] = struct{}{}
	}
	updater.mu.Unlock()

	for _, itemID := range []string{"ep-1", "ep-2"} {
		if _, ok := queued[itemID]; !ok {
			t.Errorf("no interest mutation queued for %s on the batch fallback path; interest goes stale", itemID)
		}
	}
}

// rollupCapableStore is a UserStore that also implements the series rollup,
// standing in for the Postgres backend.
type rollupCapableStore struct {
	userstore.UserStore
	called bool
}

func (s *rollupCapableStore) SeriesEpisodeWatchCounts(_ context.Context, _ string, seriesIDs []string) (map[string]userstore.SeriesWatchCounts, error) {
	s.called = true
	counts := make(map[string]userstore.SeriesWatchCounts, len(seriesIDs))
	for _, seriesID := range seriesIDs {
		counts[seriesID] = userstore.SeriesWatchCounts{TotalEpisodes: 3, WatchedCount: 2}
	}
	return counts, nil
}

// TestInterestTrackingStoreForwardsRollupWhenSupported is the other half of the
// conditional: a backend that can do the rollup must keep advertising it
// through the wrapper, and calls must reach it. Losing this would silently
// push jellycompat back onto the per-episode rollup for every series.
func TestInterestTrackingStoreForwardsRollupWhenSupported(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	inner := &rollupCapableStore{UserStore: userdb.NewSQLiteUserStore(db)}
	provider := WrapUserStoreProvider(preferenceTransactionTestProvider{store: inner}, &System{})
	wrapped, err := provider.ForUser(context.Background(), 1)
	if err != nil {
		t.Fatalf("ForUser: %v", err)
	}

	rollup, ok := wrapped.(userstore.SeriesEpisodeRollupStore)
	if !ok {
		t.Fatal("wrapper dropped SeriesEpisodeRollupStore for a store that supports it")
	}
	counts, err := rollup.SeriesEpisodeWatchCounts(context.Background(), "p1", []string{"series-1"})
	if err != nil {
		t.Fatalf("SeriesEpisodeWatchCounts: %v", err)
	}
	if !inner.called {
		t.Error("rollup call did not reach the backing store")
	}
	if counts["series-1"].WatchedCount != 2 {
		t.Errorf("counts = %+v, want WatchedCount 2", counts["series-1"])
	}

	// The other capabilities must still survive alongside the rollup.
	if _, ok := wrapped.(userstore.WatchedBatchWriter); !ok {
		t.Error("rollup-capable wrapper dropped WatchedBatchWriter")
	}
	if _, ok := wrapped.(userstore.SettingValueCompareAndSetter); !ok {
		t.Error("rollup-capable wrapper dropped SettingValueCompareAndSetter")
	}
}
