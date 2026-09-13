package storetest

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunAtomicJellycompatFavorite rejects favorite writes after history and progress
// mutations, then retries the same request with the rejection removed.
func RunAtomicJellycompatFavorite(t *testing.T, store userstore.UserStore, reject func(bool)) {
	t.Helper()
	ctx := t.Context()
	const profile, item = "favorite-profile", "favorite-item"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: "Favorite"}); err != nil {
		t.Fatal(err)
	}
	writer, ok := store.(userstore.JellycompatProgressEditor)
	if !ok {
		t.Fatal("atomic editor unavailable")
	}
	date := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := store.SetProgressAt(ctx, profile, item, 12, 600, false, date); err != nil {
		t.Fatal(err)
	}
	check := func(favorite, played bool, position float64, count int) {
		t.Helper()
		actual, err := store.IsFavorite(ctx, profile, item)
		if err != nil || actual != favorite {
			t.Fatalf("favorite=%v error=%v", actual, err)
		}
		p, err := store.GetProgress(ctx, profile, item)
		if err != nil || p == nil || p.Completed != played || p.PositionSeconds != position || p.DurationSeconds != 600 {
			t.Fatalf("progress=%+v error=%v", p, err)
		}
		history, err := store.ListHistory(ctx, profile, 10, 0)
		if err != nil || len(history) != count {
			t.Fatalf("history=%+v error=%v", history, err)
		}
	}
	edit := userstore.JellycompatProgressEdit{MediaItemID: item, PositionSeconds: 123, DurationSeconds: 600, Completed: true, EventAt: date, IsFavorite: new(true), History: &userstore.WatchHistoryEntry{WatchedAt: date.Format(time.RFC3339), Completed: true}}
	reject(true)
	if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err == nil {
		t.Fatal("favorite insertion failure ignored")
	}
	check(false, false, 12, 0)
	reject(false)
	if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err != nil {
		t.Fatal(err)
	}
	check(true, true, 123, 1)
	edit.IsFavorite = new(false)
	edit.Completed = false
	edit.ClearHistory = true
	edit.History = nil
	edit.PositionSeconds = 77
	reject(true)
	if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err == nil {
		t.Fatal("favorite removal failure ignored")
	}
	check(true, true, 123, 1)
	reject(false)
	if err := writer.ApplyJellycompatProgress(ctx, profile, edit); err != nil {
		t.Fatal(err)
	}
	check(false, false, 77, 0)
}
