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
