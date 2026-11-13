package storetest

import (
	"context"
	"slices"
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

// RunJellycompatPlayedByDate checks the play-date listing behind the
// Jellyfin-compatible SortBy=DatePlayed: completed rows only, ordered by play
// date in either direction regardless of write order, and paged by offset.
func RunJellycompatPlayedByDate(t *testing.T, store userstore.UserStore) {
	t.Helper()
	ctx := t.Context()
	const profile = "played-by-date"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: "Played by date"}); err != nil {
		t.Fatal(err)
	}
	writer, ok := store.(userstore.JellycompatProgressEditor)
	if !ok {
		t.Fatal("jellycompat progress editor unavailable")
	}
	const played2015, played2020, played2025 = "played-2015", "played-2020", "played-2025"
	// Written newest play first; one fractional-second date and one unfinished
	// row that must not be listed.
	for _, row := range []struct {
		id        string
		date      time.Time
		completed bool
	}{
		{played2025, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{played2020, time.Date(2020, 6, 1, 12, 30, 0, 500_000_000, time.UTC), true},
		{"started-2022", time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{played2015, time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC), true},
	} {
		edit := userstore.JellycompatProgressEdit{MediaItemID: row.id, DurationSeconds: 600, Completed: row.completed, EventAt: row.date}
		if !row.completed {
			edit.PositionSeconds = 120
		}
		if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err != nil {
			t.Fatal(err)
		}
	}
	list := func(ascending bool, limit, offset int) []string {
		t.Helper()
		rows, err := userstore.ListJellycompatPlayedByDate(ctx, store, profile, nil, nil, ascending, limit, offset)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			if !row.Completed {
				t.Fatalf("listed unfinished row %+v", row)
			}
			ids = append(ids, row.MediaItemID)
		}
		return ids
	}
	for _, tc := range []struct {
		ascending     bool
		limit, offset int
		want          []string
	}{
		{true, 10, 0, []string{played2015, played2020, played2025}},
		{false, 10, 0, []string{played2025, played2020, played2015}},
		{false, 2, 1, []string{played2020, played2015}},
	} {
		if got := list(tc.ascending, tc.limit, tc.offset); !slices.Equal(got, tc.want) {
			t.Fatalf("ascending=%v limit=%d offset=%d: %v, want %v", tc.ascending, tc.limit, tc.offset, got, tc.want)
		}
	}
}
