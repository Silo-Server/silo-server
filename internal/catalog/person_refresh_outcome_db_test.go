package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
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

// identityOf reads the provider ids a lookup of person id would ask about.
func identityOf(t *testing.T, pool *pgxpool.Pool, id int64) PersonIdentity {
	t.Helper()
	var identity PersonIdentity
	if err := pool.QueryRow(context.Background(),
		`SELECT tmdb_id, imdb_id, tvdb_id FROM people WHERE id = $1`, id,
	).Scan(&identity.TmdbID, &identity.ImdbID, &identity.TvdbID); err != nil {
		t.Fatalf("read person %d ids: %v", id, err)
	}
	return identity
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
	if err := repo.RecordRefreshOutcome(ctx, answered, identityOf(t, pool, answered), PersonRefreshAnswered); err != nil {
		t.Fatal(err)
	}
	state := readRefreshState(t, pool, answered)
	if state.outcome == nil || *state.outcome != "answered" || state.failures != 0 {
		t.Fatalf("answered: outcome=%v failures=%d", state.outcome, state.failures)
	}
	requireDueIn(t, "answered", state, PersonMetadataStaleAfter)

	failed := seedRefreshPerson(t, pool, "failed")
	for attempt, want := range []time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour} {
		if err := repo.RecordRefreshOutcome(ctx, failed, identityOf(t, pool, failed), PersonRefreshFailed); err != nil {
			t.Fatal(err)
		}
		requireDueIn(t, fmt.Sprintf("failure %d", attempt+1), readRefreshState(t, pool, failed), want)
	}
	if _, err := pool.Exec(ctx, `UPDATE people SET metadata_refresh_failures = 40 WHERE id = $1`, failed); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordRefreshOutcome(ctx, failed, identityOf(t, pool, failed), PersonRefreshFailed); err != nil {
		t.Fatal(err)
	}
	requireDueIn(t, "capped failure", readRefreshState(t, pool, failed), PersonRefreshRetryAfter)
	// An answer resets the count.
	if err := repo.RecordRefreshOutcome(ctx, failed, identityOf(t, pool, failed), PersonRefreshAnswered); err != nil {
		t.Fatal(err)
	}
	if state := readRefreshState(t, pool, failed); state.failures != 0 {
		t.Fatalf("failures after an answer = %d, want 0", state.failures)
	}

	missing := seedRefreshPerson(t, pool, "missing")
	for attempt := 1; attempt < PersonRefreshNotFoundAttempts; attempt++ {
		if err := repo.RecordRefreshOutcome(ctx, missing, identityOf(t, pool, missing), PersonRefreshNotFound); err != nil {
			t.Fatal(err)
		}
		requireDueIn(t, fmt.Sprintf("not found %d", attempt), readRefreshState(t, pool, missing), PersonRefreshRetryAfter)
	}
	if err := repo.RecordRefreshOutcome(ctx, missing, identityOf(t, pool, missing), PersonRefreshNotFound); err != nil {
		t.Fatal(err)
	}
	if state := readRefreshState(t, pool, missing); state.dueIn != nil {
		t.Fatalf("not found after %d attempts: due in %s, want never", PersonRefreshNotFoundAttempts, *state.dueIn)
	}

	// Failures don't count toward giving up: only not-found lookups in a row do.
	flaky := seedRefreshPerson(t, pool, "flaky")
	for _, outcome := range []PersonRefreshOutcome{PersonRefreshFailed, PersonRefreshFailed, PersonRefreshNotFound} {
		if err := repo.RecordRefreshOutcome(ctx, flaky, identityOf(t, pool, flaky), outcome); err != nil {
			t.Fatal(err)
		}
	}
	state = readRefreshState(t, pool, flaky)
	if state.failures != 1 {
		t.Fatalf("not-found streak after two failures = %d, want 1", state.failures)
	}
	requireDueIn(t, "not found after failures", state, PersonRefreshRetryAfter)
	if err := repo.RecordRefreshOutcome(ctx, flaky, identityOf(t, pool, flaky), PersonRefreshFailed); err != nil {
		t.Fatal(err)
	}
	requireDueIn(t, "failure after not found", readRefreshState(t, pool, flaky), PersonRefreshFailureBackoff)

	if err := repo.RecordRefreshOutcome(ctx, answered, identityOf(t, pool, answered), PersonRefreshOutcome("bogus")); err == nil {
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

// An API server older than the outcome columns records only the attempt.
// Once that attempt is a retry interval old the person is a candidate again,
// as the older server would have made them; a recent one waits (#1606).
func TestFindRefreshCandidatesRetriesAttemptsWithoutAnOutcomePostgres(t *testing.T) {
	repo, pool := personRefreshTestRepo(t)
	ctx := context.Background()

	stale := seedRefreshPerson(t, pool, "older-server-stale")
	recent := seedRefreshPerson(t, pool, "older-server-recent")
	for id, attempted := range map[int64]time.Duration{stale: 8 * 24 * time.Hour, recent: 24 * time.Hour} {
		if _, err := pool.Exec(ctx, `
			UPDATE people SET metadata_refresh_attempted_at = NOW() - make_interval(secs => $2)
			WHERE id = $1`, id, attempted.Seconds()); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := repo.FindRefreshCandidates(ctx, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ids, stale) {
		t.Error("a week-old attempt without an outcome is not a candidate")
	}
	if slices.Contains(ids, recent) {
		t.Error("a day-old attempt without an outcome is already a candidate")
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

	identity := identityOf(t, pool, id)
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- repo.RecordRefreshOutcome(ctx, id, identity, PersonRefreshFailed) }()
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

// A person the providers didn't know under their old id is looked up again
// once an admin corrects it; an update that keeps the ids keeps the outcome
// (#1606).
func TestPersonIDChangeResetsRefreshOutcomePostgres(t *testing.T) {
	repo, pool := personRefreshTestRepo(t)
	ctx := context.Background()

	id := seedRefreshPerson(t, pool, "corrected-id")
	if _, err := pool.Exec(ctx, `
		UPDATE people SET metadata_refresh_attempted_at = NOW() - interval '1 day',
			metadata_refresh_outcome = 'not_found', metadata_refresh_failures = 3,
			metadata_refresh_due_at = NULL
		WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	person, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	person.Name += " (renamed)"
	if err := repo.Update(ctx, *person); err != nil {
		t.Fatal(err)
	}
	if state := readRefreshState(t, pool, id); state.outcome == nil || *state.outcome != "not_found" || state.dueIn != nil {
		t.Fatalf("an update keeping the ids changed the outcome: %+v", state)
	}

	staleIdentity := identityOf(t, pool, id)
	person.TmdbID += "-corrected"
	if err := repo.Update(ctx, *person); err != nil {
		t.Fatal(err)
	}
	// A lookup that started under the old id finishes after the correction:
	// its outcome is about the old identity and isn't recorded.
	if err := repo.RecordRefreshOutcome(ctx, id, staleIdentity, PersonRefreshNotFound); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale outcome err = %v, want pgx.ErrNoRows", err)
	}
	state := readRefreshState(t, pool, id)
	if state.outcome != nil || state.failures != 0 || state.dueIn == nil || *state.dueIn > time.Minute {
		t.Fatalf("after correcting the id: %+v, want no outcome, no failures, due now", state)
	}
	ids, err := repo.FindRefreshCandidates(ctx, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ids, id) {
		t.Fatal("the person with a corrected id is not a candidate")
	}
}

// An outcome repairs a missing attempt time, so a person whose attempt mark
// failed isn't taken for one never looked up (#1606).
func TestRecordRefreshOutcomeStampsMissingAttemptPostgres(t *testing.T) {
	repo, pool := personRefreshTestRepo(t)
	ctx := context.Background()
	id := seedRefreshPerson(t, pool, "unmarked")

	if err := repo.RecordRefreshOutcome(ctx, id, identityOf(t, pool, id), PersonRefreshAnswered); err != nil {
		t.Fatal(err)
	}
	var stamped bool
	if err := pool.QueryRow(ctx, `SELECT metadata_refresh_attempted_at IS NOT NULL FROM people WHERE id = $1`, id).Scan(&stamped); err != nil {
		t.Fatal(err)
	}
	if !stamped {
		t.Fatal("the outcome left the attempt time empty")
	}
	ids, err := repo.FindRefreshCandidates(ctx, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(ids, id) {
		t.Fatal("an answered person is a never-looked-up candidate again")
	}
}

// A refresh's result doesn't overwrite an id correction made during the
// lookup: the write checks the ids the lookup asked about under its row lock.
func TestUpdateRefreshedKeepsAnIDCorrectionPostgres(t *testing.T) {
	repo, pool := personRefreshTestRepo(t)
	ctx := context.Background()
	id := seedRefreshPerson(t, pool, "refresh-race")
	lookedUp := identityOf(t, pool, id)
	person, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	corrected := *person
	corrected.TmdbID += "-corrected"
	if err := repo.Update(ctx, corrected); err != nil {
		t.Fatal(err)
	}
	refreshed := *person
	refreshed.Bio = "About the old id"
	if _, err := repo.UpdateRefreshed(ctx, refreshed, lookedUp); !errors.Is(err, ErrPersonIdentityChanged) {
		t.Fatalf("err = %v, want ErrPersonIdentityChanged", err)
	}
	if got := identityOf(t, pool, id); got.TmdbID != corrected.TmdbID {
		t.Fatalf("tmdb id = %q, want the correction %q", got.TmdbID, corrected.TmdbID)
	}

	// With the ids unchanged the write goes through.
	refreshed.TmdbID = corrected.TmdbID
	stored, err := repo.UpdateRefreshed(ctx, refreshed, identityOf(t, pool, id))
	if err != nil {
		t.Fatal(err)
	}
	if stored != PersonIdentityOf(refreshed) {
		t.Fatalf("stored identity %v, want %v", stored, PersonIdentityOf(refreshed))
	}
}

// When a refresh adds an id another person holds and the two can't be merged,
// the write keeps the person's own value, and reports the identity it stored
// rather than the refreshed one.
func TestUpdateRefreshedReportsTheStoredIdentityPostgres(t *testing.T) {
	repo, pool := personRefreshTestRepo(t)
	ctx := context.Background()
	id := seedRefreshPerson(t, pool, "keeps-id")
	other := seedRefreshPerson(t, pool, "holds-imdb")
	imdb := fmt.Sprintf("nm-held-%d", other)
	if _, err := pool.Exec(ctx, `UPDATE people SET imdb_id = $2 WHERE id = $1`, other, imdb); err != nil {
		t.Fatal(err)
	}
	lookedUp := identityOf(t, pool, id)
	person, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	refreshed := *person
	refreshed.ImdbID = imdb
	stored, err := repo.UpdateRefreshed(ctx, refreshed, lookedUp)
	if err != nil {
		t.Fatal(err)
	}
	if stored != lookedUp || identityOf(t, pool, id) != lookedUp {
		t.Fatalf("stored identity %v (row %v), want %v", stored, identityOf(t, pool, id), lookedUp)
	}
	// Recording against the stored identity lands.
	if err := repo.RecordRefreshOutcome(ctx, id, stored, PersonRefreshAnswered); err != nil {
		t.Fatal(err)
	}
}

// Two refreshes that each add the other person's id resolve their conflicts
// at once. The guard takes no lock ahead of the resolver's ordered ones, so
// neither deadlocks.
func TestUpdateRefreshedConcurrentConflictsDontDeadlockPostgres(t *testing.T) {
	repo, pool := personRefreshTestRepo(t)
	ctx := context.Background()
	for round := range 10 {
		a := seedRefreshPerson(t, pool, fmt.Sprintf("swap-a-%d", round))
		b := seedRefreshPerson(t, pool, fmt.Sprintf("swap-b-%d", round))
		for _, id := range []int64{a, b} {
			if _, err := pool.Exec(ctx, `UPDATE people SET imdb_id = $2 WHERE id = $1`, id, fmt.Sprintf("nm-swap-%d", id)); err != nil {
				t.Fatal(err)
			}
		}
		refresh := func(id, otherID int64) (models.Person, PersonIdentity) {
			person, err := repo.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			lookedUp := identityOf(t, pool, id)
			person.ImdbID = identityOf(t, pool, otherID).ImdbID
			return *person, lookedUp
		}
		pa, ga := refresh(a, b)
		pb, gb := refresh(b, a)

		start := make(chan struct{})
		errs := make(chan error, 2)
		for _, w := range []struct {
			p models.Person
			g PersonIdentity
		}{{pa, ga}, {pb, gb}} {
			go func() {
				<-start
				_, err := repo.UpdateRefreshed(ctx, w.p, w.g)
				errs <- err
			}()
		}
		close(start)
		for range 2 {
			if err := <-errs; err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
	}
}
