package storetest

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunAtomicJellycompatParentFavorite verifies favorite failures roll back every
// child, including history visibility, before retrying the complete parent edit.
func RunAtomicJellycompatParentFavorite(t *testing.T, store userstore.UserStore, reject func(bool)) {
	t.Helper()
	ctx := t.Context()
	const profile = "parent-profile"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: "Parent"}); err != nil {
		t.Fatal(err)
	}
	writer, ok := store.(userstore.JellycompatParentEditor)
	if !ok {
		t.Fatal("parent editor unavailable")
	}
	date := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	edit := userstore.JellycompatParentEdit{MediaItemID: "series", Played: true, IsFavorite: true}
	for _, id := range []string{"episode-1", "episode-2"} {
		if err := store.SetProgressAt(ctx, profile, id, 12, 600, false, date); err != nil {
			t.Fatal(err)
		}
		edit.Targets = append(edit.Targets, userstore.MarkWatchedTarget{MediaItemID: id, DurationSeconds: 600})
		edit.History = append(edit.History, userstore.WatchHistoryEntry{ProfileID: profile, MediaItemID: id, WatchedAt: date.Format(time.RFC3339), Completed: true})
	}
	check := func(favorite, completed bool, position float64, historyCount int) {
		t.Helper()
		actual, err := store.IsFavorite(ctx, profile, "series")
		if err != nil || actual != favorite {
			t.Fatalf("favorite=%v error=%v", actual, err)
		}
		for _, target := range edit.Targets {
			p, err := store.GetProgress(ctx, profile, target.MediaItemID)
			if err == nil && p == nil && !completed && position == 0 {
				continue
			}
			if err != nil || p == nil || p.Completed != completed || p.PositionSeconds != position {
				t.Fatalf("child=%s progress=%+v error=%v", target.MediaItemID, p, err)
			}
		}
		history, err := store.ListHistory(ctx, profile, 10, 0)
		if err != nil || len(history) != historyCount {
			t.Fatalf("history count=%d error=%v", len(history), err)
		}
	}
	reject(true)
	if err := writer.ApplyJellycompatParent(ctx, profile, edit); err == nil {
		t.Fatal("favorite insert failure ignored")
	}
	check(false, false, 12, 0)
	reject(false)
	if err := writer.ApplyJellycompatParent(ctx, profile, edit); err != nil {
		t.Fatal(err)
	}
	check(true, true, 0, 2)
	edit.Played, edit.IsFavorite = false, false
	edit.History = nil
	reject(true)
	if err := writer.ApplyJellycompatParent(ctx, profile, edit); err == nil {
		t.Fatal("favorite delete failure ignored")
	}
	check(true, true, 0, 2)
	reject(false)
	if err := writer.ApplyJellycompatParent(ctx, profile, edit); err != nil {
		t.Fatal(err)
	}
	check(false, false, 0, 0)
	edit.Targets = nil
	edit.IsFavorite = true
	if err := writer.ApplyJellycompatParent(ctx, profile, edit); err != nil {
		t.Fatal(err)
	}
	check(true, false, 0, 0)
}
