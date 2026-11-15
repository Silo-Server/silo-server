package handlers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Audiobooks keep media_items.status empty. One whose enrichment found no
// provider match, or whose providers kept failing, lists as unmatched, one still waiting for enrichment does
// not, and a match takes it off the list: one that stores a provider ID, an
// automatic match that only records a successful outcome for its tagged ASIN,
// or an admin's Match Item.
func TestUnmatchedItemsListsAudiobooksWithoutProviderMatchDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	tag := fmt.Sprintf("abunmatched-%d", time.Now().UnixNano())
	noMatchID, waitingID, failedID := tag+"-nomatch", tag+"-waiting", tag+"-failed"
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('audiobooks', $1, true) RETURNING id`, tag).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id IN ($1, $2, $3)`, noMatchID, waitingID, failedID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	for _, id := range []string{noMatchID, waitingID, failedID} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'audiobook', $1, '', '{}'::text[])`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, folderID); err != nil {
			t.Fatal(err)
		}
	}
	// The scanner's ASIN from tags is not a provider match.
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type) VALUES ($1, 'asin', 'B000TEST', 'audiobook')`, noMatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO audiobook_enrichment_state (content_id, attempts, outcome) VALUES ($1, 1, 'no_match')`, noMatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO audiobook_enrichment_state (content_id, attempts, outcome) VALUES ($1, 5, 'failed')`, failedID); err != nil {
		t.Fatal(err)
	}

	h := NewLibraryHandler(nil, nil, nil, pool, nil)
	list := func() map[string]string {
		t.Helper()
		views, _, err := h.ListUnmatchedItems(ctx, tag, false, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, v := range views {
			got[v.ContentID] = v.Status
		}
		return got
	}
	got := list()
	if len(got) != 2 || got[noMatchID] != "unmatched" || got[failedID] != "unmatched" {
		t.Fatalf("listed = %v, want %s and %s as unmatched", got, noMatchID, failedID)
	}
	// The rest of the test follows the no-match book alone.
	if _, err := pool.Exec(ctx, `UPDATE audiobook_enrichment_state SET outcome = 'success' WHERE content_id = $1`, failedID); err != nil {
		t.Fatal(err)
	}

	// A match through the tagged ASIN stores no other provider ID.
	if _, err := pool.Exec(ctx, `UPDATE audiobook_enrichment_state SET outcome = 'success' WHERE content_id = $1`, noMatchID); err != nil {
		t.Fatal(err)
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("listed after an asin-only match = %v, want none", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE audiobook_enrichment_state SET outcome = 'no_match' WHERE content_id = $1`, noMatchID); err != nil {
		t.Fatal(err)
	}
	// Match Item with only the ASIN writes status 'matched' and no state.
	if _, err := pool.Exec(ctx, `UPDATE media_items SET status = 'matched' WHERE content_id = $1`, noMatchID); err != nil {
		t.Fatal(err)
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("listed after an admin match = %v, want none", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_items SET status = '' WHERE content_id = $1`, noMatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type) VALUES ($1, 'audible', $1, 'audiobook')`, noMatchID); err != nil {
		t.Fatal(err)
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("listed after a provider-id match = %v, want none", got)
	}
}
