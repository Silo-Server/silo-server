package requests

import (
	"context"
	"errors"
	"fmt"
	"os"
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

	req, err := svc.CreateRequest(context.Background(), testViewer(1), fightClubInput)
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
