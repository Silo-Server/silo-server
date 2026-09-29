package requests

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// lifecycleTestRepository gives each test its own schema holding copies of the
// migrated request tables, so the guarded writes run against the real columns
// and constraints without touching shared rows.
func lifecycleTestRepository(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("request_lifecycle_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(t.Context(), `CREATE SCHEMA `+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+quoted+` CASCADE`) })
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"media_requests", "media_request_events", "media_request_targets", "request_integrations", "request_routing", "media_request_follows"} {
		if _, err = pool.Exec(t.Context(), `CREATE TABLE `+table+` (LIKE public.`+table+` INCLUDING ALL)`); err != nil {
			t.Fatalf("copy %s: %v", table, err)
		}
	}
	return NewRepository(pool, nil), pool
}

func insertLifecycleRequest(t *testing.T, repo *Repository, id string, userID, tmdbID int, status Status) *Request {
	t.Helper()
	req, err := repo.CreateRequest(t.Context(), CreateRequestRecord{
		ID:        id,
		Input:     CreateRequestInput{MediaType: MediaTypeMovie, TMDBID: tmdbID, Title: "Title " + id},
		Status:    status,
		Outcome:   OutcomeActive,
		Requester: Viewer{UserID: userID, ProfileID: "profile"},
	})
	if err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	return req
}

// raceLifecycle runs write from many goroutines at once and returns how many
// succeeded. Every loser must fail with wantLoss.
func raceLifecycle(t *testing.T, write func() error, wantLoss error) int {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	var won atomic.Int32
	for range 8 {
		wg.Go(func() {
			<-start
			err := write()
			switch {
			case err == nil:
				won.Add(1)
			case wantLoss == nil || !errors.Is(err, wantLoss):
				t.Errorf("write: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()
	return int(won.Load())
}

func TestGuardedTransitionsDatabase(t *testing.T) {
	repo, _ := lifecycleTestRepository(t)
	ctx := t.Context()
	admin := Viewer{}
	insertLifecycleRequest(t, repo, "req-approve", 1, 101, StatusPending)

	won := raceLifecycle(t, func() error {
		_, err := repo.SetStatus(ctx, "req-approve", guardPending, StatusApproved, admin)
		return err
	}, ErrInvalidState)
	if won != 1 {
		t.Fatalf("concurrent approvals applied %d times, want exactly 1", won)
	}
	if _, err := repo.SetOutcome(ctx, "req-approve", guardPending, OutcomeDeclined, admin, "late decline"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("decline after approval: err = %v, want ErrInvalidState", err)
	}
	if _, err := repo.SetStatus(ctx, "missing", guardPending, StatusApproved, admin); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approve missing request: err = %v, want ErrNotFound", err)
	}
	got, err := repo.GetRequest(ctx, "req-approve")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusApproved || got.Outcome != OutcomeActive {
		t.Fatalf("request = %s/%s, want approved/active", got.Status, got.Outcome)
	}
}

func TestWithdrawGuardDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "waiting", 1, 111, StatusApproved)
	insertLifecycleRequest(t, repo, "leased", 1, 112, StatusApproved)
	insertLifecycleRequest(t, repo, "targeted", 1, 113, StatusApproved)
	if _, claimed, err := repo.ClaimSubmission(ctx, "leased", time.Minute); err != nil || !claimed {
		t.Fatalf("claim: claimed = %v, err = %v", claimed, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_request_targets (request_id, quality, status, updated_at) VALUES ('targeted', '1080p', 'queued', now())`); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.SetOutcome(ctx, "waiting", guardWithdrawable, OutcomeDeclined, Viewer{}, ""); err != nil {
		t.Fatalf("decline an approved request nothing was sent for: %v", err)
	}
	for _, id := range []string{"leased", "targeted"} {
		if _, err := repo.SetOutcome(ctx, id, guardWithdrawable, OutcomeDeclined, Viewer{}, ""); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("decline %s: err = %v, want ErrInvalidState", id, err)
		}
	}
}

func TestSubmissionClaimDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "req-claim", 1, 202, StatusApproved)

	var lease atomic.Pointer[time.Time]
	won := raceLifecycle(t, func() error {
		req, claimed, err := repo.ClaimSubmission(ctx, "req-claim", time.Minute)
		if err != nil {
			return err
		}
		if !claimed {
			return ErrInvalidState
		}
		lease.Store(req.SubmitLeaseUntil)
		return nil
	}, ErrInvalidState)
	if won != 1 {
		t.Fatalf("concurrent claims succeeded %d times, want exactly 1", won)
	}

	// A worker whose lease ran out while another server claimed the request
	// again must not release or reschedule the newer claim.
	stale := *lease.Load()
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET submit_lease_until = now() - interval '1 second' WHERE id = 'req-claim'`); err != nil {
		t.Fatal(err)
	}
	reclaimed, claimed, err := repo.ClaimSubmission(ctx, "req-claim", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim after the lease expired: claimed = %v, err = %v; want claimed", claimed, err)
	}
	if _, err := repo.DeferSubmission(ctx, "req-claim", stale, time.Hour, "stale"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("defer with an expired lease: err = %v, want ErrInvalidState", err)
	}
	if current, err := repo.GetRequest(ctx, "req-claim"); err != nil || current.SubmitLeaseUntil == nil || current.NextSubmitAt != nil {
		t.Fatalf("after a stale defer: request = %+v, err = %v; want the newer claim untouched", current, err)
	}
	// Nor may it fail the newer attempt after running out of attempts.
	if _, err := repo.FailSubmission(ctx, "req-claim", stale, Viewer{}, "stale"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("fail with an expired lease: err = %v, want ErrInvalidState", err)
	}
	if current, err := repo.GetRequest(ctx, "req-claim"); err != nil || current.Outcome != OutcomeActive || current.SubmitLeaseUntil == nil || current.LastError != "" {
		t.Fatalf("after a stale fail: request = %+v, err = %v; want the newer claim untouched", current, err)
	}
	lease.Store(reclaimed.SubmitLeaseUntil)

	deferred, err := repo.DeferSubmission(ctx, "req-claim", *lease.Load(), time.Hour, "radarr unreachable")
	if err != nil {
		t.Fatal(err)
	}
	if deferred.LastError != "radarr unreachable" || deferred.SubmitAttempts != 2 || deferred.NextSubmitAt == nil || deferred.SubmitLeaseUntil != nil {
		t.Fatalf("deferred = %+v, want last error, two attempts, a next attempt time, and the claim released", deferred)
	}
	if _, claimed, err := repo.ClaimSubmission(ctx, "req-claim", time.Minute); err != nil || claimed {
		t.Fatalf("claim during backoff: claimed = %v, err = %v; want refused", claimed, err)
	}

	// Once the request leaves approved, nobody can claim or defer it.
	if _, err := repo.SetOutcome(ctx, "req-claim", StateGuard{Statuses: []Status{StatusApproved}}, OutcomeFailed, Viewer{}, "gave up"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DeferSubmission(ctx, "req-claim", *lease.Load(), time.Minute, "late"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("defer on failed request: err = %v, want ErrInvalidState", err)
	}

	reopened, err := repo.ReopenFailed(ctx, "req-claim", Viewer{})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status != StatusApproved || reopened.Outcome != OutcomeActive || reopened.LastError != "" ||
		reopened.SubmitAttempts != 0 || reopened.SubmitLeaseUntil != nil || reopened.NextSubmitAt != nil {
		t.Fatalf("reopened = %+v, want approved/active with a fresh submission budget", reopened)
	}
	if _, err := repo.ReopenFailed(ctx, "req-claim", Viewer{}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second reopen: err = %v, want ErrInvalidState", err)
	}
	final, claimed, err := repo.ClaimSubmission(ctx, "req-claim", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim after reopen: claimed = %v, err = %v; want claimed", claimed, err)
	}
	// The current claim fails its request and releases the lease.
	failed, err := repo.FailSubmission(ctx, "req-claim", *final.SubmitLeaseUntil, Viewer{}, "radarr rejected it")
	if err != nil {
		t.Fatal(err)
	}
	if failed.Outcome != OutcomeFailed || failed.LastError != "radarr rejected it" || failed.SubmitLeaseUntil != nil {
		t.Fatalf("failed = %+v, want failed with the error and the claim released", failed)
	}
}

func TestRecordSubmissionIsFencedOnLeaseDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "req-record", 1, 212, StatusApproved)
	insertLifecycleRequest(t, repo, "req-withdrawn", 1, 213, StatusApproved)
	targets := []Target{
		{Quality: Quality1080p, Status: StatusQueued, ExternalID: "42", ExternalStatus: "added"},
		{Quality: Quality2160p, Status: StatusFailed, LastError: "no 4K server"},
	}
	targetCount := func(id string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM media_request_targets WHERE request_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	expire := func(id string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE media_requests SET submit_lease_until = now() - interval '1 second' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}

	// A router call that outlived its lease while the request was withdrawn
	// records nothing and leaves the request withdrawn.
	withdrawn, claimed, err := repo.ClaimSubmission(ctx, "req-withdrawn", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim: claimed = %v, err = %v", claimed, err)
	}
	expire("req-withdrawn")
	if _, err := repo.SetOutcome(ctx, "req-withdrawn", guardWithdrawable, OutcomeCancelled, Viewer{}, "changed my mind"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RecordSubmission(ctx, "req-withdrawn", *withdrawn.SubmitLeaseUntil, targets, Viewer{}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("record on a withdrawn request: err = %v, want ErrInvalidState", err)
	}
	if current, err := repo.GetRequest(ctx, "req-withdrawn"); err != nil || current.Outcome != OutcomeCancelled || targetCount("req-withdrawn") != 0 {
		t.Fatalf("after a stale record: request = %+v, err = %v; want it withdrawn with no targets", current, err)
	}

	// Nor may it record over a newer claim.
	stale, claimed, err := repo.ClaimSubmission(ctx, "req-record", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim: claimed = %v, err = %v", claimed, err)
	}
	expire("req-record")
	current, claimed, err := repo.ClaimSubmission(ctx, "req-record", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim after the lease expired: claimed = %v, err = %v", claimed, err)
	}
	if _, err := repo.RecordSubmission(ctx, "req-record", *stale.SubmitLeaseUntil, targets, Viewer{}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("record with an expired lease: err = %v, want ErrInvalidState", err)
	}
	if targetCount("req-record") != 0 {
		t.Fatal("a stale record wrote targets")
	}

	// The current claim records its targets and releases the lease.
	recorded, err := repo.RecordSubmission(ctx, "req-record", *current.SubmitLeaseUntil, targets, Viewer{})
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Status != StatusQueued || recorded.Outcome != OutcomeActive || recorded.SubmitLeaseUntil != nil {
		t.Fatalf("recorded = %+v, want queued/active with the claim released", recorded)
	}
	stored, err := repo.ListTargets(ctx, "req-record")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0].ExternalID != "42" || stored[0].Status != StatusQueued ||
		stored[1].Status != StatusFailed || stored[1].LastError != "no 4K server" {
		t.Fatalf("targets = %+v, want the queued 1080p and failed 2160p targets", stored)
	}
}

func TestReplaceFailedIsScopedToRequesterDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "mine-failed", 1, 303, StatusApproved)
	insertLifecycleRequest(t, repo, "theirs-failed", 2, 404, StatusApproved)
	// Two accounts' failed requests for one title can coexist; the active-title
	// unique index only covers active rows.
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET outcome = 'failed', tmdb_id = 303`); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.CreateRequest(ctx, CreateRequestRecord{
		ID:            "mine-again",
		Input:         CreateRequestInput{MediaType: MediaTypeMovie, TMDBID: 303, Title: "Again"},
		Status:        StatusPending,
		Outcome:       OutcomeActive,
		Requester:     Viewer{UserID: 1},
		ReplaceFailed: true,
		Quota:         &QuotaCheck{UserID: 1, WindowStart: time.Now().Add(-time.Hour), MaxRequests: 1},
	}); err != nil {
		t.Fatalf("re-request: %v", err)
	}
	if _, err := repo.GetRequest(ctx, "mine-failed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("requester's failed row: err = %v, want deleted", err)
	}
	if _, err := repo.GetRequest(ctx, "theirs-failed"); err != nil {
		t.Fatalf("other account's failed row: %v, want kept", err)
	}
}

func TestReopenFailedWhileAnotherRequestIsActiveDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "first-failed", 1, 606, StatusApproved)
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET outcome = 'failed' WHERE id = 'first-failed'`); err != nil {
		t.Fatal(err)
	}
	insertLifecycleRequest(t, repo, "second-active", 2, 606, StatusPending)

	if _, err := repo.ReopenFailed(ctx, "first-failed", Viewer{}); !errors.Is(err, ErrAlreadyRequested) {
		t.Fatalf("reopen while another request is active: err = %v, want ErrAlreadyRequested", err)
	}
}

func TestMarkAvailableWaitsForSubmissionClaimDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "req-present", 1, 707, StatusApproved)
	if _, claimed, err := repo.ClaimSubmission(ctx, "req-present", time.Minute); err != nil || !claimed {
		t.Fatalf("claim: claimed = %v, err = %v", claimed, err)
	}
	if _, err := repo.MarkAvailable(ctx, "req-present", Viewer{}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("mark available during a live claim: err = %v, want ErrInvalidState", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET submit_lease_until = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	got, err := repo.MarkAvailable(ctx, "req-present", Viewer{})
	if err != nil {
		t.Fatalf("mark available after the lease: %v", err)
	}
	if got.Status != StatusCompleted || got.CompletedAt == nil {
		t.Fatalf("request = %s completed_at=%v, want completed with a timestamp", got.Status, got.CompletedAt)
	}
}

// A request submitted after the caller found none of its targets live keeps
// going: the title arriving does not complete it over a queued target.
func TestMarkAvailableRefusesLiveTargetsDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "req-queued", 1, 708, StatusPending)
	if _, err := pool.Exec(ctx, `
		UPDATE media_requests SET status = 'queued' WHERE id = 'req-queued';
		INSERT INTO media_request_targets (request_id, quality, status, updated_at) VALUES ('req-queued', '1080p', 'queued', now());`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MarkAvailable(ctx, "req-queued", Viewer{}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("mark available over a queued target: err = %v, want ErrInvalidState", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_request_targets SET status = 'completed' WHERE request_id = 'req-queued'`); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.MarkAvailable(ctx, "req-queued", Viewer{}); err != nil || got.Status != StatusCompleted {
		t.Fatalf("mark available once nothing is on its way: %+v, %v", got, err)
	}
}

// A request retried after a later request for the title took its follows and
// failed too gets them back.
func TestReopenFailedTakesBackFollowsDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	fail := func(id string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE media_requests SET outcome = 'failed' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	insertLifecycleRequest(t, repo, "first", 1, 709, StatusApproved)
	if err := repo.FollowTitle(ctx, MediaTypeMovie, 709, Viewer{UserID: 3, ProfileID: "profile-a"}); err != nil {
		t.Fatal(err)
	}
	fail("first")
	insertLifecycleRequest(t, repo, "second", 2, 709, StatusApproved)
	fail("second")

	if _, err := repo.ReopenFailed(ctx, "first", Viewer{}); err != nil {
		t.Fatal(err)
	}
	followers, err := repo.ListRequestFollowers(ctx, Request{ID: "first", MediaType: MediaTypeMovie, TMDBID: 709})
	if err != nil || len(followers) != 1 {
		t.Fatalf("followers of the retried request = %+v, err = %v; want the follow back", followers, err)
	}
}

func TestReconcileCandidatesRotateDatabase(t *testing.T) {
	repo, _ := lifecycleTestRepository(t)
	ctx := t.Context()
	for i, id := range []string{"rot-a", "rot-b", "rot-c"} {
		insertLifecycleRequest(t, repo, id, 1, 500+i, StatusApproved)
	}
	first, err := repo.ListReconciliationCandidates(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("first batch = %d, want 2", len(first))
	}
	for _, req := range first {
		if err := repo.MarkReconciled(ctx, req.ID); err != nil {
			t.Fatal(err)
		}
	}
	second, err := repo.ListReconciliationCandidates(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{first[0].ID: true, first[1].ID: true}
	if len(second) == 0 || seen[second[0].ID] {
		t.Fatalf("second batch starts with %v, want the request the first batch skipped", second)
	}
}

func TestApproveTwiceSubmitsOnce(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["r1"] = &Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusPending, Outcome: OutcomeActive}
	router := &fakeRouterProvider{}
	svc := newTestService(store)
	svc.SetRouterProvider(router)
	admin := Viewer{UserID: 1, IsAdmin: true}

	if _, err := svc.Approve(context.Background(), admin, "r1"); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	if _, err := svc.Approve(context.Background(), admin, "r1"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second approve: err = %v, want ErrInvalidState", err)
	}
	if router.fulfillCalls != 1 {
		t.Fatalf("fulfill calls = %d, want 1", router.fulfillCalls)
	}
}

func TestApproveKeepsApprovalWhenSubmissionFails(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["r1"] = &Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusPending, Outcome: OutcomeActive}
	router := &fakeRouterProvider{fulfillErr: ErrIntegrationUnreachable}
	svc := newTestService(store)
	svc.SetRouterProvider(router)

	req, err := svc.Approve(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "r1")
	if err != nil {
		t.Fatalf("Approve returned error: %v", err)
	}
	if req.Status != StatusApproved || req.Outcome != OutcomeActive {
		t.Fatalf("request = %s/%s, want approved/active", req.Status, req.Outcome)
	}
	if req.LastError == "" || req.NextSubmitAt == nil {
		t.Fatalf("request = %+v, want the failure recorded and a retry scheduled", req)
	}

	// A reconcile pass during the backoff does not call the router again.
	if _, err := svc.submitApprovedRequest(context.Background(), *req, Viewer{}, nil); err != nil {
		t.Fatalf("resubmit during backoff: %v", err)
	}
	if router.fulfillCalls != 1 {
		t.Fatalf("fulfill calls = %d, want 1 (backoff not elapsed)", router.fulfillCalls)
	}
}

func TestCreateRequestKeepsAutoApprovedRequestWhenSubmissionFails(t *testing.T) {
	store := newFakeStore()
	store.settings.GlobalAutoApprovalEnabled = true
	store.integrations = []Integration{autoApproveRouterInst("router-1", "radarr-key")}
	svc := newTestService(store)
	svc.SetRouterProvider(&fakeRouterProvider{fulfillErr: ErrIntegrationUnreachable})

	req, err := svc.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if req.Status != StatusApproved || req.Outcome != OutcomeActive || req.LastError == "" {
		t.Fatalf("request = %+v, want approved/active with the submission error recorded", req)
	}
}

func TestSubmitApprovedMarksFailedAfterLastAttempt(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["r1"] = &Request{
		ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusApproved, Outcome: OutcomeActive,
		SubmitAttempts: maxSubmitAttempts - 1,
	}
	svc := newTestService(store)
	svc.SetRouterProvider(&fakeRouterProvider{fulfillErr: errors.New("radarr: connection refused")})

	req, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if req.Outcome != OutcomeFailed || req.LastError != "radarr: connection refused" {
		t.Fatalf("request = %+v, want failed with the last error", req)
	}
}

// reclaimingRouter fails every submission and, while the call is in flight,
// lets the claim lapse and another server claim the request again.
type reclaimingRouter struct {
	*fakeRouterProvider
	store *fakeStore
}

func (r reclaimingRouter) Fulfill(ctx context.Context, installationID int, capabilityID string, req Request, qualities []Quality, conns []ResolvedRouterConnection) ([]RouterTarget, string, error) {
	r.store.mu.Lock()
	newer := time.Now().Add(time.Hour)
	r.store.requests[req.ID].SubmitLeaseUntil = &newer
	r.store.mu.Unlock()
	return r.fakeRouterProvider.Fulfill(ctx, installationID, capabilityID, req, qualities, conns)
}

func TestSubmitLastAttemptWithExpiredLeaseLeavesNewerClaim(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["r1"] = &Request{
		ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusApproved, Outcome: OutcomeActive,
		SubmitAttempts: maxSubmitAttempts - 1,
	}
	svc := newTestService(store)
	svc.SetRouterProvider(reclaimingRouter{&fakeRouterProvider{fulfillErr: errors.New("radarr: connection refused")}, store})

	req, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if req.Outcome != OutcomeActive || req.Status != StatusApproved || req.LastError != "" {
		t.Fatalf("request = %+v, want the newer claim's request left approved/active", req)
	}
}

// lapsingRouter succeeds, but while the call is in flight its claim lapses and
// meanwhile changes the request as another actor would.
type lapsingRouter struct {
	*fakeRouterProvider
	store     *fakeStore
	meanwhile func(req *Request)
}

func (r lapsingRouter) Fulfill(ctx context.Context, installationID int, capabilityID string, req Request, qualities []Quality, conns []ResolvedRouterConnection) ([]RouterTarget, string, error) {
	r.store.mu.Lock()
	r.meanwhile(r.store.requests[req.ID])
	r.store.mu.Unlock()
	return r.fakeRouterProvider.Fulfill(ctx, installationID, capabilityID, req, qualities, conns)
}

func TestSubmitSuccessWithExpiredLeaseIsDropped(t *testing.T) {
	for _, tc := range []struct {
		name      string
		meanwhile func(req *Request)
		want      func(req *Request) bool
	}{
		{
			name: "withdrawn",
			meanwhile: func(req *Request) {
				req.SubmitLeaseUntil = nil
				req.Outcome = OutcomeCancelled
			},
			want: func(req *Request) bool { return req.Outcome == OutcomeCancelled },
		},
		{
			name: "claimed again",
			meanwhile: func(req *Request) {
				newer := time.Now().Add(time.Hour)
				req.SubmitLeaseUntil = &newer
			},
			want: func(req *Request) bool {
				return req.Status == StatusApproved && req.Outcome == OutcomeActive && req.SubmitLeaseUntil != nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.integrations = []Integration{routerInst("router-1")}
			store.requests["r1"] = &Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusApproved, Outcome: OutcomeActive}
			svc := newTestService(store)
			svc.SetRouterProvider(lapsingRouter{&fakeRouterProvider{}, store, tc.meanwhile})

			req, err := svc.submitApprovedRequest(context.Background(), *store.requests["r1"], Viewer{}, nil)
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			if !tc.want(req) {
				t.Fatalf("request = %+v, want the state the other actor left", req)
			}
			if targets := store.targets["r1"]; len(targets) != 0 {
				t.Fatalf("targets = %+v, want none recorded by the stale attempt", targets)
			}
		})
	}
}

func TestCreateRequestReplacesOwnFailedRequestAtQuota(t *testing.T) {
	store := newFakeStore()
	store.settings.GlobalMaxRequests = 1
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	// The failed request fills the user's only slot; re-requesting the same
	// title replaces it, so it must not count against the re-request.
	store.requests["mine-failed"] = &Request{
		ID: "mine-failed", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusApproved,
		Outcome: OutcomeFailed, RequestedByUserID: 1, CreatedAt: now,
	}
	svc := newTestService(store)

	req, err := svc.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if req.Status != StatusPending {
		t.Fatalf("request = %+v, want a new pending request", req)
	}
	if _, ok := store.requests["mine-failed"]; ok {
		t.Fatal("the failed request was not replaced")
	}
}

func TestSubmitKeepsFailedTargetWhenEntitlementLookupFails(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["r1"] = &Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusQueued, Outcome: OutcomeFailed}
	store.targets = map[string][]Target{"r1": {
		{ID: 1, RequestID: "r1", IntegrationID: "router-1", Quality: Quality1080p, Status: StatusCompleted},
		{ID: 2, RequestID: "r1", Quality: Quality2160p, Status: StatusFailed, LastError: "radarr 4k unreachable"},
	}}
	store.targetSeq = 2
	svc := newTestService(store)
	svc.SetRouterProvider(&fakeRouterProvider{})
	svc.SetEntitlementResolver(failingCeiling{})

	if _, err := svc.Retry(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "r1"); err != nil {
		t.Fatalf("Retry returned error: %v", err)
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	if len(targets) != 2 {
		t.Fatalf("targets = %+v, want the failed 4K target kept after a failed entitlement lookup", targets)
	}
}

func TestSubmitBackoff(t *testing.T) {
	for attempts, want := range map[int]time.Duration{
		1:  5 * time.Minute,
		2:  10 * time.Minute,
		3:  20 * time.Minute,
		4:  40 * time.Minute,
		5:  time.Hour,
		10: time.Hour,
	} {
		if got := submitBackoff(attempts); got != want {
			t.Errorf("submitBackoff(%d) = %s, want %s", attempts, got, want)
		}
	}
}

// Issue #582: after the 4K tier is turned off, Retry must drop the failed 4K
// target instead of leaving the request failed next to a finished 1080p copy.
func TestRetryDropsFailedTargetForUnwantedQuality(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["r1"] = &Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusQueued, Outcome: OutcomeFailed}
	store.targets = map[string][]Target{"r1": {
		{ID: 1, RequestID: "r1", IntegrationID: "router-1", Quality: Quality1080p, Status: StatusCompleted},
		{ID: 2, RequestID: "r1", Quality: Quality2160p, Status: StatusFailed, LastError: msgNoTargetForQuality},
	}}
	store.targetSeq = 2
	router := &fakeRouterProvider{}
	svc := newTestService(store)
	svc.SetRouterProvider(router)
	svc.SetEntitlementResolver(fixedCeiling{q: "1080p"})

	req, err := svc.Retry(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "r1")
	if err != nil {
		t.Fatalf("Retry returned error: %v", err)
	}
	if router.fulfillCalls != 0 {
		t.Fatalf("fulfill calls = %d, want 0 (nothing left to send)", router.fulfillCalls)
	}
	if req.Status != StatusCompleted || req.Outcome != OutcomeActive {
		t.Fatalf("request = %s/%s, want completed/active", req.Status, req.Outcome)
	}
	targets, _ := store.ListTargets(context.Background(), "r1")
	if len(targets) != 1 || targets[0].Quality != Quality1080p {
		t.Fatalf("targets = %+v, want only the 1080p target", targets)
	}
}

func TestReconcileStampsEveryCandidate(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.candidates = []*Request{
		{ID: "ok", MediaType: MediaTypeMovie, TMDBID: 1, Status: StatusQueued, Outcome: OutcomeActive},
		{ID: "broken", MediaType: MediaTypeMovie, TMDBID: 2, Status: StatusQueued, Outcome: OutcomeActive},
	}
	store.targets = map[string][]Target{
		"ok":     {{ID: 1, RequestID: "ok", IntegrationID: "router-1", Quality: Quality1080p, Status: StatusQueued}},
		"broken": {{ID: 2, RequestID: "broken", IntegrationID: "router-1", Quality: Quality1080p, Status: StatusQueued}},
	}
	svc := newTestService(store)
	svc.SetRouterProvider(&fakeRouterProvider{statusErr: errors.New("sonarr timeout")})

	result, err := svc.ReconcileRequests(context.Background(), 10)
	if err != nil {
		t.Fatalf("ReconcileRequests: %v", err)
	}
	if result.Errors != 2 {
		t.Fatalf("errors = %d, want 2", result.Errors)
	}
	if len(store.reconciled) != 2 {
		t.Fatalf("stamped = %v, want both candidates stamped even though they errored", store.reconciled)
	}
}

func TestRequestState(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  Request
		want State
	}{
		{"pending", Request{Status: StatusPending, Outcome: OutcomeActive}, StatePending},
		{"approved", Request{Status: StatusApproved, Outcome: OutcomeActive}, StateApproved},
		{"queued", Request{Status: StatusQueued, Outcome: OutcomeActive}, StateProcessing},
		{"downloading", Request{Status: StatusDownloading, Outcome: OutcomeActive}, StateProcessing},
		{"downloaded, not scanned in yet", Request{Status: StatusCompleted, Outcome: OutcomeActive}, StateProcessing},
		{"in the library", Request{Status: StatusCompleted, Outcome: OutcomeActive, LibraryContentID: "movie-tmdb-1"}, StateAvailable},
		{"declined", Request{Status: StatusPending, Outcome: OutcomeDeclined}, StateDeclined},
		{"withdrawn", Request{Status: StatusPending, Outcome: OutcomeCancelled}, StateCancelled},
		// A failed request keeps whatever status it failed at.
		{"failed after queueing", Request{Status: StatusQueued, Outcome: OutcomeFailed}, StateFailed},
	} {
		if got := tc.req.State(); got != tc.want {
			t.Errorf("%s: State() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestOutcomeReasonDatabase(t *testing.T) {
	repo, _ := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "declined", 1, 901, StatusPending)
	insertLifecycleRequest(t, repo, "retried", 1, 902, StatusApproved)

	got, err := repo.SetOutcome(ctx, "declined", guardWithdrawable, OutcomeDeclined, Viewer{}, "  Not this month  ")
	if err != nil {
		t.Fatal(err)
	}
	if got.OutcomeReason != "Not this month" || got.LastError != "" {
		t.Fatalf("declined = %+v, want the trimmed reason in outcome_reason and no last_error", got)
	}
	reread, err := repo.GetRequest(ctx, "declined")
	if err != nil || reread.OutcomeReason != "Not this month" {
		t.Fatalf("reread = %+v, %v; want the reason stored", reread, err)
	}

	failed, err := repo.SetOutcome(ctx, "retried", StateGuard{Statuses: []Status{StatusApproved}}, OutcomeFailed, Viewer{}, "radarr down")
	if err != nil {
		t.Fatal(err)
	}
	if failed.OutcomeReason != "" || failed.LastError != "radarr down" {
		t.Fatalf("failed = %+v, want the error in last_error, not outcome_reason", failed)
	}
}
