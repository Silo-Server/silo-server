package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunDatedMarkWatchedBatch checks the atomic explicit-date extension against
// both real stores, including failures after the progress statement succeeds.
func RunDatedMarkWatchedBatch(t *testing.T, store userstore.UserStore) {
	t.Helper()
	ctx := t.Context()
	const profile = "dated-profile"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: "Dated"}); err != nil {
		t.Fatal(err)
	}
	date := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	ids := []string{"dated-1", "dated-2"}
	targets := []userstore.MarkWatchedTarget{
		{MediaItemID: ids[0], DurationSeconds: 600, EventAt: new(date)},
		{MediaItemID: ids[1], DurationSeconds: 900, EventAt: new(date)},
	}
	entries := []userstore.WatchHistoryEntry{
		{ID: "00000000-0000-0000-0000-000000000001", ProfileID: profile, MediaItemID: ids[0], WatchedAt: date.Format(time.RFC3339), Completed: true, Source: userstore.WatchHistorySourceJellycompat},
		{ID: "00000000-0000-0000-0000-000000000001", ProfileID: profile, MediaItemID: ids[1], WatchedAt: date.Format(time.RFC3339), Completed: true, Source: userstore.WatchHistorySourceJellycompat},
	}
	if _, err := userstore.MarkWatchedBatch(ctx, store, profile, targets, entries); err == nil {
		t.Fatal("duplicate history key must fail")
	}
	for _, id := range ids {
		p, err := store.GetProgress(ctx, profile, id)
		if err != nil || p != nil {
			t.Fatalf("failed transaction left progress: %+v %v", p, err)
		}
	}
	history, err := store.ListHistory(ctx, profile, 10, 0)
	if err != nil || len(history) != 0 {
		t.Fatalf("failed transaction left history: %+v %v", history, err)
	}
	entries[0].ID, entries[1].ID = "", ""
	reader, ok := store.(interface {
		ListJellycompatProgressDates(context.Context, string, []string) (map[string]string, error)
	})
	if !ok {
		t.Fatal("store does not expose progress dates")
	}
	for iteration := range 2 {
		if _, err := userstore.MarkWatchedBatch(ctx, store, profile, targets, entries); err != nil {
			t.Fatal(err)
		}
		dates, err := reader.ListJellycompatProgressDates(ctx, profile, ids)
		if err != nil {
			t.Fatal(err)
		}
		for index, id := range ids {
			p, err := store.GetProgress(ctx, profile, id)
			if err != nil || p == nil || !p.Completed || p.PositionSeconds != 0 || p.DurationSeconds != []float64{600, 900}[index] {
				t.Fatalf("dated batch progress: %+v %v", p, err)
			}
			if dates[id] != date.Format(time.RFC3339) {
				t.Fatalf("date for %s=%s", id, dates[id])
			}
		}
		if iteration == 0 {
			targets[0].DurationSeconds, targets[1].DurationSeconds = 0, 0
			if _, err := userstore.MarkWatchedBatch(ctx, store, profile, targets, nil); err != nil {
				t.Fatal(err)
			}
			for index, id := range ids {
				progress, err := store.GetProgress(ctx, profile, id)
				if err != nil || progress == nil || progress.DurationSeconds != []float64{600, 900}[index] {
					t.Fatalf("zero-duration update lost known duration: %+v %v", progress, err)
				}
			}
			targets[0].DurationSeconds, targets[1].DurationSeconds = 600, 900
			if err := store.RemoveHistoryItems(ctx, profile, ids, time.Now().UTC().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Mixed targets preserve the explicit date while undated targets retain
	// ordinary manual-mark timing within the same transaction.
	targets[0].EventAt = nil
	if _, err := userstore.MarkWatchedBatch(ctx, store, profile, targets, nil); err != nil {
		t.Fatal(err)
	}
	dates, err := reader.ListJellycompatProgressDates(ctx, profile, ids)
	if err != nil {
		t.Fatal(err)
	}
	if dates[ids[0]] == date.Format(time.RFC3339) || dates[ids[1]] != date.Format(time.RFC3339) {
		t.Fatalf("mixed batch dates: %v", dates)
	}
	// An ordinary mark in a later transaction must not inherit the date flag.
	if _, err := userstore.MarkWatchedBatch(ctx, store, profile, []userstore.MarkWatchedTarget{{MediaItemID: ids[1]}}, nil); err != nil {
		t.Fatal(err)
	}
	dates, err = reader.ListJellycompatProgressDates(ctx, profile, ids[1:])
	if err != nil {
		t.Fatal(err)
	}
	if dates[ids[1]] == date.Format(time.RFC3339) {
		t.Fatal("explicit date leaked into later undated transaction")
	}
}
