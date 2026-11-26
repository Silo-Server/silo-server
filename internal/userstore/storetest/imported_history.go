package storetest

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ImportedHistoryConcurrent exercises replay and visibility on a real store.
func ImportedHistoryConcurrent(t *testing.T, store userstore.UserStore) {
	t.Helper()
	ctx := t.Context()
	for _, id := range []string{"import-p", "import-other"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entry := userstore.WatchHistoryEntry{ProfileID: "import-p", MediaItemID: "import-movie", WatchedAt: stamp.Format(time.RFC3339), Completed: true, Source: userstore.WatchHistorySourceImport}
	start := make(chan struct{})
	results := make(chan error, 24)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			<-start
			created, err := store.AddHistoryIfMissing(ctx, entry)
			if created {
				winners.Add(1)
			}
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners.Load() != 1 {
		t.Fatalf("created count=%d, want 1", winners.Load())
	}
	rows, err := store.ListHistory(ctx, entry.ProfileID, 100, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("history rows=%d err=%v", len(rows), err)
	}
	other := entry
	other.ProfileID = "import-other"
	if created, err := store.AddHistoryIfMissing(ctx, other); err != nil || !created {
		t.Fatalf("other profile=%v %v", created, err)
	}
	later := entry
	later.WatchedAt = stamp.Add(time.Second).Format(time.RFC3339)
	if created, err := store.AddHistoryIfMissing(ctx, later); err != nil || !created {
		t.Fatalf("different timestamp=%v %v", created, err)
	}
	// Normal playback history may contain multiple events at the same instant.
	if err := store.AddHistory(ctx, entry); err != nil {
		t.Fatal(err)
	}
	rows, err = store.ListHistory(ctx, entry.ProfileID, 100, 0)
	if err != nil || len(rows) != 3 {
		t.Fatalf("normal history rows=%d err=%v", len(rows), err)
	}
	if err := store.RemoveHistoryItems(ctx, entry.ProfileID, []string{entry.MediaItemID}, stamp.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if created, err := store.AddHistoryIfMissing(ctx, entry); err != nil || created {
		t.Fatalf("hidden replay=%v %v", created, err)
	}
	rows, err = store.ListHistory(ctx, entry.ProfileID, 100, 0)
	if err != nil || len(rows) != 0 {
		t.Fatalf("hidden rows=%d err=%v", len(rows), err)
	}

	// The remove/import race must leave no visible replay, whichever writer wins.
	entry.MediaItemID = "import-race"
	start = make(chan struct{})
	results = make(chan error, 2)
	wg.Go(func() { <-start; _, err := store.AddHistoryIfMissing(ctx, entry); results <- err })
	wg.Go(func() {
		<-start
		results <- store.RemoveHistoryItems(ctx, entry.ProfileID, []string{entry.MediaItemID}, stamp.Add(time.Minute))
	})
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if created, err := store.AddHistoryIfMissing(ctx, entry); err != nil || created {
		t.Fatalf("race replay=%v %v", created, err)
	}
	rows, err = store.ListHistory(ctx, entry.ProfileID, 100, 0)
	if err != nil || len(rows) != 0 {
		t.Fatalf("race visible rows=%d err=%v", len(rows), err)
	}
}

// RunImportedCompletionAfterSameSecondPartial checks that an imported
// completion is kept when a partial play of the same item landed earlier in
// the same second, while a replay of either is still recognized (#5366).
func RunImportedCompletionAfterSameSecondPartial(t *testing.T, newStore func(*testing.T) userstore.UserStore) {
	t.Helper()
	ctx := t.Context()
	const profileID = "same-second-p"
	store := newStore(t)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: profileID}); err != nil {
		t.Fatal(err)
	}
	second := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	add := func(item string, offset time.Duration, completed bool) bool {
		t.Helper()
		created, err := store.AddHistoryIfMissing(ctx, userstore.WatchHistoryEntry{
			ProfileID: profileID, MediaItemID: item, WatchedAt: second.Add(offset).Format(time.RFC3339Nano),
			DurationSeconds: 600, Completed: completed, Source: userstore.WatchHistorySourceImport,
		})
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	completedRows := func(item string) (all, completed int) {
		t.Helper()
		rows, err := store.ListHistory(ctx, profileID, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.MediaItemID != item {
				continue
			}
			all++
			if row.Completed {
				completed++
			}
		}
		return all, completed
	}

	// A partial stop, then the completion half a second later.
	if !add("stop-then-played", 100*time.Millisecond, false) {
		t.Fatal("partial stop was not recorded")
	}
	if add("stop-then-played", 100*time.Millisecond, false) {
		t.Fatal("a replayed partial stop was recorded again")
	}
	if !add("stop-then-played", 600*time.Millisecond, true) {
		t.Fatal("completion in the same second as a partial stop was not recorded")
	}
	if add("stop-then-played", 600*time.Millisecond, true) || add("stop-then-played", 100*time.Millisecond, false) {
		t.Fatal("a replayed import was recorded again")
	}
	if all, completed := completedRows("stop-then-played"); all != 2 || completed != 1 {
		t.Fatalf("history rows=%d completed=%d, want 2 and 1", all, completed)
	}

	// A partial stop after a completion in the same second adds nothing.
	if !add("played-then-stop", 100*time.Millisecond, true) {
		t.Fatal("completion was not recorded")
	}
	if add("played-then-stop", 600*time.Millisecond, false) {
		t.Fatal("partial stop in the same second as a completion was recorded")
	}
	if all, completed := completedRows("played-then-stop"); all != 1 || completed != 1 {
		t.Fatalf("history rows=%d completed=%d, want 1 and 1", all, completed)
	}
}
