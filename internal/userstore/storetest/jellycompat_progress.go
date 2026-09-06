package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunAtomicJellycompatProgress requires the fixture to reject position 321 at
// the database boundary, after the history insert/removal has already executed.
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
	for _, clear := range []bool{false, true} {
		failed := edit
		failed.PositionSeconds = 321
		failed.ClearHistory = clear
		failed.Completed = !clear
		if clear {
			failed.History = nil
		}
		if err := writer.ApplyJellycompatProgress(ctx, profile, failed); err == nil {
			t.Fatal("database failure was ignored")
		}
		check(true, 123, 1, date)
	}
	// Retry after the failed played request adds exactly one history entry.
	if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err != nil {
		t.Fatal(err)
	}
	check(true, 123, 2, date)
	// A date-only edit must not create history or change the position/played state.
	edit.History = nil
	edit.EventAt = date.Add(-time.Hour)
	if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err != nil {
		t.Fatal(err)
	}
	check(true, 123, 2, edit.EventAt)
	// Explicit unplayed clears history and retains the requested resume position.
	edit.ClearHistory = true
	edit.Completed = false
	edit.PositionSeconds = 77
	if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err != nil {
		t.Fatal(err)
	}
	check(false, 77, 0, edit.EventAt)
	// History failure leaves the prior progress/date intact too.
	edit.ClearHistory = false
	edit.Completed = true
	edit.PositionSeconds = 88
	edit.History = &userstore.WatchHistoryEntry{ID: "00000000-0000-0000-0000-000000000002", WatchedAt: date.Format(time.RFC3339), Completed: true}
	if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err != nil {
		t.Fatal(err)
	}
	edit.PositionSeconds = 99
	if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err == nil {
		t.Fatal("duplicate history key accepted")
	}
	check(true, 88, 1, edit.EventAt)
}
