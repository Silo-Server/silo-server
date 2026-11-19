package requests

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"runtime"
	"slices"
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
	insertLifecycleRequest(t, repo, "mine-other-title", 1, 505, StatusApproved)
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET outcome = 'failed' WHERE id = 'mine-other-title'`); err != nil {
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
	if _, err := repo.GetRequest(ctx, "mine-other-title"); err != nil {
		t.Fatalf("other title's failed row: %v, want kept", err)
	}
}

func TestDeleteFailedRequestDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "failed", 1, 101, StatusApproved)
	insertLifecycleRequest(t, repo, "partly-delivered", 1, 102, StatusApproved)
	insertLifecycleRequest(t, repo, "active", 1, 103, StatusApproved)
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET outcome = 'failed' WHERE id <> 'active'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_request_targets (request_id, quality, status)
		VALUES ('partly-delivered', '1080p', 'completed'), ('partly-delivered', '2160p', 'failed')`); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteFailed(ctx, "failed"); err != nil {
		t.Fatalf("delete failed request: %v", err)
	}
	if _, err := repo.GetRequest(ctx, "failed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed request after delete: err = %v, want ErrNotFound", err)
	}
	for _, id := range []string{"partly-delivered", "active"} {
		if err := repo.DeleteFailed(ctx, id); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("delete %s: err = %v, want ErrInvalidState", id, err)
		}
		if _, err := repo.GetRequest(ctx, id); err != nil {
			t.Fatalf("%s after refused delete: %v, want kept", id, err)
		}
	}
	if err := repo.DeleteFailed(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing: err = %v, want ErrNotFound", err)
	}
}

// The library completes an approved request only when it delivered nothing:
// one that delivered a quality is a retry resending the rest.
func TestMarkAvailableLeavesARetryResendingATierDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "retrying", 1, 101, StatusApproved)
	insertLifecycleRequest(t, repo, "fresh", 1, 102, StatusApproved)
	if _, err := pool.Exec(ctx, `INSERT INTO media_request_targets (request_id, quality, status)
		VALUES ('retrying', '1080p', 'completed')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MarkAvailable(ctx, "retrying", Viewer{}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("retrying: err = %v, want ErrInvalidState", err)
	}
	if req, err := repo.MarkAvailable(ctx, "fresh", Viewer{}); err != nil || req.Status != StatusCompleted {
		t.Fatalf("fresh: request = %+v, err = %v, want completed", req, err)
	}
}

func TestApproveTwiceSubmitsOnce(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["r1"] = &Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusPending, Outcome: OutcomeActive}
	svc, router := newRoutedService(store)
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

// A Retry whose resend errors keeps the failed tier's target through the
// backoff, so the failure stays visible and the next attempt still resends
// that tier; a resend that reaches the router replaces it.
func TestRetryKeepsFailedTargetUntilResendRecorded(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["r1"] = &Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusQueued, Outcome: OutcomeFailed}
	store.targets = map[string][]Target{"r1": {
		{ID: 1, RequestID: "r1", IntegrationID: "router-1", Quality: Quality1080p, Status: StatusCompleted},
		{ID: 2, RequestID: "r1", Quality: Quality2160p, Status: StatusFailed, LastError: "radarr 4k unreachable"},
	}}
	store.targetSeq = 2
	svc, router := newRoutedService(store)
	router.fulfillErr = errors.New("router unreachable")

	req, err := svc.Retry(ctx, Viewer{UserID: 1, IsAdmin: true}, "r1")
	if err != nil {
		t.Fatalf("Retry returned error: %v", err)
	}
	if req.Status != StatusApproved || req.Outcome != OutcomeActive {
		t.Fatalf("request = %s/%s, want approved/active in backoff", req.Status, req.Outcome)
	}
	targets, _ := store.ListTargets(ctx, "r1")
	if len(targets) != 2 {
		t.Fatalf("targets = %+v, want the failed 4K target kept while the resend backs off", targets)
	}

	store.requests["r1"].NextSubmitAt = nil
	router.fulfillErr = nil
	router.gotQualities = nil
	req, err = svc.submitApprovedRequest(ctx, *store.requests["r1"], Viewer{}, nil)
	if err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	if !slices.Equal(router.gotQualities, []Quality{Quality2160p}) {
		t.Fatalf("resent qualities = %v, want 2160p", router.gotQualities)
	}
	if req.Status != StatusQueued || req.Outcome != OutcomeActive {
		t.Fatalf("request = %s/%s, want queued/active", req.Status, req.Outcome)
	}
	targets, _ = store.ListTargets(ctx, "r1")
	if len(targets) != 2 || !slices.ContainsFunc(targets, func(t Target) bool { return t.Quality == Quality2160p && t.Status == StatusQueued }) {
		t.Fatalf("targets = %+v, want the completed 1080p and the queued 2160p resend", targets)
	}
}

// pollingRouter runs during before each Fulfill call, to interleave another
// API server's work with a submission's router call.
type pollingRouter struct {
	*fakeRouterProvider
	during func()
}

func (p *pollingRouter) Fulfill(ctx context.Context, installationID int, capabilityID string, req Request, qualities []Quality, conns []ResolvedRouterConnection) ([]RouterTarget, string, error) {
	p.during()
	return p.fakeRouterProvider.Fulfill(ctx, installationID, capabilityID, req, qualities, conns)
}

// A status poll that read a target as queued before it failed, and lands
// while a Retry is resending that failed tier, leaves the target and the
// request alone, so the resend is recorded.
func TestStalePollDoesNotFailRetryInFlight(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["r1"] = &Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusQueued, Outcome: OutcomeFailed}
	store.targets = map[string][]Target{"r1": {
		{ID: 1, RequestID: "r1", IntegrationID: "router-1", Quality: Quality1080p, Status: StatusCompleted},
		{ID: 2, RequestID: "r1", IntegrationID: "router-1", Quality: Quality2160p, Status: StatusFailed, ExternalID: "4k"},
	}}
	store.targetSeq = 2
	svc := newTestService(store)
	stale := []Target{store.targets["r1"][0], store.targets["r1"][1]}
	stale[1].Status = StatusQueued
	svc.SetRouterProvider(&pollingRouter{fakeRouterProvider: &fakeRouterProvider{}, during: func() {
		req := *store.requests["r1"]
		if _, err := svc.applyTargetStatuses(ctx, newCompletionCheck(req, nil), stale,
			[]RouterTargetStatus{{Quality: Quality2160p, ConnectionID: "router-1", Status: StatusFailed}}); err != nil {
			t.Errorf("stale poll: %v", err)
		}
	}})

	req, err := svc.Retry(ctx, Viewer{UserID: 1, IsAdmin: true}, "r1")
	if err != nil {
		t.Fatalf("Retry returned error: %v", err)
	}
	if req.Status != StatusQueued || req.Outcome != OutcomeActive {
		t.Fatalf("request = %s/%s, want queued/active with the resend recorded", req.Status, req.Outcome)
	}
}

// RecordSubmission replaces a resent quality's failed target in the same
// transaction that records the resend, and a write it refuses or rolls back
// leaves the failure in place.
func TestRecordSubmissionReplacesFailedTargetDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "r1", 1, 101, StatusApproved)
	if _, err := pool.Exec(ctx, `INSERT INTO media_request_targets (request_id, quality, status, last_error)
		VALUES ('r1', '1080p', 'completed', ''), ('r1', '2160p', 'failed', 'radarr 4k unreachable')`); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := repo.ClaimSubmission(ctx, "r1", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	lease := *claimed.SubmitLeaseUntil
	statuses := func() map[Quality]Status {
		t.Helper()
		targets, err := repo.ListTargets(ctx, "r1")
		if err != nil {
			t.Fatal(err)
		}
		out := map[Quality]Status{}
		for _, target := range targets {
			out[target.Quality] = target.Status
		}
		return out
	}
	kept := map[Quality]Status{Quality1080p: StatusCompleted, Quality2160p: StatusFailed}
	resend := []Target{{Quality: Quality2160p, Status: StatusQueued, ExternalID: "4k"}}

	if _, err := repo.RecordSubmission(ctx, "r1", lease.Add(-time.Second), resend, Viewer{}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("stale lease: err = %v, want ErrInvalidState", err)
	}
	if got := statuses(); !maps.Equal(got, kept) {
		t.Fatalf("after stale record: targets = %v, want %v", got, kept)
	}
	if _, err := repo.RecordSubmission(ctx, "r1", lease, []Target{{Quality: Quality2160p, Status: "bogus"}}, Viewer{}); err == nil {
		t.Fatal("invalid target recorded, want an error")
	}
	if got := statuses(); !maps.Equal(got, kept) {
		t.Fatalf("after rolled-back record: targets = %v, want %v", got, kept)
	}

	req, err := repo.RecordSubmission(ctx, "r1", lease, resend, Viewer{})
	if err != nil {
		t.Fatalf("record resend: %v", err)
	}
	if want := (map[Quality]Status{Quality1080p: StatusCompleted, Quality2160p: StatusQueued}); !maps.Equal(statuses(), want) {
		t.Fatalf("after resend: targets = %v, want %v", statuses(), want)
	}
	if req.Status != StatusQueued || req.Outcome != OutcomeActive {
		t.Fatalf("request = %s/%s, want queued/active", req.Status, req.Outcome)
	}
}

// A status report for a target another pass already finished leaves the
// target and its request alone.
func TestUpdateTargetStatusLeavesFinishedTargetDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "r1", 1, 101, StatusApproved)
	var failedID int64
	if err := pool.QueryRow(ctx, `INSERT INTO media_request_targets (request_id, quality, status, last_error)
		VALUES ('r1', '2160p', 'failed', 'radarr 4k unreachable') RETURNING id`).Scan(&failedID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateTargetStatus(ctx, failedID, StatusFailed, "", "", "late report", Viewer{}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("update a failed target: err = %v, want ErrInvalidState", err)
	}
	req, err := repo.GetRequest(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != StatusApproved || req.Outcome != OutcomeActive {
		t.Fatalf("request = %s/%s, want approved/active left alone", req.Status, req.Outcome)
	}
	targets, err := repo.ListTargets(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].LastError != "radarr 4k unreachable" {
		t.Fatalf("targets = %+v, want the failed target unchanged", targets)
	}
	if _, err := repo.UpdateTargetStatus(ctx, failedID+1000, StatusFailed, "", "", "", Viewer{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update a missing target: err = %v, want ErrNotFound", err)
	}
}

// A retry backing off holds only failed targets until it resends, so it can
// still be withdrawn; a target that went anywhere keeps it in the pipeline.
func TestWithdrawIgnoresFailedTargetsDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "only-failed", 1, 101, StatusApproved)
	insertLifecycleRequest(t, repo, "delivered", 1, 102, StatusApproved)
	if _, err := pool.Exec(ctx, `INSERT INTO media_request_targets (request_id, quality, status)
		VALUES ('only-failed', '1080p', 'failed'), ('delivered', '1080p', 'completed'), ('delivered', '2160p', 'failed')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetOutcome(ctx, "only-failed", guardWithdrawable, OutcomeCancelled, Viewer{UserID: 1}, ""); err != nil {
		t.Fatalf("cancel a retry holding only failed targets: %v", err)
	}
	if _, err := repo.SetOutcome(ctx, "delivered", guardWithdrawable, OutcomeCancelled, Viewer{UserID: 1}, ""); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("cancel a request that delivered a quality: err = %v, want ErrInvalidState", err)
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
	svc, router := newRoutedService(store)
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

// retryingMovie stores movie request r1, failed after delivering 1080p but not
// 2160p, with its title in the library from the 1080p copy.
func retryingMovie(store *fakeStore, svc *Service) {
	store.requests["r1"] = &Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusQueued, Outcome: OutcomeFailed}
	store.candidates = []*Request{store.requests["r1"]}
	store.targets = map[string][]Target{"r1": {
		{ID: 1, RequestID: "r1", IntegrationID: "router-1", Quality: Quality1080p, Status: StatusCompleted},
		{ID: 2, RequestID: "r1", Quality: Quality2160p, Status: StatusFailed, LastError: msgNoTargetForQuality},
	}}
	store.targetSeq = 2
	svc.SetEntitlementResolver(fixedCeiling{q: "2160p"})
	svc.presence = &fakePresence{available: map[MediaType]map[int]bool{MediaTypeMovie: {550: true}}}
}

// A Retry resending a failed tier is not completed by the library, which only
// holds the tier that arrived: not while the resend waits out its backoff, and
// not ahead of the resend once the backoff is over.
func TestRetryOfAFailedTierIsNotCompletedByTheLibrary(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	svc, router := newRoutedService(store)
	retryingMovie(store, svc)
	router.fulfillErr = errors.New("radarr unreachable")
	if _, err := svc.Retry(ctx, Viewer{UserID: 1, IsAdmin: true}, "r1"); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if _, err := svc.ReconcileRequests(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if got := store.requests["r1"]; got.Status != StatusApproved || got.Outcome != OutcomeActive {
		t.Fatalf("in backoff: request = %s/%s, want approved/active, waiting to resend the 4K tier", got.Status, got.Outcome)
	}

	router.fulfillErr = nil
	due := svc.now().Add(-time.Second) // past on the service's and the store's clocks
	store.requests["r1"].NextSubmitAt = &due
	if _, err := svc.ReconcileRequests(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if got := store.requests["r1"]; got.Status != StatusQueued {
		t.Fatalf("after backoff: request = %s/%s, want queued with the 4K tier resent", got.Status, got.Outcome)
	}
	if !slices.ContainsFunc(store.targets["r1"], func(tg Target) bool { return tg.Quality == Quality2160p && tg.Status == StatusQueued }) {
		t.Fatalf("targets = %+v, want a queued 2160p target", store.targets["r1"])
	}
}

// deleteFailingStore fails every target delete.
type deleteFailingStore struct{ *fakeStore }

func (deleteFailingStore) DeleteTarget(context.Context, string, int64, time.Time) error {
	return errors.New("database unavailable")
}

// A submission attempt that outlived its claim neither drops a failed target
// nor settles the request while a newer claim is submitting it (#4184).
func TestStaleSubmissionLeavesTheNewerClaimsRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		targets []Target
	}{
		{name: "failed target for an unwanted quality", targets: []Target{
			{ID: 1, RequestID: "r1", Quality: Quality1080p, Status: StatusCompleted},
			{ID: 2, RequestID: "r1", Quality: Quality2160p, Status: StatusFailed, LastError: msgNoTargetForQuality},
		}},
		{name: "nothing left to send", targets: []Target{
			{ID: 1, RequestID: "r1", Quality: Quality1080p, Status: StatusCompleted},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			newer := time.Now().Add(time.Hour)
			store.requests["r1"] = &Request{ID: "r1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusApproved, Outcome: OutcomeActive, SubmitLeaseUntil: &newer}
			store.targets = map[string][]Target{"r1": slices.Clone(tc.targets)}
			store.targetSeq = 2
			svc := newTestService(store)

			stale := newer.Add(-time.Minute)
			_, current, err := svc.planSubmission(context.Background(), Request{ID: "r1", SubmitLeaseUntil: &stale}, Viewer{}, []Quality{Quality1080p}, true)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if current == nil || current.Status != StatusApproved {
				t.Fatalf("plan returned %+v, want the request as the newer claim holds it", current)
			}
			if got := store.requests["r1"]; got.Status != StatusApproved || got.Outcome != OutcomeActive || !got.SubmitLeaseUntil.Equal(newer) {
				t.Fatalf("request = %s/%s lease %v, want approved/active with the newer lease", got.Status, got.Outcome, got.SubmitLeaseUntil)
			}
			if got := store.targets["r1"]; len(got) != len(tc.targets) {
				t.Fatalf("targets = %+v, want all %d kept", got, len(tc.targets))
			}
		})
	}
}

// DeleteTarget and RecomputeStatus write only while the caller's claim holds
// the request, as RecordSubmission does (#4184).
func TestClaimedTargetWritesNeedTheClaimDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "r1", 1, 101, StatusApproved)
	if _, err := pool.Exec(ctx, `INSERT INTO media_request_targets (request_id, quality, status, last_error)
		VALUES ('r1', '1080p', 'completed', ''), ('r1', '2160p', 'failed', 'radarr 4k unreachable')`); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := repo.ClaimSubmission(ctx, "r1", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	lease, stale := *claimed.SubmitLeaseUntil, claimed.SubmitLeaseUntil.Add(-time.Second)
	targets, err := repo.ListTargets(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	var failedID int64
	for _, target := range targets {
		if target.Status == StatusFailed {
			failedID = target.ID
		}
	}

	if err := repo.DeleteTarget(ctx, "r1", failedID, stale); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("delete with a stale lease: err = %v, want ErrInvalidState", err)
	}
	if _, err := repo.RecomputeStatus(ctx, "r1", stale, Viewer{}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("recompute with a stale lease: err = %v, want ErrInvalidState", err)
	}
	if got, err := repo.ListTargets(ctx, "r1"); err != nil || len(got) != 2 {
		t.Fatalf("after stale writes: targets = %+v err = %v, want both kept", got, err)
	}
	if got, err := repo.GetRequest(ctx, "r1"); err != nil || got.Status != StatusApproved {
		t.Fatalf("after stale writes: request = %+v err = %v, want approved", got, err)
	}

	if err := repo.DeleteTarget(ctx, "r1", failedID, lease); err != nil {
		t.Fatalf("delete with the lease: %v", err)
	}
	if err := repo.DeleteTarget(ctx, "r1", failedID, lease); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete again: err = %v, want ErrNotFound", err)
	}
	req, err := repo.RecomputeStatus(ctx, "r1", lease, Viewer{})
	if err != nil {
		t.Fatalf("recompute with the lease: %v", err)
	}
	if req.Status != StatusCompleted || req.Outcome != OutcomeActive {
		t.Fatalf("request = %s/%s, want completed/active", req.Status, req.Outcome)
	}
}

// A reclaim that commits while DeleteTarget runs stops the stale attempt's
// delete: the request row is locked before its lease is checked, so the
// delete waits for the reclaim and then sees the newer lease.
func TestDeleteTargetWaitsForAConcurrentReclaimDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "r1", 1, 101, StatusApproved)
	if _, err := pool.Exec(ctx, `INSERT INTO media_request_targets (request_id, quality, status, last_error)
		VALUES ('r1', '2160p', 'failed', 'radarr 4k unreachable')`); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := repo.ClaimSubmission(ctx, "r1", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	targets, err := repo.ListTargets(ctx, "r1")
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets = %+v err = %v", targets, err)
	}

	reclaim, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reclaim.Rollback(ctx) }()
	if _, err := reclaim.Exec(ctx, `UPDATE media_requests SET submit_lease_until = now() + interval '1 hour' WHERE id = 'r1'`); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- repo.DeleteTarget(ctx, "r1", targets[0].ID, *claimed.SubmitLeaseUntil) }()
	// Wait until the delete is blocked on the reclaim's row lock, which
	// Postgres reports as an ungranted lock; a delete that reads past the
	// open reclaim finishes first instead.
	for blocked := false; !blocked; {
		select {
		case err := <-deleted:
			t.Fatalf("delete finished while the reclaim was open (err = %v); want it to wait for the request lock", err)
		default:
		}
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE NOT granted)`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	if err := reclaim.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; !errors.Is(err, ErrInvalidState) {
		t.Fatalf("delete after the reclaim: err = %v, want ErrInvalidState", err)
	}
	if got, err := repo.ListTargets(ctx, "r1"); err != nil || len(got) != 1 {
		t.Fatalf("targets after the stale delete = %+v err = %v, want the target kept", got, err)
	}
}
