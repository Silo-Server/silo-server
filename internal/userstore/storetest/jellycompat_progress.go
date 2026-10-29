package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunAtomicJellycompatProgress requires the fixture to reject position 321 at
// the database boundary, after the history insert/removal has already executed.
// A played edit adds history only when the leaf is not already completed.
func RunAtomicJellycompatProgress(t *testing.T, store userstore.UserStore) {
	t.Helper()
	ctx := t.Context()
	const profile = "atomic-profile"
	const item = "atomic-item"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: "Atomic"}); err != nil {
		t.Fatal(err)
	}
	writer, ok := store.(userstore.JellycompatProgressEditor)
	if !ok {
		t.Fatal("atomic editor unavailable")
	}
	reader, ok := store.(interface {
		ListJellycompatProgressDates(context.Context, string, []string) (map[string]string, error)
	})
	if !ok {
		t.Fatal("progress dates unavailable")
	}
	date := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	edit := userstore.JellycompatProgressEdit{MediaItemID: item, PositionSeconds: 123, DurationSeconds: 600, Completed: true, EventAt: date,
		History: &userstore.WatchHistoryEntry{WatchedAt: date.Format(time.RFC3339), Completed: true, Source: userstore.WatchHistorySourceJellycompat}}
	if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err != nil {
		t.Fatal(err)
	}
	check := func(completed bool, position float64, historyCount int, eventAt time.Time) {
		t.Helper()
		progress, err := store.GetProgress(ctx, profile, item)
		if err != nil || progress == nil || progress.Completed != completed || progress.PositionSeconds != position || progress.DurationSeconds != 600 {
			t.Fatalf("progress %+v: %v", progress, err)
		}
		history, err := store.ListHistory(ctx, profile, 10, 0)
		if err != nil || len(history) != historyCount {
			t.Fatalf("history %+v: %v", history, err)
		}
		dates, err := reader.ListJellycompatProgressDates(ctx, profile, []string{item})
		if err != nil || dates[item] != eventAt.Format(time.RFC3339) {
			t.Fatalf("dates %v: %v", dates, err)
		}
	}
	check(true, 123, 1, date)
	failed := edit
	failed.PositionSeconds = 321
	failed.ClearWatched = true
	failed.Completed = false
	failed.History = nil
	if err := writer.ApplyJellycompatProgress(ctx, profile, failed); err == nil {
		t.Fatal("database failure was ignored")
	}
	check(true, 123, 1, date)
	// Repeating the played edit on a completed leaf adds no history.
	if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err != nil {
		t.Fatal(err)
	}
	check(true, 123, 1, date)
	// A date-only edit must not create history or change the position/played state.
	edit.History = nil
	edit.EventAt = date.Add(-time.Hour)
	if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err != nil {
		t.Fatal(err)
	}
	check(true, 123, 1, edit.EventAt)
	// Explicit unplayed keeps history and retains the requested resume position.
	unplayed := edit
	unplayed.ClearWatched = true
	unplayed.Completed = false
	unplayed.PositionSeconds = 77
	if err := writer.ApplyJellycompatProgress(ctx, profile, unplayed); err != nil {
		t.Fatal(err)
	}
	check(false, 77, 1, edit.EventAt)
	// A played edit that fails after its history insert rolls the insert back.
	failed = edit
	failed.PositionSeconds = 321
	failed.History = &userstore.WatchHistoryEntry{WatchedAt: date.Format(time.RFC3339), Completed: true}
	if err := writer.ApplyJellycompatProgress(ctx, profile, failed); err == nil {
		t.Fatal("database failure was ignored")
	}
	check(false, 77, 1, edit.EventAt)
	// After an unmark, the next played edit records a new play.
	edit.PositionSeconds = 88
	edit.History = &userstore.WatchHistoryEntry{ID: "00000000-0000-0000-0000-000000000002", WatchedAt: date.Format(time.RFC3339), Completed: true}
	if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err != nil {
		t.Fatal(err)
	}
	check(true, 88, 2, edit.EventAt)
	// History failure leaves the prior progress/date intact too.
	if err := writer.ApplyJellycompatProgress(ctx, profile, unplayed); err != nil {
		t.Fatal(err)
	}
	edit.PositionSeconds = 99
	if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err == nil {
		t.Fatal("duplicate history key accepted")
	}
	check(false, 77, 2, edit.EventAt)
}
