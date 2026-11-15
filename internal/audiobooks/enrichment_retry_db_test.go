package audiobooks

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

type retryRow struct {
	attempts, noMatches, failures int
	outcome, errorClass           *string
	parkedFor                     *time.Duration
	completed                     bool
}

func readRetryRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, contentID string) retryRow {
	t.Helper()
	var row retryRow
	var parkedSecs *float64
	if err := pool.QueryRow(ctx, `
		SELECT attempts, no_matches, failures, outcome, last_error_class,
		       EXTRACT(EPOCH FROM next_attempt_at - now())::float8,
		       completed_at IS NOT NULL
		FROM audiobook_enrichment_state WHERE content_id = $1
	`, contentID).Scan(&row.attempts, &row.noMatches, &row.failures, &row.outcome, &row.errorClass, &parkedSecs, &row.completed); err != nil {
		t.Fatal(err)
	}
	if parkedSecs != nil {
		d := time.Duration(*parkedSecs * float64(time.Second)).Round(time.Minute)
		row.parkedFor = &d
	}
	return row
}

func newRetryTestItem(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	contentID := "ab-retry-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status)
		VALUES ($1, 'audiobook', 'Book', 'pending')
	`, contentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
	return contentID
}

// claim leases the row the way claimBatch does, so the claimed write paths run.
func claimRetryTestItem(t *testing.T, ctx context.Context, pool *pgxpool.Pool, contentID string) string {
	t.Helper()
	token := uuid.NewString()
	if _, err := pool.Exec(ctx, `
		INSERT INTO audiobook_enrichment_state (content_id, claim_token, lease_until, updated_at)
		VALUES ($1, $2, now() + interval '1 hour', now())
		ON CONFLICT (content_id) DO UPDATE SET claim_token = $2, lease_until = now() + interval '1 hour'
	`, contentID, token); err != nil {
		t.Fatal(err)
	}
	return token
}

// Provider failures back off exponentially, honor a retry-after, and end in
// a terminal 'failed' outcome at the cap instead of retrying forever. A
// failure between clean no-matches does not restart the no-match count.
func TestAudiobookEnrichmentRetryStateDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := newEnrichmentStateStore(pool)

	for name, check := range map[string]func(*testing.T, context.Context, *pgxpool.Pool, *enrichmentStateStore){
		"transient failures back off and become terminal": checkTransientFailuresBecomeTerminal,
		"retry-after raises the backoff":                  checkRetryAfterRaisesBackoff,
		"permanent failure is rechecked once":             checkPermanentFailureRecheckedOnce,
		"failures between no-matches keep the streak":     checkFailuresKeepNoMatchStreak,
		"a deferral costs no attempt":                     checkDeferralCostsNoAttempt,
		"an unclaimed write creates the row":              checkUnclaimedWriteCreatesRow,
	} {
		t.Run(name, func(t *testing.T) { check(t, ctx, pool, store) })
	}
}

func parkedMinutes(m int) time.Duration { return time.Duration(m) * time.Minute }

func checkTransientFailuresBecomeTerminal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, store *enrichmentStateStore) {
	id := newRetryTestItem(t, ctx, pool)
	want := []int{15, 30, 60, 120, 240, 480, 960, 1440, 1440}
	for i, wantMinutes := range want {
		token := claimRetryTestItem(t, ctx, pool, id)
		if err := store.RecordFailure(ctx, id, token, EnrichmentErrorTransient, 0, "down"); err != nil {
			t.Fatal(err)
		}
		row := readRetryRow(t, ctx, pool, id)
		if row.outcome != nil || row.failures != i+1 || row.parkedFor == nil || *row.parkedFor != parkedMinutes(wantMinutes) {
			t.Fatalf("failure %d: outcome=%v failures=%d parked=%v, want pending, %d, %dm",
				i+1, row.outcome, row.failures, row.parkedFor, i+1, wantMinutes)
		}
	}
	token := claimRetryTestItem(t, ctx, pool, id)
	if err := store.RecordFailure(ctx, id, token, EnrichmentErrorTransient, 0, "down"); err != nil {
		t.Fatal(err)
	}
	row := readRetryRow(t, ctx, pool, id)
	if row.outcome == nil || *row.outcome != string(EnrichmentOutcomeFailed) || !row.completed || row.parkedFor != nil {
		t.Fatalf("at the cap: outcome=%v completed=%v parked=%v, want terminal failed", row.outcome, row.completed, row.parkedFor)
	}
	if row.errorClass == nil || *row.errorClass != string(EnrichmentErrorTransient) {
		t.Fatalf("error class = %v, want the last failure kept for admins", row.errorClass)
	}
}

func checkRetryAfterRaisesBackoff(t *testing.T, ctx context.Context, pool *pgxpool.Pool, store *enrichmentStateStore) {
	id := newRetryTestItem(t, ctx, pool)
	token := claimRetryTestItem(t, ctx, pool, id)
	if err := store.RecordFailure(ctx, id, token, EnrichmentErrorRateLimited, 3*time.Hour, "slow down"); err != nil {
		t.Fatal(err)
	}
	if row := readRetryRow(t, ctx, pool, id); row.parkedFor == nil || *row.parkedFor != 3*time.Hour {
		t.Fatalf("parked = %v, want the 3h retry-after over the 1h step", row.parkedFor)
	}
}

func checkPermanentFailureRecheckedOnce(t *testing.T, ctx context.Context, pool *pgxpool.Pool, store *enrichmentStateStore) {
	id := newRetryTestItem(t, ctx, pool)
	for range audiobookPermanentFailureRetryLimit {
		token := claimRetryTestItem(t, ctx, pool, id)
		if err := store.RecordFailure(ctx, id, token, EnrichmentErrorPermanent, 0, "invalid argument"); err != nil {
			t.Fatal(err)
		}
	}
	if row := readRetryRow(t, ctx, pool, id); row.outcome == nil || *row.outcome != string(EnrichmentOutcomeFailed) {
		t.Fatalf("outcome = %v, want failed after %d permanent failures", row.outcome, audiobookPermanentFailureRetryLimit)
	}
}

func checkFailuresKeepNoMatchStreak(t *testing.T, ctx context.Context, pool *pgxpool.Pool, store *enrichmentStateStore) {
	id := newRetryTestItem(t, ctx, pool)
	noMatch := func(token string) error { return store.RecordOutcome(ctx, id, token, EnrichmentOutcomeNoMatch) }
	flake := func(token string) error {
		return store.RecordFailure(ctx, id, token, EnrichmentErrorTransient, 0, "flake")
	}
	steps := []func(string) error{noMatch, flake, noMatch, flake, noMatch}
	for _, step := range steps {
		if err := step(claimRetryTestItem(t, ctx, pool, id)); err != nil {
			t.Fatal(err)
		}
	}
	row := readRetryRow(t, ctx, pool, id)
	if row.noMatches != audiobookNoMatchRetryLimit || row.outcome == nil || *row.outcome != string(EnrichmentOutcomeNoMatch) || !row.completed {
		t.Fatalf("no_matches=%d outcome=%v completed=%v, want a terminal no_match after %d", row.noMatches, row.outcome, row.completed, audiobookNoMatchRetryLimit)
	}
	if row.attempts != len(steps) || row.failures != 0 {
		t.Fatalf("attempts=%d failures=%d, want %d and 0", row.attempts, row.failures, len(steps))
	}
}

func checkDeferralCostsNoAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, store *enrichmentStateStore) {
	id := newRetryTestItem(t, ctx, pool)
	token := claimRetryTestItem(t, ctx, pool, id)
	if err := store.Defer(ctx, id, token, time.Now().Add(40*time.Minute)); err != nil {
		t.Fatal(err)
	}
	row := readRetryRow(t, ctx, pool, id)
	if row.attempts != 0 || row.parkedFor == nil || *row.parkedFor != 40*time.Minute {
		t.Fatalf("attempts=%d parked=%v, want 0 and 40m", row.attempts, row.parkedFor)
	}
	if err := store.Defer(ctx, id, token, time.Now()); !errors.Is(err, ErrAudiobookEnrichmentClaimLost) {
		t.Fatalf("second Defer = %v, want the released claim reported lost", err)
	}
}

func checkUnclaimedWriteCreatesRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, store *enrichmentStateStore) {
	id := newRetryTestItem(t, ctx, pool)
	if err := store.RecordFailure(ctx, id, "", EnrichmentErrorTransient, 0, "down"); err != nil {
		t.Fatal(err)
	}
	if row := readRetryRow(t, ctx, pool, id); row.attempts != 1 || row.failures != 1 {
		t.Fatalf("attempts=%d failures=%d, want 1 and 1", row.attempts, row.failures)
	}

	// last_error is kept for admins, so a secret echoed by a provider is
	// masked, whether bare or nested in a gRPC status description.
	for _, cause := range []string{
		"api_key=hunter2secret",
		"rpc error: code = Unknown desc = api_key=hunter2secret",
	} {
		if err := store.RecordFailure(ctx, id, "", EnrichmentErrorTransient, 0, cause); err != nil {
			t.Fatal(err)
		}
		var lastError string
		if err := pool.QueryRow(ctx, `SELECT last_error FROM audiobook_enrichment_state WHERE content_id = $1`, id).Scan(&lastError); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(lastError, "hunter2secret") {
			t.Fatalf("last_error for %q = %q, want the secret masked", cause, lastError)
		}
	}
}

// newSweepTestLibrary creates an audiobook library and returns a function that
// adds an unenriched book to it with the given creation time.
func newSweepTestLibrary(t *testing.T, ctx context.Context, pool *pgxpool.Pool) func(time.Time) string {
	t.Helper()
	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name) VALUES ('audiobooks', $1) RETURNING id
	`, "ab-sweep-"+uuid.NewString()).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	return func(createdAt time.Time) string {
		id := newRetryTestItem(t, ctx, pool)
		if _, err := pool.Exec(ctx, `UPDATE media_items SET created_at = $2 WHERE content_id = $1`, id, createdAt); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, folderID); err != nil {
			t.Fatal(err)
		}
		return id
	}
}

// runWhileAnotherNodeSweeps holds the sweep lock as another node would and
// runs the sweep and its scheduling check, which must both stand down.
func runWhileAnotherNodeSweeps(t *testing.T, ctx context.Context, pool *pgxpool.Pool, e *Enricher) {
	t.Helper()
	held, acquired, err := pglock.TryAcquire(ctx, pool, enrichSweepLock)
	if err != nil || !acquired {
		t.Fatalf("taking the sweep lock: acquired=%v err=%v", acquired, err)
	}
	defer func() { _ = held.Release(ctx) }()
	if _, err := e.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if pending, err := e.HasPendingItems(ctx); err != nil || pending {
		t.Fatalf("HasPendingItems while another node sweeps = (%v, %v), want false", pending, err)
	}
}

// One sweep: a node that finds the sweep lock taken does nothing; otherwise a
// rate-limited answer with retry-after parks that item and defers the rest of
// the batch without calling the provider again, and a terminally failed item
// is never claimed.
func TestAudiobookEnrichmentSweepRateLimitDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	addItem := newSweepTestLibrary(t, ctx, pool)
	// Oldest first, so they lead the batch in this order.
	base := time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)
	failed := addItem(base)
	throttled := addItem(base.Add(time.Minute))
	deferred := addItem(base.Add(2 * time.Minute))
	if _, err := pool.Exec(ctx, `
		INSERT INTO audiobook_enrichment_state (content_id, attempts, failures, outcome, last_error_class, completed_at)
		VALUES ($1, 10, 10, 'failed', 'transient', now())
	`, failed); err != nil {
		t.Fatal(err)
	}

	retry, err := status.New(codes.ResourceExhausted, "slow down").WithDetails(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(3 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &countingProvider{fakeAudiobookMetadataProvider: fakeAudiobookMetadataProvider{slug: "abm", searchErr: retry.Err()}}
	e := &Enricher{
		pool:      pool,
		chainRepo: metadata.NewChainRepository(pool),
		state:     newEnrichmentStateStore(pool),
		batchSize: 1000,
		workers:   1,
		resolveProviders: func(context.Context, int, string) ([]metadata.Provider, error) {
			return []metadata.Provider{provider}, nil
		},
	}

	runWhileAnotherNodeSweeps(t, ctx, pool, e)
	if provider.searches != 0 {
		t.Fatalf("searches = %d while another node held the sweep lock, want 0", provider.searches)
	}

	if _, err := e.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if provider.searches != 1 {
		t.Fatalf("searches = %d, want 1: the rest of the sweep must leave a rate-limited provider alone", provider.searches)
	}
	if row := readRetryRow(t, ctx, pool, failed); row.attempts != 10 || row.outcome == nil || *row.outcome != string(EnrichmentOutcomeFailed) {
		t.Fatalf("terminal item: attempts=%d outcome=%v, want it left alone", row.attempts, row.outcome)
	}
	row := readRetryRow(t, ctx, pool, throttled)
	if row.attempts != 1 || row.errorClass == nil || *row.errorClass != string(EnrichmentErrorRateLimited) || row.parkedFor == nil || *row.parkedFor != 3*time.Hour {
		t.Fatalf("throttled item: attempts=%d class=%v parked=%v, want 1, rate_limited, 3h", row.attempts, row.errorClass, row.parkedFor)
	}
	row = readRetryRow(t, ctx, pool, deferred)
	if row.attempts != 0 || row.parkedFor == nil || *row.parkedFor != 3*time.Hour {
		t.Fatalf("deferred item: attempts=%d parked=%v, want 0 and 3h", row.attempts, row.parkedFor)
	}

	// With a second provider that answers but finds nothing, the paused one
	// still keeps the item out of no_match, and since no paused provider was
	// asked, the item is deferred rather than charged a failure.
	answering := &countingProvider{fakeAudiobookMetadataProvider: fakeAudiobookMetadataProvider{slug: "answering"}}
	e.resolveProviders = func(context.Context, int, string) ([]metadata.Provider, error) {
		return []metadata.Provider{provider, answering}, nil
	}
	partial := addItem(base.Add(3 * time.Minute))
	if _, err := e.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if answering.searches != 1 || provider.searches != 1 {
		t.Fatalf("searches: answering=%d paused=%d, want 1 and still 1", answering.searches, provider.searches)
	}
	row = readRetryRow(t, ctx, pool, partial)
	if row.attempts != 0 || row.outcome != nil || row.parkedFor == nil || *row.parkedFor != 3*time.Hour {
		t.Fatalf("partly paused item: attempts=%d outcome=%v parked=%v, want deferred 3h with no outcome", row.attempts, row.outcome, row.parkedFor)
	}
	checkSweepRunsOnOneConnection(t, ctx, dsn, provider)
	t.Run("a sweep lock in another database", func(t *testing.T) {
		checkSweepLockIsPerDatabase(t, ctx, dsn, e)
	})
}

// Advisory locks belong to one database, but pg_locks lists the whole
// server. A sweep holding the same key in another database on the server
// must not make this one report that its sweep is taken.
func checkSweepLockIsPerDatabase(t *testing.T, ctx context.Context, dsn string, e *Enricher) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = "postgres"
	other, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Skipf("no second database to hold the lock in: %v", err)
	}
	defer func() { _ = other.Close(context.WithoutCancel(ctx)) }()
	var acquired bool
	if err := other.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, enrichSweepLock).Scan(&acquired); err != nil || !acquired {
		t.Fatalf("taking the sweep key in the other database: acquired=%v err=%v", acquired, err)
	}
	held, err := e.sweepLockHeld(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Fatal("a lock held in another database counted as this database's sweep")
	}
}

// The sweep lock holds a pooled connection, so a one-connection pool sweeps
// without it instead of waiting on itself.
func checkSweepRunsOnOneConnection(t *testing.T, ctx context.Context, dsn string, provider metadata.Provider) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	small, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	e := &Enricher{
		pool: small, chainRepo: metadata.NewChainRepository(small), state: newEnrichmentStateStore(small),
		batchSize: 1000, workers: 1,
		resolveProviders: func(context.Context, int, string) ([]metadata.Provider, error) {
			return []metadata.Provider{provider}, nil
		},
	}
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := e.Run(runCtx); err != nil {
		t.Fatalf("Run on a one-connection pool = %v, want it to finish", err)
	}
}

// One run keeps claiming batches through the real claim and summary until no
// book is due, rather than stopping after the first batch.
func TestAudiobookEnrichmentSweepDrainsBacklogDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	addItem := newSweepTestLibrary(t, ctx, pool)
	base := time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)
	var items []string
	for i := range 5 {
		items = append(items, addItem(base.Add(time.Duration(i)*time.Minute)))
	}
	provider := &countingProvider{fakeAudiobookMetadataProvider: fakeAudiobookMetadataProvider{slug: "abm"}}
	e := &Enricher{
		pool: pool, chainRepo: metadata.NewChainRepository(pool), state: newEnrichmentStateStore(pool),
		batchSize: 2, workers: 1,
		resolveProviders: func(context.Context, int, string) ([]metadata.Provider, error) {
			return []metadata.Provider{provider}, nil
		},
	}
	lock, acquired, err := e.takeSweepLock(ctx)
	if err != nil || !acquired {
		t.Fatalf("taking the sweep lock: acquired=%v err=%v", acquired, err)
	}
	defer lock.release()
	noGap := func(context.Context, time.Duration) error { return nil }
	if _, err := e.sweep(ctx, lock.check, e.runOneBatch, noGap); err != nil {
		t.Fatal(err)
	}
	for _, id := range items {
		if row := readRetryRow(t, ctx, pool, id); row.outcome == nil || *row.outcome != string(EnrichmentOutcomeNoMatch) {
			t.Fatalf("item %s outcome = %v after one run, want no_match", id, row.outcome)
		}
	}
}
