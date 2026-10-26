package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunHistoryEntryOnce checks that a visible-history insert under an ID the
// store already holds adds no row: a completed entry completes a row stored
// incomplete, and any other repeat reports ErrHistoryEntryExists. This is how
// a play finalized by several stops is recorded once.
func RunHistoryEntryOnce(t *testing.T, store userstore.UserStore) {
	t.Helper()
	ctx := t.Context()
	const profile = "once-profile"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: "Once"}); err != nil {
		t.Fatal(err)
	}
	entry := userstore.WatchHistoryEntry{
		ID:              "7f3a1c52-9b0e-5d84-a6f2-3e1b9c7d0a45",
		ProfileID:       profile,
		MediaItemID:     "once-item",
		WatchedAt:       time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Format(time.RFC3339),
		DurationSeconds: 3600,
		Completed:       false,
		Source:          userstore.WatchHistorySourcePlayback,
	}
	if _, err := userstore.AddVisibleHistory(ctx, store, entry); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	entry.WatchedAt = time.Date(2026, 1, 2, 3, 9, 5, 0, time.UTC).Format(time.RFC3339)
	if _, err := userstore.AddVisibleHistory(ctx, store, entry); !errors.Is(err, userstore.ErrHistoryEntryExists) {
		t.Fatalf("repeated incomplete insert: err = %v, want ErrHistoryEntryExists", err)
	}
	entry.Completed = true
	if _, err := userstore.AddVisibleHistory(ctx, store, entry); err != nil {
		t.Fatalf("completing insert: %v", err)
	}
	if _, err := userstore.AddVisibleHistory(ctx, store, entry); !errors.Is(err, userstore.ErrHistoryEntryExists) {
		t.Fatalf("repeated completed insert: err = %v, want ErrHistoryEntryExists", err)
	}
	history, err := store.ListHistory(ctx, profile, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || !history[0].Completed {
		t.Fatalf("history = %+v, want one completed row", history)
	}
}
