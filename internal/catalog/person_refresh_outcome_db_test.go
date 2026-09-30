package catalog

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func personRefreshTestRepo(t *testing.T) (*PersonRepository, *pgxpool.Pool) {
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
	return NewPersonRepository(pool), pool
}

// seedRefreshPerson inserts a person with an external id and incomplete
// metadata, returning its id. Ids grow with each call, so later seeds are
// "newer".
func seedRefreshPerson(t *testing.T, pool *pgxpool.Pool, label string) int64 {
	t.Helper()
	id := time.Now().UnixNano()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO people (id, name, tmdb_id, updated_at)
		VALUES ($1, $2, $3, NOW())`, id, "Refresh "+label, fmt.Sprintf("refresh-%s-%d", label, id)); err != nil {
		t.Fatalf("seed person %s: %v", label, err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM people WHERE id = $1`, id) })
	return id
}

type refreshState struct {
	outcome  *string
	failures int
	dueIn    *time.Duration
}

func readRefreshState(t *testing.T, pool *pgxpool.Pool, id int64) refreshState {
	t.Helper()
	var state refreshState
	var dueAt *time.Time
	var now time.Time
	if err := pool.QueryRow(context.Background(), `
		SELECT metadata_refresh_outcome, metadata_refresh_failures, metadata_refresh_due_at, NOW()
		FROM people WHERE id = $1`, id).Scan(&state.outcome, &state.failures, &dueAt, &now); err != nil {
		t.Fatalf("read person %d: %v", id, err)
	}
	if dueAt != nil {
		d := dueAt.Sub(now)
		state.dueIn = &d
	}
	return state
}

func requireDueIn(t *testing.T, label string, state refreshState, want time.Duration) {
	t.Helper()
	if state.dueIn == nil {
		t.Fatalf("%s: due never, want in %s", label, want)
	}
	if diff := *state.dueIn - want; diff < -time.Minute || diff > time.Minute {
		t.Fatalf("%s: due in %s, want %s", label, *state.dueIn, want)
	}
}

// Each outcome sets when the sweep looks the person up again (#1606).
func TestPersonRefreshOutcomeSchedulesNextLookupPostgres(t *testing.T) {
	repo, pool := personRefreshTestRepo(t)
	ctx := context.Background()

	answered := seedRefreshPerson(t, pool, "answered")
	if err := repo.MarkRefreshAttempt(ctx, answered); err != nil {
		t.Fatal(err)
	}
	requireDueIn(t, "attempt lease", readRefreshState(t, pool, answered), PersonRefreshAttemptLease)
	if err := repo.RecordRefreshOutcome(ctx, answered, PersonRefreshAnswered); err != nil {
		t.Fatal(err)
	}
	state := readRefreshState(t, pool, answered)
	if state.outcome == nil || *state.outcome != "answered" || state.failures != 0 {
		t.Fatalf("answered: outcome=%v failures=%d", state.outcome, state.failures)
	}
	requireDueIn(t, "answered", state, PersonMetadataStaleAfter)

	failed := seedRefreshPerson(t, pool, "failed")
	for attempt, want := range []time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour} {
		if err := repo.RecordRefreshOutcome(ctx, failed, PersonRefreshFailed); err != nil {
			t.Fatal(err)
		}
		requireDueIn(t, fmt.Sprintf("failure %d", attempt+1), readRefreshState(t, pool, failed), want)
	}
	if _, err := pool.Exec(ctx, `UPDATE people SET metadata_refresh_failures = 40 WHERE id = $1`, failed); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordRefreshOutcome(ctx, failed, PersonRefreshFailed); err != nil {
		t.Fatal(err)
	}
	requireDueIn(t, "capped failure", readRefreshState(t, pool, failed), PersonRefreshRetryAfter)
	// An answer resets the count.
	if err := repo.RecordRefreshOutcome(ctx, failed, PersonRefreshAnswered); err != nil {
		t.Fatal(err)
	}
	if state := readRefreshState(t, pool, failed); state.failures != 0 {
		t.Fatalf("failures after an answer = %d, want 0", state.failures)
	}

	missing := seedRefreshPerson(t, pool, "missing")
	for attempt := 1; attempt < PersonRefreshNotFoundAttempts; attempt++ {
		if err := repo.RecordRefreshOutcome(ctx, missing, PersonRefreshNotFound); err != nil {
			t.Fatal(err)
		}
		requireDueIn(t, fmt.Sprintf("not found %d", attempt), readRefreshState(t, pool, missing), PersonRefreshRetryAfter)
	}
	if err := repo.RecordRefreshOutcome(ctx, missing, PersonRefreshNotFound); err != nil {
		t.Fatal(err)
	}
	if state := readRefreshState(t, pool, missing); state.dueIn != nil {
		t.Fatalf("not found after %d attempts: due in %s, want never", PersonRefreshNotFoundAttempts, *state.dueIn)
	}

	// Failures don't count toward giving up: only not-found lookups in a row do.
	flaky := seedRefreshPerson(t, pool, "flaky")
	for _, outcome := range []PersonRefreshOutcome{PersonRefreshFailed, PersonRefreshFailed, PersonRefreshNotFound} {
		if err := repo.RecordRefreshOutcome(ctx, flaky, outcome); err != nil {
			t.Fatal(err)
		}
	}
	state = readRefreshState(t, pool, flaky)
	if state.failures != 1 {
		t.Fatalf("not-found streak after two failures = %d, want 1", state.failures)
	}
	requireDueIn(t, "not found after failures", state, PersonRefreshRetryAfter)
	if err := repo.RecordRefreshOutcome(ctx, flaky, PersonRefreshFailed); err != nil {
		t.Fatal(err)
	}
	requireDueIn(t, "failure after not found", readRefreshState(t, pool, flaky), PersonRefreshFailureBackoff)

	if err := repo.RecordRefreshOutcome(ctx, answered, PersonRefreshOutcome("bogus")); err == nil {
		t.Fatal("unknown outcome was accepted")
	}
}

// The sweep takes people never looked up first, newest first, then people
// whose next lookup is due, earliest first; people not yet due wait (#1606).
func TestFindRefreshCandidatesOrderPostgres(t *testing.T) {
	repo, pool := personRefreshTestRepo(t)
	ctx := context.Background()

	olderNew := seedRefreshPerson(t, pool, "older-new")
	newerNew := seedRefreshPerson(t, pool, "newer-new")
	dueLater := seedRefreshPerson(t, pool, "due-later")
	dueSooner := seedRefreshPerson(t, pool, "due-sooner")
	notDue := seedRefreshPerson(t, pool, "not-due")
	givenUp := seedRefreshPerson(t, pool, "given-up")
	for id, due := range map[int64]string{
		dueLater:  "NOW() - interval '1 hour'",
		dueSooner: "NOW() - interval '2 hours'",
		notDue:    "NOW() + interval '1 hour'",
		givenUp:   "NULL",
	} {
		if _, err := pool.Exec(ctx, `
			UPDATE people SET metadata_refresh_attempted_at = NOW() - interval '1 day',
				metadata_refresh_outcome = 'failed', metadata_refresh_due_at = `+due+`
			WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := repo.FindRefreshCandidates(ctx, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	position := func(id int64) int { return slices.Index(ids, id) }
	for _, id := range []int64{olderNew, newerNew, dueLater, dueSooner} {
		if position(id) < 0 {
			t.Fatalf("person %d missing from candidates", id)
		}
	}
	if position(newerNew) > position(olderNew) {
		t.Errorf("newer never-looked-up person came after the older one")
	}
	if position(olderNew) > position(dueSooner) {
		t.Errorf("a person due again came before a never-looked-up person")
	}
	if position(dueSooner) > position(dueLater) {
		t.Errorf("the later due person came before the earlier one")
	}
	if position(notDue) >= 0 || position(givenUp) >= 0 {
		t.Errorf("a person not due (%d) or given up (%d) was a candidate", position(notDue), position(givenUp))
	}

	if ids, err := repo.FindRefreshCandidates(ctx, 1); err != nil || len(ids) != 1 {
		t.Fatalf("limit 1 returned %v, %v", ids, err)
	}
}

// Two outcomes recorded at once for the same person must both count. The
// second write waits on the first's row lock, then must build its streak from
// the row the first wrote, not from the statement's older snapshot.
func TestRecordRefreshOutcomeConcurrentWritesBothCountPostgres(t *testing.T) {
	repo, pool := personRefreshTestRepo(t)
	ctx := context.Background()
	id := seedRefreshPerson(t, pool, "concurrent")

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT id FROM people WHERE id = $1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- repo.RecordRefreshOutcome(ctx, id, PersonRefreshFailed) }()
	}
	// Both writes must be waiting on the lock before it is released.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%metadata_refresh_outcome = $2%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d outcome writes waiting on the row lock", waiting)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if state := readRefreshState(t, pool, id); state.failures != 2 {
		t.Fatalf("failures after two concurrent failed outcomes = %d, want 2", state.failures)
	}
}
