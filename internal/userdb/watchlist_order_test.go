package userdb

import (
	"database/sql"
	"testing"
	"time"
)

func watchlistIDs(t *testing.T, db *sql.DB, profileID string) []string {
	t.Helper()
	entries, err := ListWatchlist(db, profileID, 100, 0)
	if err != nil {
		t.Fatalf("ListWatchlist: %v", err)
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.MediaItemID)
	}
	return ids
}

func TestWatchlistOrderMirrorsProviderSequence(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	const profile = "profile-1"
	// Add in one order (added_at ascending a, b, c).
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"a", "b", "c"} {
		if _, err := AddToWatchlistAt(db, profile, id, base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("AddToWatchlistAt %s: %v", id, err)
		}
	}

	// Without a synced order, newest-added comes first.
	if got := watchlistIDs(t, db, profile); !equalStrings(got, []string{"c", "b", "a"}) {
		t.Fatalf("default order = %v, want [c b a]", got)
	}

	// Mirror a provider order of c, a, b.
	if err := ReplaceWatchlistOrder(db, profile, []string{"c", "a", "b"}); err != nil {
		t.Fatalf("ReplaceWatchlistOrder: %v", err)
	}
	if got := watchlistIDs(t, db, profile); !equalStrings(got, []string{"c", "a", "b"}) {
		t.Fatalf("synced order = %v, want [c a b]", got)
	}

	// A locally-added item (no synced index) sorts after the ordered ones.
	if _, err := AddToWatchlistAt(db, profile, "d", base.Add(10*time.Hour)); err != nil {
		t.Fatalf("AddToWatchlistAt d: %v", err)
	}
	if got := watchlistIDs(t, db, profile); !equalStrings(got, []string{"c", "a", "b", "d"}) {
		t.Fatalf("order with local add = %v, want [c a b d]", got)
	}

	// Re-syncing a shorter provider list drops removed items' index and keeps
	// the new order; items no longer present fall back to added_at.
	if err := ReplaceWatchlistOrder(db, profile, []string{"b", "a"}); err != nil {
		t.Fatalf("ReplaceWatchlistOrder re-sync: %v", err)
	}
	// b, a are positioned; c and d are unpositioned → after, newest-first (d then c).
	if got := watchlistIDs(t, db, profile); !equalStrings(got, []string{"b", "a", "d", "c"}) {
		t.Fatalf("re-synced order = %v, want [b a d c]", got)
	}

	// Clearing the order reverts everything to added_at DESC.
	if err := ReplaceWatchlistOrder(db, profile, nil); err != nil {
		t.Fatalf("ReplaceWatchlistOrder clear: %v", err)
	}
	if got := watchlistIDs(t, db, profile); !equalStrings(got, []string{"d", "c", "b", "a"}) {
		t.Fatalf("cleared order = %v, want [d c b a]", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
