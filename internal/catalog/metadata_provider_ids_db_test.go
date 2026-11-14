package catalog

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestMetadataUpdateKeepsDurableProviderIDsDB pins that an admin's provider-ID
// edit moves the item's durable provider IDs with its columns. Lookups,
// AttachTMDBID's conflict check and a refresh's ID seeding read the durable
// rows, so a stale one keeps naming the ID the admin replaced or cleared.
func TestMetadataUpdateKeepsDurableProviderIDsDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	movie := fmt.Sprintf("edit-provider-ids-movie-%d", stamp)
	other := fmt.Sprintf("edit-provider-ids-other-%d", stamp)
	oldTMDB := strconv.FormatInt(900000000+stamp%1000000, 10)
	newTMDB := strconv.FormatInt(910000000+stamp%1000000, 10)
	heldTMDB := strconv.FormatInt(920000000+stamp%1000000, 10)
	oldIMDb := fmt.Sprintf("tt%08d", 10000000+stamp%1000000)
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movie, other})
	})
	batchEquivExec(t, pool, `
		INSERT INTO media_items (content_id, type, title, genres, tmdb_id, imdb_id)
		VALUES ($1, 'movie', 'Edit Provider IDs', '{}'::text[], $2, $3),
			($4, 'movie', 'Holds An ID', '{}'::text[], $5, '')`,
		movie, oldTMDB, oldIMDb, other, heldTMDB)
	batchEquivExec(t, pool, `
		INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id)
		VALUES ($1, 'movie', 'tmdb', $2), ($1, 'movie', 'imdb', $3), ($4, 'movie', 'tmdb', $5)`,
		movie, oldTMDB, oldIMDb, other, heldTMDB)
	repo := NewItemRepository(pool)

	// A corrected TMDB ID replaces the durable one; the IMDb ID it didn't
	// touch stays.
	if err := repo.UpdateMetadata(ctx, movie, &MetadataUpdate{TmdbID: &newTMDB}); err != nil {
		t.Fatalf("UpdateMetadata(tmdb): %v", err)
	}
	assertDurableProviderIDs(t, pool, movie, map[string]string{"tmdb": newTMDB, "imdb": oldIMDb})

	// Resending an ID the item already has leaves its durable row alone: only a
	// changed ID is a choice, which is what a curator's scope check authorizes.
	batchEquivExec(t, pool, `UPDATE media_item_provider_ids SET provider_id = $2 WHERE content_id = $1 AND provider = 'imdb'`, movie, "tt0000001")
	if err := repo.UpdateMetadata(ctx, movie, &MetadataUpdate{ImdbID: new(" " + strings.ToUpper(oldIMDb))}); err != nil {
		t.Fatalf("UpdateMetadata(resent imdb): %v", err)
	}
	assertDurableProviderIDs(t, pool, movie, map[string]string{"tmdb": newTMDB, "imdb": "tt0000001"})

	// Clearing the IMDb ID removes its durable row, which a refresh would
	// otherwise use to put it back.
	empty := ""
	if err := repo.UpdateMetadata(ctx, movie, &MetadataUpdate{ImdbID: &empty}); err != nil {
		t.Fatalf("UpdateMetadata(clear imdb): %v", err)
	}
	assertDurableProviderIDs(t, pool, movie, map[string]string{"tmdb": newTMDB})

	// An ID another item already holds still saves on the column, as before,
	// and leaves this item no durable row to contradict it.
	if err := repo.UpdateMetadata(ctx, movie, &MetadataUpdate{TmdbID: &heldTMDB}); err != nil {
		t.Fatalf("UpdateMetadata(held tmdb): %v", err)
	}
	assertColumns(t, pool, "media_items", movie, map[string]string{"tmdb_id": heldTMDB})
	assertDurableProviderIDs(t, pool, movie, map[string]string{})
	assertDurableProviderIDs(t, pool, other, map[string]string{"tmdb": heldTMDB})

	// The same holds when the other item's claim commits while the edit waits
	// on it: the unique key then refuses the durable row, not the edit.
	racedTVDB := strconv.FormatInt(930000000+stamp%1000000, 10)
	claim, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin claim: %v", err)
	}
	defer claim.Rollback(ctx) //nolint:errcheck // a no-op once the claim commits
	if _, err := claim.Exec(ctx, `
		INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id)
		VALUES ($1, 'movie', 'tvdb', $2)`, other, racedTVDB); err != nil {
		t.Fatalf("claim tvdb: %v", err)
	}
	edited := make(chan error, 1)
	go func() { edited <- repo.UpdateMetadata(ctx, movie, &MetadataUpdate{TvdbID: &racedTVDB}) }()
	waitForLockWait(t, pool, edited, "INSERT INTO media_item_provider_ids")
	if err := claim.Commit(ctx); err != nil {
		t.Fatalf("commit claim: %v", err)
	}
	if err := <-edited; err != nil {
		t.Fatalf("UpdateMetadata(raced tvdb): %v", err)
	}
	assertColumns(t, pool, "media_items", movie, map[string]string{"tvdb_id": racedTVDB})
	assertDurableProviderIDs(t, pool, movie, map[string]string{})

	// A replace (book enrichment, a legacy refresh) waits for an edit holding
	// the item before it deletes the item's rows, so the two can't deadlock.
	edit, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin edit: %v", err)
	}
	defer edit.Rollback(ctx) //nolint:errcheck // a no-op once the edit commits
	if _, err := edit.Exec(ctx, `SELECT 1 FROM media_items WHERE content_id = $1 FOR UPDATE`, movie); err != nil {
		t.Fatalf("lock item: %v", err)
	}
	replaced := make(chan error, 1)
	go func() {
		replaced <- NewProviderIDRepository(pool).ReplaceByContentID(ctx, movie, map[string]string{"tmdb": newTMDB})
	}()
	waitForLockWait(t, pool, replaced, "SELECT type FROM media_items")
	if err := edit.Commit(ctx); err != nil {
		t.Fatalf("commit edit: %v", err)
	}
	if err := <-replaced; err != nil {
		t.Fatalf("ReplaceByContentID: %v", err)
	}
	assertDurableProviderIDs(t, pool, movie, map[string]string{"tmdb": newTMDB})
}

// waitForLockWait returns once a session of this database waits on a lock
// while running statement, and fails if done reports first.
func waitForLockWait(t *testing.T, pool *pgxpool.Pool, done <-chan error, statement string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(context.Background(), `
			SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND datname = current_database()
			  AND pid <> pg_backend_pid() AND strpos(query, $1) > 0)`, statement).Scan(&waiting); err != nil {
			t.Fatalf("read lock waits: %v", err)
		}
		if waiting {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("finished without waiting on a lock in %q: %v", statement, err)
		case <-deadline:
			t.Fatalf("never waited on a lock in %q", statement)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func assertDurableProviderIDs(t *testing.T, pool *pgxpool.Pool, contentID string, want map[string]string) {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT provider, provider_id FROM media_item_provider_ids WHERE content_id = $1`, contentID)
	if err != nil {
		t.Fatalf("read durable provider ids of %s: %v", contentID, err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var provider, providerID string
		if err := rows.Scan(&provider, &providerID); err != nil {
			t.Fatalf("scan durable provider id: %v", err)
		}
		got[provider] = providerID
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate durable provider ids: %v", err)
	}
	if !maps.Equal(got, want) {
		t.Fatalf("durable provider ids of %s = %v, want %v", contentID, got, want)
	}
}
