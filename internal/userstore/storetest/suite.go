// Package storetest provides a shared conformance test suite for UserStore
// implementations. Both SQLite and Postgres backends run these same tests.
package storetest

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunMarkWatchedBatch checks series and season mark-watched behavior on each backend.
func RunMarkWatchedBatch(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	t.Run("MarkWatchedBatch", func(t *testing.T) {
		testMarkWatchedBatch(t, newStore)
	})
}

// RunProgressSince checks offline-sync progress reconciliation and event ordering.
func RunProgressSince(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	t.Run("DeltaCursor", func(t *testing.T) {
		testProgressSince(t, newStore)
	})
	t.Run("OnlineWriteAdvancesEventAt", func(t *testing.T) {
		testOnlineWriteAdvancesEventAt(t, newStore)
	})
	t.Run("BatchWritesAdvanceEventAt", func(t *testing.T) {
		testBatchWritesAdvanceEventAt(t, newStore)
	})
}

// RunCollectionSortPreferences runs the preference timestamp and profile
// lifecycle conformance checks against a UserStore implementation.
func RunCollectionSortPreferences(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	t.Run("TimestampAndProfileLifecycle", func(t *testing.T) {
		testCollectionSortPreferences(t, newStore)
	})
}

func testCollectionSortPreferences(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)
	const (
		profileID       = "sort-pref-profile"
		viewerProfileID = "sort-pref-viewer"
		titleSortField  = "title"
		ascendingOrder  = "asc"
	)

	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Sort Pref"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: viewerProfileID, Name: "Sort Pref Viewer"}); err != nil {
		t.Fatalf("CreateProfile(viewer): %v", err)
	}
	if err := store.SetCollectionSortPreference(ctx, userstore.CollectionSortPreference{
		ProfileID:      profileID,
		CollectionKind: userstore.CollectionKindLibrary,
		CollectionID:   "collection-1",
		SortField:      titleSortField,
		SortOrder:      ascendingOrder,
	}); err != nil {
		t.Fatalf("SetCollectionSortPreference: %v", err)
	}
	pref, err := store.GetCollectionSortPreference(ctx, profileID, userstore.CollectionKindLibrary, "collection-1")
	if err != nil || pref == nil {
		t.Fatalf("GetCollectionSortPreference = %v, err = %v", pref, err)
	}
	if _, err := time.Parse(time.RFC3339, pref.UpdatedAt); err != nil {
		t.Fatalf("UpdatedAt = %q, want RFC3339 timestamp: %v", pref.UpdatedAt, err)
	}

	for _, kind := range []string{userstore.CollectionKindWatchlist, userstore.CollectionKindFavorites} {
		if err := store.SetCollectionSortPreference(ctx, userstore.CollectionSortPreference{
			ProfileID:      profileID,
			CollectionKind: kind,
			CollectionID:   userstore.PersonalSortPreferenceCollectionID,
			SortField:      "added_at",
			SortOrder:      "desc",
		}); err != nil {
			t.Fatalf("SetCollectionSortPreference(%s): %v", kind, err)
		}
		personalPref, err := store.GetCollectionSortPreference(ctx, profileID, kind, userstore.PersonalSortPreferenceCollectionID)
		if err != nil || personalPref == nil || personalPref.SortField != "added_at" {
			t.Fatalf("GetCollectionSortPreference(%s) = %+v, err = %v", kind, personalPref, err)
		}
	}

	for _, invalid := range []userstore.CollectionSortPreference{
		{
			ProfileID:      profileID,
			CollectionKind: "invalid",
			CollectionID:   "collection-invalid-kind",
			SortField:      titleSortField,
			SortOrder:      ascendingOrder,
		},
		{
			ProfileID:      profileID,
			CollectionKind: userstore.CollectionKindLibrary,
			CollectionID:   "collection-invalid-order",
			SortField:      titleSortField,
			SortOrder:      "sideways",
		},
	} {
		if err := store.SetCollectionSortPreference(ctx, invalid); err == nil {
			t.Fatalf("SetCollectionSortPreference accepted invalid preference: %+v", invalid)
		}
	}

	deletedCollection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: profileID,
		Name:             "Deleted sort preference target",
	})
	if err != nil {
		t.Fatalf("CreateCollection(delete target): %v", err)
	}
	if err := store.SetCollectionSortPreference(ctx, userstore.CollectionSortPreference{
		ProfileID:      viewerProfileID,
		CollectionKind: userstore.CollectionKindUser,
		CollectionID:   deletedCollection.ID,
		SortField:      "year",
		SortOrder:      "desc",
	}); err != nil {
		t.Fatalf("SetCollectionSortPreference(delete target): %v", err)
	}
	if err := store.DeleteCollection(ctx, deletedCollection.ID); err != nil {
		t.Fatalf("DeleteCollection: %v", err)
	}
	deletedPref, err := store.GetCollectionSortPreference(ctx, viewerProfileID, userstore.CollectionKindUser, deletedCollection.ID)
	if err != nil {
		t.Fatalf("GetCollectionSortPreference(deleted collection): %v", err)
	}
	if deletedPref != nil {
		t.Fatalf("preference survived collection deletion: %+v", deletedPref)
	}

	profileDeletedCollection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: profileID,
		Name:             "Profile-deleted sort preference target",
	})
	if err != nil {
		t.Fatalf("CreateCollection(profile delete target): %v", err)
	}
	if err := store.SetCollectionSortPreference(ctx, userstore.CollectionSortPreference{
		ProfileID:      viewerProfileID,
		CollectionKind: userstore.CollectionKindUser,
		CollectionID:   profileDeletedCollection.ID,
		SortField:      titleSortField,
		SortOrder:      ascendingOrder,
	}); err != nil {
		t.Fatalf("SetCollectionSortPreference(profile delete target): %v", err)
	}

	if err := store.DeleteProfile(ctx, profileID); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	deletedPref, err = store.GetCollectionSortPreference(ctx, viewerProfileID, userstore.CollectionKindUser, profileDeletedCollection.ID)
	if err != nil {
		t.Fatalf("GetCollectionSortPreference(profile-deleted collection): %v", err)
	}
	if deletedPref != nil {
		t.Fatalf("preference survived creator profile deletion: %+v", deletedPref)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: "Recreated"}); err != nil {
		t.Fatalf("CreateProfile(recreate): %v", err)
	}
	pref, err = store.GetCollectionSortPreference(ctx, profileID, userstore.CollectionKindLibrary, "collection-1")
	if err != nil {
		t.Fatalf("GetCollectionSortPreference(recreated profile): %v", err)
	}
	if pref != nil {
		t.Fatalf("stale preference survived profile recreation: %+v", pref)
	}
	for _, kind := range []string{userstore.CollectionKindWatchlist, userstore.CollectionKindFavorites} {
		pref, err = store.GetCollectionSortPreference(ctx, profileID, kind, userstore.PersonalSortPreferenceCollectionID)
		if err != nil {
			t.Fatalf("GetCollectionSortPreference(%s after profile recreation): %v", kind, err)
		}
		if pref != nil {
			t.Fatalf("%s preference survived profile recreation: %+v", kind, pref)
		}
	}
}

func cursorInt(t *testing.T, cursor string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil {
		t.Fatalf("cursor %q is not a server token: %v", cursor, err)
	}
	return n
}

// testProgressSince exercises invariant 1: cross-device delta delivery is driven
// only by the server-assigned synced_seq cursor, and a write that loses the
// event_at LWW (which is exactly what a clamped future-dated client write
// becomes — older than a later real write) never advances the cursor.
func testProgressSince(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour)

	if _, err := store.SetProgressIfNewer(ctx, "p1", "m1", 100, 1000, false, base); err != nil {
		t.Fatalf("write m1: %v", err)
	}
	if _, err := store.SetProgressIfNewer(ctx, "p1", "m2", 200, 1000, false, base.Add(time.Minute)); err != nil {
		t.Fatalf("write m2: %v", err)
	}

	// Full delta from an empty cursor returns both rows + a non-zero server token.
	all, cursor, err := store.ListProgressSince(ctx, "p1", "")
	if err != nil {
		t.Fatalf("ListProgressSince(empty): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("full delta = %d rows, want 2", len(all))
	}
	if cursorInt(t, cursor) <= 0 {
		t.Fatalf("next cursor = %q, want a positive server token", cursor)
	}

	// No further changes → empty delta, cursor unchanged.
	none, cursor2, err := store.ListProgressSince(ctx, "p1", cursor)
	if err != nil {
		t.Fatalf("ListProgressSince(cursor): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("empty delta = %d rows, want 0", len(none))
	}
	if cursorInt(t, cursor2) != cursorInt(t, cursor) {
		t.Fatalf("cursor advanced with no change: %q -> %q", cursor, cursor2)
	}

	// A new winning write appears in the delta and advances the cursor.
	if _, err := store.SetProgressIfNewer(ctx, "p1", "m3", 300, 1000, false, base.Add(2*time.Minute)); err != nil {
		t.Fatalf("write m3: %v", err)
	}
	delta, cursor3, err := store.ListProgressSince(ctx, "p1", cursor)
	if err != nil {
		t.Fatalf("ListProgressSince(after new): %v", err)
	}
	if len(delta) != 1 || delta[0].MediaItemID != "m3" {
		t.Fatalf("delta = %+v, want [m3]", delta)
	}
	if cursorInt(t, cursor3) <= cursorInt(t, cursor) {
		t.Fatalf("cursor did not advance: %q -> %q", cursor, cursor3)
	}

	// invariant 1: a stale write (older event_at) loses LWW AND never advances
	// the cursor — the same fate a clamped future-dated client write meets.
	if _, err := store.SetProgressIfNewer(ctx, "p1", "m3", 999, 1000, false, base); err != nil {
		t.Fatalf("stale write m3: %v", err)
	}
	got, err := store.GetProgress(ctx, "p1", "m3")
	if err != nil || got == nil || got.PositionSeconds != 300 {
		t.Fatalf("stale write won LWW: %+v (%v), want position 300", got, err)
	}
	stale, cursorStale, err := store.ListProgressSince(ctx, "p1", cursor3)
	if err != nil {
		t.Fatalf("ListProgressSince(after stale): %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("stale write delivered a delta: %+v", stale)
	}
	if cursorInt(t, cursorStale) != cursorInt(t, cursor3) {
		t.Fatalf("stale write advanced the cursor: %q -> %q", cursor3, cursorStale)
	}
}

// testOnlineWriteAdvancesEventAt locks in the LWW fix: a normal (online) write
// must advance the event_at comparison key, so a later offline replay whose
// client time predates the online write loses and cannot resurrect stale
// progress. Before the fix, online writes updated updated_at but left event_at
// frozen at the row's first write, letting almost any offline event win.
func testOnlineWriteAdvancesEventAt(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	noThreshold := userstore.ProgressThresholds{}
	// SetProgress and UpdateProgress share the online-write contract: both must
	// stamp event_at = now, so both are pinned here.
	onlineWrites := []struct {
		name  string
		write func(ctx context.Context, store userstore.UserStore) error
	}{
		{"SetProgress", func(ctx context.Context, store userstore.UserStore) error {
			return store.SetProgress(ctx, "p1", "m1", 500, 1000, noThreshold)
		}},
		{"UpdateProgress", func(ctx context.Context, store userstore.UserStore) error {
			return store.UpdateProgress(ctx, "p1", "m1", 500, 1000, noThreshold)
		}},
	}
	for _, tc := range onlineWrites {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := newStore(t)
			if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
				t.Fatalf("CreateProfile: %v", err)
			}
			base := time.Now().UTC().Add(-time.Hour)

			// An offline event seeds the row with an old event_at.
			if _, err := store.SetProgressIfNewer(ctx, "p1", "m1", 100, 1000, false, base); err != nil {
				t.Fatalf("seed offline write: %v", err)
			}
			// A live online write (no client time) must stamp event_at = now,
			// overtaking the seed's event_at.
			if err := tc.write(ctx, store); err != nil {
				t.Fatalf("online %s: %v", tc.name, err)
			}
			// A replayed offline event whose client time is newer than the seed but
			// older than the online write must NOT win.
			wrote, err := store.SetProgressIfNewer(ctx, "p1", "m1", 999, 1000, false, base.Add(5*time.Minute))
			if err != nil {
				t.Fatalf("stale offline replay: %v", err)
			}
			if wrote {
				t.Fatalf("stale offline replay reported a write; online event_at should have won")
			}
			got, err := store.GetProgress(ctx, "p1", "m1")
			if err != nil || got == nil {
				t.Fatalf("GetProgress: %+v (%v)", got, err)
			}
			if got.PositionSeconds != 500 {
				t.Fatalf("stale offline replay won LWW: position = %v, want 500 (online write)", got.PositionSeconds)
			}
		})
	}
}

// testMarkWatchedBatch pins the batch mark-watched path used by the series and
// season mark-watched handlers. It must be equivalent to a MarkWatched +
// AddVisibleHistory loop: per-target durations land on progress rows, exactly
// one history row appears per target, the hidden-history watermark still
// pushes a mark after a removal back into visibility, and a zero duration
// leaves a known duration alone (jellycompat's mark-played supplies none).
func testMarkWatchedBatch(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	const secondEpisodeID = "ep-2"
	ctx := context.Background()
	store := newStore(t)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	watchedAt := time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)
	targets := []userstore.MarkWatchedTarget{
		{MediaItemID: "ep-1", DurationSeconds: 1200},
		{MediaItemID: secondEpisodeID, DurationSeconds: 1500},
	}
	entries := []userstore.WatchHistoryEntry{
		{
			ProfileID:       "p1",
			MediaItemID:     "ep-1",
			WatchedAt:       watchedAt.Format(time.RFC3339),
			DurationSeconds: 1200,
			Completed:       true,
			Source:          userstore.WatchHistorySourceManual,
		},
		{
			ProfileID:       "p1",
			MediaItemID:     secondEpisodeID,
			WatchedAt:       watchedAt.Format(time.RFC3339),
			DurationSeconds: 1500,
			Completed:       true,
			Source:          userstore.WatchHistorySourceManual,
		},
	}

	written, err := userstore.MarkWatchedBatch(ctx, store, "p1", targets, entries)
	if err != nil {
		t.Fatalf("MarkWatchedBatch: %v", err)
	}
	if len(written) != 2 {
		t.Fatalf("MarkWatchedBatch returned %d entries, want 2", len(written))
	}
	for _, entry := range written {
		if entry.ID == "" {
			t.Fatalf("returned entry %s has no ID; callers relay these to watch providers", entry.MediaItemID)
		}
		if entry.WatchedAt == "" {
			t.Fatalf("returned entry %s has no WatchedAt", entry.MediaItemID)
		}
	}

	for _, want := range targets {
		progress, err := store.GetProgress(ctx, "p1", want.MediaItemID)
		if err != nil {
			t.Fatalf("GetProgress(%s): %v", want.MediaItemID, err)
		}
		if progress == nil || !progress.Completed {
			t.Fatalf("GetProgress(%s) = %+v, want completed", want.MediaItemID, progress)
		}
		if progress.DurationSeconds != want.DurationSeconds {
			t.Fatalf("GetProgress(%s) duration = %v, want %v — per-target durations must survive the batch",
				want.MediaItemID, progress.DurationSeconds, want.DurationSeconds)
		}
		if progress.PositionSeconds != 0 {
			t.Fatalf("GetProgress(%s) position = %v, want 0", want.MediaItemID, progress.PositionSeconds)
		}
	}

	history, err := store.ListHistory(ctx, "p1", 50, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	counts := map[string]int{}
	for _, entry := range history {
		counts[entry.MediaItemID]++
	}
	if counts["ep-1"] != 1 || counts[secondEpisodeID] != 1 {
		t.Fatalf("history counts = %v, want exactly one row per target", counts)
	}

	// A retry reaches the store directly: it must not produce a second play
	// or return another entry for outbound provider synchronization.
	replay, err := userstore.MarkWatchedBatch(ctx, store, "p1", targets, entries)
	if err != nil || len(replay) != 0 {
		t.Fatalf("completed retry: %+v %v", replay, err)
	}
	history, err = store.ListHistory(ctx, "p1", 50, 0)
	if err != nil || len(history) != 2 {
		t.Fatalf("history after retry: %+v %v", history, err)
	}

	// Start incomplete so the zero-duration mark must update the row.
	if err := store.SetProgressAt(ctx, "p1", "known-duration", 120, 1200, false, watchedAt); err != nil {
		t.Fatal(err)
	}
	marked, err := userstore.MarkWatchedBatch(ctx, store, "p1",
		[]userstore.MarkWatchedTarget{{MediaItemID: "known-duration"}},
		[]userstore.WatchHistoryEntry{{
			ProfileID: "p1", MediaItemID: "known-duration",
			WatchedAt: watchedAt.Add(time.Hour).Format(time.RFC3339),
			Completed: true, Source: userstore.WatchHistorySourceJellycompat,
		}},
	)
	if err != nil || len(marked) != 1 {
		t.Fatalf("zero-duration mark: %+v %v", marked, err)
	}
	progress, err := store.GetProgress(ctx, "p1", "known-duration")
	if err != nil || progress == nil || !progress.Completed || progress.PositionSeconds != 0 || progress.DurationSeconds != 1200 {
		t.Fatalf("zero-duration mark must complete and preserve duration: %+v %v", progress, err)
	}

	// Marking watched after a history removal must land after the hidden
	// watermark, otherwise the new mark is invisible.
	removedAt := time.Date(2026, 5, 3, 9, 0, 0, 0, time.UTC)
	if err := store.RemoveHistoryItems(ctx, "p1", []string{secondEpisodeID}, removedAt); err != nil {
		t.Fatalf("RemoveHistoryItems(ep-2): %v", err)
	}
	remarked, err := userstore.MarkWatchedBatch(ctx, store, "p1",
		[]userstore.MarkWatchedTarget{{MediaItemID: secondEpisodeID, DurationSeconds: 1500}},
		[]userstore.WatchHistoryEntry{{
			ProfileID:       "p1",
			MediaItemID:     secondEpisodeID,
			WatchedAt:       watchedAt.Format(time.RFC3339), // older than the removal
			DurationSeconds: 1500,
			Completed:       true,
			Source:          userstore.WatchHistorySourceManual,
		}},
	)
	if err != nil {
		t.Fatalf("MarkWatchedBatch(after removal): %v", err)
	}
	if len(remarked) != 1 {
		t.Fatalf("re-mark returned %d entries, want 1", len(remarked))
	}
	if remarked[0].WatchedAt <= removedAt.Format(time.RFC3339) {
		t.Fatalf("re-marked WatchedAt = %q, want later than the %q removal watermark",
			remarked[0].WatchedAt, removedAt.Format(time.RFC3339))
	}
	completed, err := store.ListCompletedHistoryItems(ctx, userstore.CompletedHistoryItemQuery{
		ProfileID:    "p1",
		MediaItemIDs: []string{secondEpisodeID},
	})
	if err != nil {
		t.Fatalf("ListCompletedHistoryItems(ep-2): %v", err)
	}
	if len(completed) != 1 {
		t.Fatalf("ep-2 completed history = %v, want the re-mark to be visible", completed)
	}

	// Blank IDs are compacted out rather than reaching SQL.
	if _, err := userstore.MarkWatchedBatch(ctx, store, "p1",
		[]userstore.MarkWatchedTarget{{MediaItemID: "  "}, {MediaItemID: ""}},
		nil,
	); err != nil {
		t.Fatalf("MarkWatchedBatch(blank IDs): %v", err)
	}
	if blank, err := store.GetProgress(ctx, "p1", ""); err != nil {
		t.Fatalf("GetProgress(blank): %v", err)
	} else if blank != nil {
		t.Fatalf("GetProgress(blank) = %+v, want nil", blank)
	}
}

// testBatchWritesAdvanceEventAt extends the LWW guarantee to the batch write
// paths (jellycompat series mark-played and mark-unplayed): they advance
// updated_at without hand-setting event_at, so the stamping trigger must
// advance the LWW key for them — a queued offline event older than the batch
// write must lose. It also pins the inverse: a write that DOES set event_at
// (offline sync's clamped client event time) keeps that value rather than
// having the trigger clobber it with the server write time.
func testBatchWritesAdvanceEventAt(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour)

	// MarkProgressBatch (series mark-played) must advance event_at.
	if _, err := store.SetProgressIfNewer(ctx, "p1", "m1", 100, 1000, false, base); err != nil {
		t.Fatalf("seed m1: %v", err)
	}
	if err := store.MarkProgressBatch(ctx, "p1", []string{"m1"}, time.Time{}); err != nil {
		t.Fatalf("MarkProgressBatch: %v", err)
	}
	wrote, err := store.SetProgressIfNewer(ctx, "p1", "m1", 999, 1000, false, base.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("stale replay after mark-played: %v", err)
	}
	if wrote {
		t.Fatal("offline event older than a batch mark-played won LWW; MarkProgressBatch left event_at stale")
	}
	got, err := store.GetProgress(ctx, "p1", "m1")
	if err != nil || got == nil {
		t.Fatalf("GetProgress(m1): %+v (%v)", got, err)
	}
	if got.PositionSeconds != 0 || !got.Completed {
		t.Fatalf("m1 = pos %v completed %v; stale replay must not disturb mark-played state", got.PositionSeconds, got.Completed)
	}

	// ClearProgressBatch (mark-unplayed) must advance event_at the same way.
	if _, err := store.SetProgressIfNewer(ctx, "p1", "m2", 300, 1000, false, base); err != nil {
		t.Fatalf("seed m2: %v", err)
	}
	if err := store.ClearProgressBatch(ctx, "p1", []string{"m2"}, time.Time{}); err != nil {
		t.Fatalf("ClearProgressBatch: %v", err)
	}
	wrote, err = store.SetProgressIfNewer(ctx, "p1", "m2", 888, 1000, false, base.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("stale replay after clear: %v", err)
	}
	if wrote {
		t.Fatal("offline event older than a batch clear won LWW; ClearProgressBatch left event_at stale")
	}
	got, err = store.GetProgress(ctx, "p1", "m2")
	if err != nil || got == nil {
		t.Fatalf("GetProgress(m2): %+v (%v)", got, err)
	}
	if got.PositionSeconds != 0 {
		t.Fatalf("m2 position = %v; stale replay resurrected a cleared resume point", got.PositionSeconds)
	}

	// An explicitly-set client event time survives the trigger: a strictly
	// newer offline event (still far in the past) must be accepted, which can
	// only happen if the stored event_at is the client time, not server now.
	evt := base.Add(10 * time.Minute)
	if _, err := store.SetProgressIfNewer(ctx, "p1", "m3", 100, 1000, false, evt); err != nil {
		t.Fatalf("seed m3: %v", err)
	}
	if _, err := store.SetProgressIfNewer(ctx, "p1", "m3", 200, 1000, false, evt.Add(time.Minute)); err != nil {
		t.Fatalf("advance m3: %v", err)
	}
	wrote, err = store.SetProgressIfNewer(ctx, "p1", "m3", 300, 1000, false, evt.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("third m3 write: %v", err)
	}
	if !wrote {
		t.Fatal("a strictly newer offline event was rejected; the trigger clobbered an explicitly-set event_at")
	}
}

// RunPersonalListPage checks ListFavoritesPage and ListWatchlistPage: the
// (added_at DESC, media_item_id DESC) order, the tie on added_at broken by
// the id, resumption strictly after a key, and that a synced watchlist
// sort_index does not reorder the keyset page.
// The ids of the personal-list conformance rows.
const (
	listA = "l-a"
	listB = "l-b"
	listC = "l-c"
	listD = "l-d"
	listE = "l-e"
)

func RunPersonalListPage(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	t.Run("Favorites", func(t *testing.T) {
		store := newStore(t)
		testPersonalListPage(t, store, store.AddFavoriteAt, func(ctx context.Context, profileID string, after *userstore.ListKey, limit int) ([]userstore.ListKey, error) {
			rows, err := store.ListFavoritesPage(ctx, profileID, after, limit)
			keys := make([]userstore.ListKey, 0, len(rows))
			for _, r := range rows {
				keys = append(keys, userstore.ListKey{AddedAt: r.AddedAt, MediaItemID: r.MediaItemID})
			}
			return keys, err
		})
	})
	t.Run("Watchlist", func(t *testing.T) {
		store := newStore(t)
		testPersonalListPage(t, store, store.AddToWatchlistAt, func(ctx context.Context, profileID string, after *userstore.ListKey, limit int) ([]userstore.ListKey, error) {
			rows, err := store.ListWatchlistPage(ctx, profileID, after, limit)
			keys := make([]userstore.ListKey, 0, len(rows))
			for _, r := range rows {
				keys = append(keys, userstore.ListKey{AddedAt: r.AddedAt, MediaItemID: r.MediaItemID})
			}
			return keys, err
		})
		// A synced order changes ListWatchlist but not the keyset page.
		ctx := context.Background()
		if err := store.ReplaceWatchlistOrder(ctx, "p1", []string{listA, listE}); err != nil {
			t.Fatalf("ReplaceWatchlistOrder: %v", err)
		}
		rows, err := store.ListWatchlistPage(ctx, "p1", nil, 10)
		if err != nil {
			t.Fatalf("ListWatchlistPage: %v", err)
		}
		got := make([]string, 0, len(rows))
		for _, r := range rows {
			got = append(got, r.MediaItemID)
		}
		if strings.Join(got, ",") != "m-new,"+listD+","+listC+","+listB+","+listA+","+listE+",m-old" {
			t.Fatalf("synced order leaked into the keyset page: %v", got)
		}
	})
}

func testPersonalListPage(t *testing.T, store userstore.UserStore,
	add func(ctx context.Context, profileID, mediaItemID string, addedAt time.Time) (bool, error),
	page func(ctx context.Context, profileID string, after *userstore.ListKey, limit int) ([]userstore.ListKey, error),
) {
	ctx := context.Background()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p2", Name: "Other"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	// m-b and m-c share added_at: the id breaks the tie, greater first.
	writes := []struct {
		profile, id string
		at          time.Time
	}{
		{"p1", listA, base.Add(time.Minute)},
		{"p1", listB, base.Add(2 * time.Minute)},
		{"p1", listC, base.Add(2 * time.Minute)},
		{"p1", listD, base.Add(3 * time.Minute)},
		{"p1", listE, base},
		{"p2", "m-z", base.Add(4 * time.Minute)},
	}
	for _, w := range writes {
		if inserted, err := add(ctx, w.profile, w.id, w.at); err != nil || !inserted {
			t.Fatalf("add %s: inserted=%v err=%v", w.id, inserted, err)
		}
	}
	first := writes[0]
	if inserted, err := add(ctx, first.profile, first.id, first.at); err != nil || inserted {
		t.Fatalf("duplicate add: inserted=%v err=%v", inserted, err)
	}
	want := []string{listD, listC, listB, listA, listE}

	var got []string
	var after *userstore.ListKey
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging did not terminate")
		}
		keys, err := page(ctx, "p1", after, 2)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		for _, k := range keys {
			got = append(got, k.MediaItemID)
		}
		if len(keys) < 2 {
			break
		}
		last := keys[len(keys)-1]
		after = &last
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("keyset walk = %v, want %v", got, want)
	}
	// A key resumes strictly after itself, including in the middle of a tie.
	all, err := page(ctx, "p1", nil, 10)
	if err != nil {
		t.Fatalf("page(all): %v", err)
	}
	rest, err := page(ctx, "p1", &all[1], 10)
	if err != nil {
		t.Fatalf("page(after m-c): %v", err)
	}
	if len(rest) != 3 || rest[0].MediaItemID != listB {
		t.Fatalf("rest = %+v", rest)
	}
	// An entry added after the key was minted, newer than the key, does not
	// appear in the resumed page; one older than the key does.
	if _, err := add(ctx, "p1", "m-new", base.Add(5*time.Minute)); err != nil {
		t.Fatalf("add m-new: %v", err)
	}
	if _, err := add(ctx, "p1", "m-old", base.Add(-time.Minute)); err != nil {
		t.Fatalf("add m-old: %v", err)
	}
	rest, err = page(ctx, "p1", &all[1], 10)
	if err != nil {
		t.Fatalf("page(after m-c, changed): %v", err)
	}
	if len(rest) != 4 || rest[0].MediaItemID != listB || rest[3].MediaItemID != "m-old" {
		t.Fatalf("rest after change = %+v", rest)
	}
}

func RunProgressPage(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	t.Run("KeysetPage", func(t *testing.T) {
		testProgressPage(t, newStore)
	})
	t.Run("CompletedSince", func(t *testing.T) {
		testCompletedProgressSince(t, newStore)
	})
}

func testCompletedProgressSince(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	since := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	writes := []struct {
		id        string
		position  float64
		completed bool
		at        time.Time
	}{
		{"old", 0, true, since.Add(-time.Minute)},
		{"at-cutoff", 0, true, since},
		// Whole-second precision puts this row at the cutoff, not after it.
		{"same-second", 0, true, since.Add(500 * time.Millisecond)},
		{"next-second", 0, true, since.Add(time.Second)},
		{"newer", 0, true, since.Add(2 * time.Minute)},
		{"newest", 0, true, since.Add(3 * time.Minute)},
		{"in-progress", 10, false, since.Add(4 * time.Minute)},
	}
	for _, w := range writes {
		if err := store.SetProgressAt(ctx, "p1", w.id, w.position, 1000, w.completed, w.at); err != nil {
			t.Fatalf("write %s: %v", w.id, err)
		}
	}

	ids := func(rows []userstore.WatchProgress) []string {
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.MediaItemID)
		}
		return out
	}
	rows, err := store.ListCompletedProgressSince(ctx, "p1", since, time.Time{}, 10)
	if err != nil {
		t.Fatalf("ListCompletedProgressSince: %v", err)
	}
	if got, want := ids(rows), []string{writes[5].id, writes[4].id, writes[3].id}; !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	rows, err = store.ListCompletedProgressSince(ctx, "p1", since, time.Time{}, 2)
	if err != nil {
		t.Fatalf("ListCompletedProgressSince(limit 2): %v", err)
	}
	if got, want := ids(rows), []string{writes[5].id, writes[4].id}; !slices.Equal(got, want) {
		t.Fatalf("limited rows = %v, want %v", got, want)
	}
	// An upper bound keeps rows at its whole second and drops newer ones.
	rows, err = store.ListCompletedProgressSince(ctx, "p1", since, writes[4].at.Add(500*time.Millisecond), 10)
	if err != nil {
		t.Fatalf("ListCompletedProgressSince(until): %v", err)
	}
	if got, want := ids(rows), []string{writes[4].id, writes[3].id}; !slices.Equal(got, want) {
		t.Fatalf("bounded rows = %v, want %v", got, want)
	}
}

func testProgressPage(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	// m-b and m-d share a timestamp; m-done is completed with position 0 and
	// must not appear in the in_progress listing.
	writes := []struct {
		id        string
		position  float64
		completed bool
		at        time.Time
	}{
		{"m-a", 10, false, base.Add(3 * time.Minute)},
		{"m-b", 10, false, base.Add(2 * time.Minute)},
		{"m-d", 10, false, base.Add(2 * time.Minute)},
		{"m-c", 10, false, base.Add(time.Minute)},
		{"m-done", 0, true, base.Add(4 * time.Minute)},
		{"m-e", 10, false, base},
	}
	for _, w := range writes {
		if err := store.SetProgressAt(ctx, "p1", w.id, w.position, 1000, w.completed, w.at); err != nil {
			t.Fatalf("write %s: %v", w.id, err)
		}
	}

	var got []string
	var after *userstore.ProgressKey
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging did not terminate")
		}
		rows, err := store.ListProgressPage(ctx, "p1", "in_progress", after, 2)
		if err != nil {
			t.Fatalf("ListProgressPage: %v", err)
		}
		for _, r := range rows {
			got = append(got, r.MediaItemID)
		}
		if len(rows) < 2 {
			break
		}
		last := rows[len(rows)-1]
		after = &userstore.ProgressKey{UpdatedAt: last.UpdatedAt, MediaItemID: last.MediaItemID}
	}
	want := []string{"m-a", "m-d", "m-b", "m-c", "m-e"}
	if len(got) != len(want) {
		t.Fatalf("paged ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paged ids = %v, want %v", got, want)
		}
	}

	// The full listing includes the completed row, newest first.
	all, err := store.ListProgressPage(ctx, "p1", "", nil, 10)
	if err != nil {
		t.Fatalf("ListProgressPage(all): %v", err)
	}
	if len(all) != 6 || all[0].MediaItemID != "m-done" {
		t.Fatalf("all = %d rows, first %q", len(all), all[0].MediaItemID)
	}
	// A key resumes strictly after itself even when it is the newest row.
	rest, err := store.ListProgressPage(ctx, "p1", "", &userstore.ProgressKey{UpdatedAt: all[0].UpdatedAt, MediaItemID: all[0].MediaItemID}, 10)
	if err != nil {
		t.Fatalf("ListProgressPage(after newest): %v", err)
	}
	if len(rest) != 5 || rest[0].MediaItemID != want[0] {
		t.Fatalf("rest = %d rows, first %q", len(rest), rest[0].MediaItemID)
	}
}

// RunCollectionSharing runs the personal collection visibility and owner-only
// update conformance checks: a shared collection reaches every profile on the
// login, a private one only its creator (#1615).
func RunCollectionSharing(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	t.Run("CollectionSharing", func(t *testing.T) {
		testCollectionSharing(t, newStore)
	})
}

func sorted(ids []string) []string {
	return slices.Sorted(slices.Values(ids))
}

func testCollectionSharing(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()
	store := newStore(t)

	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Owner"}); err != nil {
		t.Fatalf("CreateProfile(p1): %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p2", Name: "Viewer"}); err != nil {
		t.Fatalf("CreateProfile(p2): %v", err)
	}

	shared, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: "p1",
		Name:             "Family Action",
		CollectionType:   "smart",
		IsShared:         true,
		QueryDefinition:  `{"match":"all","groups":[]}`,
	})
	if err != nil {
		t.Fatalf("CreateCollection(shared): %v", err)
	}
	private, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: "p1",
		Name:             "Just Mine",
	})
	if err != nil {
		t.Fatalf("CreateCollection(private): %v", err)
	}
	theirs, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: "p2",
		Name:             "Viewer's Own",
	})
	if err != nil {
		t.Fatalf("CreateCollection(theirs): %v", err)
	}

	// A profile created after the collection was shared still sees it.
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p3", Name: "Added Later"}); err != nil {
		t.Fatalf("CreateProfile(p3): %v", err)
	}

	listed := func(profileID string) []string {
		t.Helper()
		collections, err := store.ListCollections(ctx, profileID)
		if err != nil {
			t.Fatalf("ListCollections(%s): %v", profileID, err)
		}
		ids := make([]string, 0, len(collections))
		for _, c := range collections {
			if !c.VisibleTo(profileID) {
				t.Fatalf("ListCollections(%s) returned %s, which VisibleTo rejects", profileID, c.ID)
			}
			ids = append(ids, c.ID)
		}
		return ids
	}
	// A shared collection reaches every profile on the login, including one
	// that no allow list ever named; a private one stays with its creator.
	// Each profile's own collections come first.
	if got, want := listed("p2"), []string{theirs.ID, shared.ID}; !slices.Equal(got, want) {
		t.Fatalf("ListCollections(p2) = %v, want %v", got, want)
	}
	if got, want := listed("p3"), []string{shared.ID}; !slices.Equal(got, want) {
		t.Fatalf("ListCollections(p3) = %v, want %v", got, want)
	}
	if got, want := listed("p1"), []string{shared.ID, private.ID}; !slices.Equal(sorted(got), sorted(want)) {
		t.Fatalf("ListCollections(p1) = %v, want %v", got, want)
	}
	if got, err := store.GetCollection(ctx, private.ID); err != nil || got.VisibleTo("p2") || !got.VisibleTo("p1") {
		t.Fatalf("private collection visibility = %+v, %v; want only its creator", got, err)
	}

	rejectedName := "Not Allowed"
	if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{
		ID:               shared.ID,
		RequestProfileID: "p2",
		Name:             &rejectedName,
	}); err == nil {
		t.Fatal("expected creator-only UpdateCollection rejection")
	}

	// Turning sharing off hides the collection from everyone but its creator.
	notShared := false
	if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{
		ID:               shared.ID,
		RequestProfileID: "p1",
		IsShared:         &notShared,
	}); err != nil {
		t.Fatalf("UpdateCollection(is_shared=false): %v", err)
	}
	if got := listed("p3"); len(got) != 0 {
		t.Fatalf("ListCollections(p3) after unsharing = %v, want none", got)
	}
}
