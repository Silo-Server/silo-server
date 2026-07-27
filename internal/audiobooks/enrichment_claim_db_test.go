package audiobooks

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

func newClaimTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedAudiobook inserts one audiobook row and returns its content ID. A
// non-empty poster is the default because that is the production shape: the
// scanner extracts embedded cover art from the file long before enrichment
// runs.
func seedAudiobook(t *testing.T, pool *pgxpool.Pool, label, poster string, refreshed bool) string {
	t.Helper()
	ctx := context.Background()
	contentID := fmt.Sprintf("audiobook-claim-%s-%d", label, time.Now().UnixNano())

	var refreshedAt any
	if refreshed {
		refreshedAt = time.Now()
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (
			content_id, type, title, genres, poster_path, last_refreshed,
			refresh_failures, episode_metadata_incomplete
		) VALUES ($1, 'audiobook', 'Claim Fixture', '{}'::text[], $2, $3, 0, FALSE)
	`, contentID, poster, refreshedAt); err != nil {
		t.Fatalf("seed audiobook %s: %v", contentID, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM media_item_provider_ids WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
	return contentID
}

func giveProviderID(t *testing.T, pool *pgxpool.Pool, contentID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type)
		VALUES ($1, 'asin', $2, 'audiobook')
	`, contentID, "B0"+contentID[len(contentID)-8:]); err != nil {
		t.Fatalf("seed provider id for %s: %v", contentID, err)
	}
}

// newTestEnricher builds an Enricher wired to a real pool, matching what the
// constructor does, so DB-backed tests exercise the same state store as
// production rather than a hand-assembled struct that can drift from it.
func newTestEnricher(pool *pgxpool.Pool) *Enricher {
	return &Enricher{
		pool:      pool,
		chainRepo: metadata.NewChainRepository(pool),
		state:     newEnrichmentStateStore(pool),
		batchSize: 500,
	}
}

func claimedIDs(t *testing.T, e *Enricher) map[string]bool {
	t.Helper()
	rows, err := e.claimBatch(context.Background())
	if err != nil {
		t.Fatalf("claimBatch: %v", err)
	}
	got := make(map[string]bool, len(rows))
	for _, r := range rows {
		got[r.ContentID] = true
	}
	return got
}

// TestClaimBatchSelectsOnIdentityNotCoverArt pins the fix for the production
// stall: eligibility must key on whether an item has a provider identity, not
// on whether it has a poster.
//
// The predicate used to require an empty poster_path. Audiobook files
// essentially always carry embedded cover art, so the scanner stamped a poster
// on every item before enrichment looked at it, the predicate matched nothing,
// and the sweep went permanently idle — 0 rows eligible while 5,712 audiobooks
// held no provider ID at all. An item with a cover and no identity is the exact
// row that regression hid, so it leads here.
func TestClaimBatchSelectsOnIdentityNotCoverArt(t *testing.T) {
	pool := newClaimTestPool(t)
	e := &Enricher{pool: pool, chainRepo: metadata.NewChainRepository(pool), batchSize: 500}

	coveredUnidentified := seedAudiobook(t, pool, "covered", "/covers/embedded.jpg", false)
	bareUnidentified := seedAudiobook(t, pool, "bare", "", false)

	identified := seedAudiobook(t, pool, "identified", "/covers/embedded.jpg", false)
	giveProviderID(t, pool, identified)

	alreadyPassed := seedAudiobook(t, pool, "passed", "", true)

	got := claimedIDs(t, e)

	if !got[coveredUnidentified] {
		t.Error("an audiobook with embedded cover art but no provider identity was not claimed; " +
			"this is the production row the poster_path predicate hid")
	}
	if !got[bareUnidentified] {
		t.Error("an audiobook with no poster and no provider identity was not claimed")
	}
	if got[identified] {
		t.Error("an audiobook that already has a provider identity was claimed; " +
			"identified items must not be re-enriched")
	}
	if got[alreadyPassed] {
		t.Error("an audiobook with last_refreshed set was claimed; last_refreshed is the " +
			"retry bound that stops unmatchable items looping against the provider")
	}
}

// TestHasPendingItemsMirrorsClaimBatch guards the invariant the two queries
// document but nothing enforced: the scheduler gate and the selection query
// must agree. If they drift, either the sweep never wakes for work that exists
// or it wakes every interval to claim nothing.
func TestHasPendingItemsMirrorsClaimBatch(t *testing.T) {
	pool := newClaimTestPool(t)
	e := &Enricher{pool: pool, chainRepo: metadata.NewChainRepository(pool), batchSize: 500}

	// Whatever else is in the test database, an unidentified item with a cover
	// must make both agree that there is work.
	fixtureID := seedAudiobook(t, pool, "mirror", "/covers/embedded.jpg", false)

	pending, err := e.HasPendingItems(context.Background())
	if err != nil {
		t.Fatalf("HasPendingItems: %v", err)
	}
	if !pending {
		t.Fatal("HasPendingItems reported no work while an unidentified audiobook exists")
	}
	// Assert the fixture itself, not just a non-empty set: unrelated rows in a
	// shared test database could otherwise satisfy the check.
	if !claimedIDs(t, e)[fixtureID] {
		t.Fatal("HasPendingItems reported work but claimBatch did not claim the eligible fixture")
	}
}
