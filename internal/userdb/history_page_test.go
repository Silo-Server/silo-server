package userdb

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ListHistoryPage orders by (watched_at DESC, id DESC) and resumes strictly
// after the key, so a tie on watched_at is broken by id and a row hidden or
// inserted between pages neither repeats nor skips a neighbor.
func TestListHistoryPageKeysetOrdering(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	store := NewSQLiteUserStore(db)
	ctx := context.Background()

	add := func(id, item, watchedAt string) {
		t.Helper()
		if err := AddHistory(db, WatchHistoryEntry{ID: id, ProfileID: "p1", MediaItemID: item, WatchedAt: watchedAt, Completed: true, Source: userstore.WatchHistorySourcePlayback}); err != nil {
			t.Fatalf("AddHistory(%s): %v", id, err)
		}
	}
	// Inserted out of order so the query, not insertion, decides the order.
	add("b", "movie-2", "2026-01-04T00:00:00Z")
	add("d", "movie-4", "2026-01-02T00:00:00Z")
	add("c", "movie-3", "2026-01-04T00:00:00Z")
	add("a", "movie-1", "2026-01-05T00:00:00Z")
	add("e", "movie-5", "2026-01-01T00:00:00Z")

	ids := func(rows []userstore.WatchHistoryEntry) []string {
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	want := func(got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("ids = %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("ids = %v, want %v", got, want)
			}
		}
	}

	page1, err := store.ListHistoryPage(ctx, "p1", nil, 2)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	// The 2026-01-04 tie breaks by id DESC: c before b.
	want(ids(page1), []string{"a", "c"})

	// A watch recorded after page 1 sorts above the key and is not repeated
	// into page 2.
	add("f", "movie-6", "2026-01-06T00:00:00Z")
	key := &userstore.HistoryKey{WatchedAt: page1[1].WatchedAt, ID: page1[1].ID}
	page2, err := store.ListHistoryPage(ctx, "p1", key, 2)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	want(ids(page2), []string{"b", "d"})

	// Hiding an emitted row after page 2 does not shift the next page.
	if err := store.RemoveHistoryItems(ctx, "p1", []string{"movie-2"}, time.Now().UTC()); err != nil {
		t.Fatalf("RemoveHistoryItems: %v", err)
	}
	key = &userstore.HistoryKey{WatchedAt: page2[1].WatchedAt, ID: page2[1].ID}
	page3, err := store.ListHistoryPage(ctx, "p1", key, 2)
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	want(ids(page3), []string{"e"})

	// The offset form v1 uses is untouched: it still lists the visible rows
	// newest first.
	all, err := store.ListHistory(ctx, "p1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(all) != 5 || all[0].ID != "f" || all[len(all)-1].ID != "e" {
		t.Fatalf("offset list = %v", ids(all))
	}
}
