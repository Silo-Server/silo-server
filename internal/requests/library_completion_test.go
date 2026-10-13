package requests

import (
	"context"
	"errors"
	"testing"
)

func presentMovie(tmdbID int) *fakePresence {
	return &fakePresence{available: map[MediaType]map[int]bool{MediaTypeMovie: {tmdbID: true}}}
}

// A pending request whose title reaches the library by any route is complete:
// there is nothing left for an admin to approve (AC4).
func TestReconcileCompletesPendingRequestFromLibrary(t *testing.T) {
	store := newFakeStore()
	store.waiting = []*Request{{ID: "req-1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusPending, Outcome: OutcomeActive}}
	router := &fakeRouterProvider{}
	svc := NewService(store, &fakeTMDBClient{}, presentMovie(550))
	svc.SetRouterProvider(router)

	result, err := svc.ReconcileRequests(context.Background(), 100)
	if err != nil {
		t.Fatalf("ReconcileRequests: %v", err)
	}
	got := store.requests["req-1"]
	if result.Completed != 1 || got.Status != StatusCompleted || got.Outcome != OutcomeActive {
		t.Fatalf("result = %+v request = %+v, want completed", result, got)
	}
	if router.fulfillCalls != 0 || router.statusCalls != 0 {
		t.Fatalf("router calls = fulfill %d / status %d, want none for a pending request", router.fulfillCalls, router.statusCalls)
	}
}

func TestReconcileLeavesPendingRequestWithoutMediaAlone(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.waiting = []*Request{{ID: "req-1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusPending, Outcome: OutcomeActive}}
	router := &fakeRouterProvider{}
	svc := NewService(store, &fakeTMDBClient{}, &fakePresence{})
	svc.SetRouterProvider(router)

	if _, err := svc.ReconcileRequests(context.Background(), 100); err != nil {
		t.Fatalf("ReconcileRequests: %v", err)
	}
	if got := store.requests["req-1"]; got != nil && got.Status != StatusPending {
		t.Fatalf("request = %+v, want still pending", got)
	}
	if router.fulfillCalls != 0 || router.statusCalls != 0 {
		t.Fatalf("router calls = fulfill %d / status %d, want none for a pending request", router.fulfillCalls, router.statusCalls)
	}
}

func TestReconcileCompletesFailedRequestFromLibrary(t *testing.T) {
	store := newFakeStore()
	store.waiting = []*Request{{
		ID: "req-1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusApproved, Outcome: OutcomeFailed,
		LastError: "radarr unreachable",
	}}
	svc := NewService(store, &fakeTMDBClient{}, presentMovie(550))

	if _, err := svc.ReconcileRequests(context.Background(), 100); err != nil {
		t.Fatalf("ReconcileRequests: %v", err)
	}
	got := store.requests["req-1"]
	if got.Status != StatusCompleted || got.Outcome != OutcomeActive || got.LastError != "" {
		t.Fatalf("request = %+v, want completed and active with the error cleared", got)
	}
}

// Without Sonarr/Radarr, an approved request waits for the library and the
// reconcile pass completes it once the title is scanned in (AC2, AC4).
func TestApprovedRequestWithoutRouterCompletesFromLibrary(t *testing.T) {
	store := newFakeStore()
	store.requests["req-1"] = &Request{ID: "req-1", MediaType: MediaTypeMovie, TMDBID: 550, Status: StatusPending, Outcome: OutcomeActive}
	presence := &fakePresence{}
	svc := NewService(store, &fakeTMDBClient{}, presence)
	svc.SetRouterProvider(&fakeRouterProvider{})

	approved, err := svc.Approve(context.Background(), Viewer{UserID: 1, IsAdmin: true}, "req-1")
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approved.Status != StatusApproved || approved.Outcome != OutcomeActive || approved.LastError != "" {
		t.Fatalf("approved = %+v, want approved and waiting", approved)
	}

	store.candidates = []*Request{approved}
	if _, err := svc.ReconcileRequests(context.Background(), 100); err != nil {
		t.Fatalf("ReconcileRequests before scan: %v", err)
	}
	if got := store.requests["req-1"]; got.Status != StatusApproved {
		t.Fatalf("before the scan: request = %+v, want still approved", got)
	}

	presence.available = map[MediaType]map[int]bool{MediaTypeMovie: {550: true}}
	if _, err := svc.ReconcileRequests(context.Background(), 100); err != nil {
		t.Fatalf("ReconcileRequests after scan: %v", err)
	}
	if got := store.requests["req-1"]; got.Status != StatusCompleted {
		t.Fatalf("after the scan: request = %+v, want completed", got)
	}
}

func TestLibraryCompletionDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "pending", 1, 801, StatusPending)
	insertLifecycleRequest(t, repo, "failed", 1, 802, StatusApproved)
	insertLifecycleRequest(t, repo, "declined", 1, 803, StatusPending)
	insertLifecycleRequest(t, repo, "done", 1, 804, StatusApproved)
	insertLifecycleRequest(t, repo, "partly-delivered", 1, 805, StatusApproved)
	insertLifecycleRequest(t, repo, "old-failure", 1, 806, StatusApproved)
	if _, err := pool.Exec(ctx, `
		UPDATE media_requests SET outcome = 'failed', last_error = 'boom' WHERE id IN ('failed', 'partly-delivered', 'old-failure');
		UPDATE media_requests SET updated_at = now() - interval '45 days' WHERE id = 'old-failure';
		UPDATE media_requests SET outcome = 'declined' WHERE id = 'declined';
		UPDATE media_requests SET status = 'completed' WHERE id = 'done';
		INSERT INTO media_request_targets (request_id, quality, status, updated_at) VALUES
			('partly-delivered', '1080p', 'completed', now()),
			('partly-delivered', '2160p', 'failed', now());`); err != nil {
		t.Fatal(err)
	}

	candidates, err := repo.ListLibraryWaitCandidates(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, c := range candidates {
		ids[c.ID] = true
	}
	if !ids["pending"] || !ids["failed"] || ids["declined"] || ids["done"] || ids["partly-delivered"] || ids["old-failure"] {
		t.Fatalf("waiting = %v, want pending and recent failed, not declined, completed, partly delivered or old failures", ids)
	}
	inFlight, err := repo.ListReconciliationCandidates(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range inFlight {
		if c.ID == "pending" || c.ID == "failed" {
			t.Fatalf("in-flight candidates include %s", c.ID)
		}
	}

	for _, id := range []string{"pending", "failed"} {
		got, err := repo.MarkAvailable(ctx, id, Viewer{})
		if err != nil {
			t.Fatalf("mark %s available: %v", id, err)
		}
		if got.Status != StatusCompleted || got.Outcome != OutcomeActive || got.LastError != "" || got.CompletedAt == nil {
			t.Fatalf("%s = %+v, want completed, active, no error, completed_at set", id, got)
		}
	}
	// A request that failed after delivering one quality keeps its failure
	// visible for an admin to retry.
	for _, id := range []string{"declined", "partly-delivered"} {
		if _, err := repo.MarkAvailable(ctx, id, Viewer{}); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("mark %s available: err = %v, want ErrInvalidState", id, err)
		}
	}
}
