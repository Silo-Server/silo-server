package requests

import (
	"context"
	"testing"
)

// watchlistPrefFunc stands in for the profile's requests.watchlist_auto_request.
type watchlistPrefFunc func(userID int, profileID string) bool

func (f watchlistPrefFunc) WatchlistAutoRequest(_ context.Context, userID int, profileID string) bool {
	return f(userID, profileID)
}

func newWatchlistTestService(store *fakeStore) *Service {
	store.settings.WatchlistRequests = true
	store.trackActive = true
	return newTestService(store)
}

func TestWatchlistRemoveCancelsOnlyUnsentWatchlistRequests(t *testing.T) {
	viewer := testViewer(1)
	seed := func(store *fakeStore, id string, tmdbID int, status Status, source Source) *Request {
		req := &Request{
			ID: id, MediaType: MediaTypeMovie, TMDBID: tmdbID, Title: "Heat",
			Status: status, Outcome: OutcomeActive, Source: source,
			RequestedByUserID: viewer.UserID, RequestedByProfileID: viewer.ProfileID,
		}
		store.requests[id] = req
		store.active[MediaTypeMovie][tmdbID] = req
		return req
	}
	store := newFakeStore()
	svc := newWatchlistTestService(store)
	pending := seed(store, "watchlist-pending", 1, StatusPending, SourceWatchlist)
	direct := seed(store, "direct-pending", 2, StatusPending, SourceDirect)
	sent := seed(store, "watchlist-sent", 3, StatusApproved, SourceWatchlist)
	store.targets = map[string][]Target{sent.ID: {{ID: 1, RequestID: sent.ID, Status: StatusQueued}}}
	other := seed(store, "other-profile", 4, StatusPending, SourceWatchlist)
	other.RequestedByProfileID = "another-profile"

	for tmdbID := 1; tmdbID <= 4; tmdbID++ {
		if err := svc.WithdrawWatchlistRequest(context.Background(), viewer, MediaTypeMovie, tmdbID); err != nil {
			t.Fatalf("WithdrawWatchlistRequest(%d): %v", tmdbID, err)
		}
	}
	if pending.Outcome != OutcomeCancelled || pending.OutcomeReason != withdrawnFromWatchlist {
		t.Fatalf("watchlist-created pending request = %+v, want canceled", pending)
	}
	for _, req := range []*Request{direct, sent, other} {
		if req.Outcome != OutcomeActive {
			t.Fatalf("request %s outcome = %s, want left alone", req.ID, req.Outcome)
		}
	}
}
