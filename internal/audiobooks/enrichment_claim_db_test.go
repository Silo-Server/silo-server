package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
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

// Every server process runs the audiobook task. The database lease is the
// cross-replica boundary: a second process must not receive the first one's
// item, and an expired worker must not be able to stamp state after reclaim.
func TestClaimBatchLeasesAcrossReplicasAndFencesExpiredWorker(t *testing.T) {
	pool := newClaimTestPool(t)
	ctx := context.Background()
	contentID := seedAudiobook(t, pool, "replica-lease", "/covers/embedded.jpg", false)
	if _, err := pool.Exec(ctx, `
		UPDATE media_items SET created_at = '1900-01-01' WHERE content_id = $1
	`, contentID); err != nil {
		t.Fatalf("age lease fixture: %v", err)
	}

	firstReplica := newTestEnricher(pool)
	firstReplica.batchSize = 1
	secondReplica := newTestEnricher(pool)
	secondReplica.batchSize = 1

	firstBatch, err := firstReplica.claimBatch(ctx)
	if err != nil {
		t.Fatalf("first claimBatch: %v", err)
	}
	if len(firstBatch) != 1 || firstBatch[0].ContentID != contentID || firstBatch[0].ClaimToken == "" {
		t.Fatalf("first batch = %+v, want leased fixture %q", firstBatch, contentID)
	}
	firstToken := firstBatch[0].ClaimToken

	secondBatch, err := secondReplica.claimBatch(ctx)
	if err != nil {
		t.Fatalf("second claimBatch: %v", err)
	}
	for _, item := range secondBatch {
		if item.ContentID == contentID {
			t.Fatal("second replica claimed an item whose lease is still active")
		}
		// Do not leave a lease on an unrelated shared-test row.
		_, _ = pool.Exec(ctx, `
			UPDATE audiobook_enrichment_state
			SET claim_token = NULL, lease_until = NULL
			WHERE content_id = $1 AND claim_token = $2
		`, item.ContentID, item.ClaimToken)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE audiobook_enrichment_state
		SET lease_until = now() - interval '1 minute'
		WHERE content_id = $1 AND claim_token = $2
	`, contentID, firstToken); err != nil {
		t.Fatalf("expire first lease: %v", err)
	}

	reclaimed, err := secondReplica.claimBatch(ctx)
	if err != nil {
		t.Fatalf("reclaim batch: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ContentID != contentID || reclaimed[0].ClaimToken == firstToken {
		t.Fatalf("reclaimed batch = %+v, want fixture with a fresh token", reclaimed)
	}

	if err := firstReplica.state.RecordFailure(
		ctx,
		contentID,
		firstToken,
		EnrichmentErrorTransient,
		"stale worker",
	); !errors.Is(err, ErrAudiobookEnrichmentClaimLost) {
		t.Fatalf("stale RecordFailure error = %v, want %v", err, ErrAudiobookEnrichmentClaimLost)
	}

	if err := secondReplica.completeWithoutMetadata(ctx, reclaimed[0], EnrichmentOutcomeNoMatch); err != nil {
		t.Fatalf("complete current claim: %v", err)
	}
	var (
		lastRefreshed *time.Time
		outcome       string
		claimToken    *string
		leaseUntil    *time.Time
	)
	if err := pool.QueryRow(ctx, `
		SELECT mi.last_refreshed, s.outcome, s.claim_token, s.lease_until
		FROM media_items mi
		JOIN audiobook_enrichment_state s ON s.content_id = mi.content_id
		WHERE mi.content_id = $1
	`, contentID).Scan(&lastRefreshed, &outcome, &claimToken, &leaseUntil); err != nil {
		t.Fatalf("read completed lease fixture: %v", err)
	}
	if lastRefreshed == nil || outcome != string(EnrichmentOutcomeNoMatch) || claimToken != nil || leaseUntil != nil {
		t.Fatalf("completed state = refreshed:%v outcome:%q token:%v lease:%v", lastRefreshed, outcome, claimToken, leaseUntil)
	}
}

type failingTxAudiobookItemRepository struct {
	err error
}

func (f *failingTxAudiobookItemRepository) UpdateMetadata(context.Context, string, *catalog.MetadataUpdate) error {
	return f.err
}

func (f *failingTxAudiobookItemRepository) UpdateMetadataTx(context.Context, pgx.Tx, string, *catalog.MetadataUpdate) error {
	return f.err
}

func (f *failingTxAudiobookItemRepository) ReplacePeople(context.Context, string, []models.ItemPerson) error {
	return nil
}

func TestPersistRollsBackProviderIDsWhenMetadataWriteFails(t *testing.T) {
	pool := newClaimTestPool(t)
	contentID := seedAudiobook(t, pool, "atomic-persist", "/covers/embedded.jpg", false)
	updateErr := errors.New("metadata write failed")
	e := &Enricher{
		pool:        pool,
		itemRepo:    &failingTxAudiobookItemRepository{err: updateErr},
		providerIDs: catalog.NewProviderIDRepository(pool),
	}

	err := e.persist(context.Background(), enrichmentItemRow{ContentID: contentID}, map[string]string{
		"asin": fmt.Sprintf("B0TX%d", time.Now().UnixNano()),
	}, &metadata.MetadataResult{HasMetadata: true, Overview: "remote overview"})
	if !errors.Is(err, updateErr) {
		t.Fatalf("persist error = %v, want %v", err, updateErr)
	}

	var (
		providerIDCount int
		overview        string
		lastRefreshed   *time.Time
	)
	if err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT COUNT(*) FROM media_item_provider_ids WHERE content_id = mi.content_id),
			COALESCE(mi.overview, ''),
			mi.last_refreshed
		FROM media_items mi
		WHERE mi.content_id = $1
	`, contentID).Scan(&providerIDCount, &overview, &lastRefreshed); err != nil {
		t.Fatalf("read rolled-back audiobook: %v", err)
	}
	if providerIDCount != 0 || overview != "" || lastRefreshed != nil {
		t.Fatalf("partial enrichment committed: provider_ids=%d overview=%q last_refreshed=%v", providerIDCount, overview, lastRefreshed)
	}
}
