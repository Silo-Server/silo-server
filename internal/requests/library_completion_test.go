package requests

import (
	"context"
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
