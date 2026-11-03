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

func heatTitle() WatchlistTitle {
	return WatchlistTitle{MediaType: MediaTypeMovie, TMDBID: 949, Title: "Heat", Year: 1995}
}

func TestWatchlistAddRequestsTitleWithWatchlistSource(t *testing.T) {
	store := newFakeStore()
	svc := newWatchlistTestService(store)

	state, err := svc.RequestFromWatchlist(context.Background(), testViewer(1), heatTitle())
	if err != nil {
		t.Fatalf("RequestFromWatchlist: %v", err)
	}
	if len(store.created) != 1 || store.created[0].Input.Source != SourceWatchlist {
		t.Fatalf("created = %+v, want one request with source watchlist", store.created)
	}
	req := store.active[MediaTypeMovie][949]
	if req == nil || req.Source != SourceWatchlist {
		t.Fatalf("stored request = %+v, want source watchlist", req)
	}
	if state.RequestID != req.ID || !state.RequestedByViewer || !state.Following || state.Requestable {
		t.Fatalf("state = %+v, want the viewer's own new request", state)
	}
	repeated, err := svc.RequestFromWatchlist(context.Background(), testViewer(1), heatTitle())
	if err != nil || len(store.created) != 1 || repeated.RequestID != req.ID {
		t.Fatalf("repeated add = %+v, %v; created %d requests, want the same request", repeated, err, len(store.created))
	}
}

func TestWatchlistAddRefusalReportsReason(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*fakeStore)
		reason string
	}{
		{"quota", func(s *fakeStore) { s.count = s.settings.GlobalMaxRequests }, "quota_exceeded"},
		{"blocked", func(s *fakeStore) { s.limit = &UserLimit{UserID: 1, LimitMode: LimitModeBlocked} }, "blocked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			svc := newWatchlistTestService(store)
			tc.setup(store)

			state, err := svc.RequestFromWatchlist(context.Background(), testViewer(1), heatTitle())
			if err != nil {
				t.Fatalf("RequestFromWatchlist: %v, want the refusal in the state so the entry is kept", err)
			}
			if len(store.created) != 0 {
				t.Fatalf("created = %+v, want none", store.created)
			}
			if state.Requestable || state.Reason != tc.reason {
				t.Fatalf("state = %+v, want not requestable with reason %q", state, tc.reason)
			}
		})
	}
}

func TestWatchlistAddWithSettingOffRequestsNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeStore, *Service)
	}{
		{"server", func(s *fakeStore, _ *Service) { s.settings.WatchlistRequests = false }},
		{"profile", func(_ *fakeStore, svc *Service) {
			svc.SetWatchlistPreference(watchlistPrefFunc(func(int, string) bool { return false }))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			activeRequestFor(store, 950)
			svc := newWatchlistTestService(store)
			tc.setup(store, svc)

			state, err := svc.RequestFromWatchlist(context.Background(), testViewer(1), heatTitle())
			if err != nil {
				t.Fatalf("RequestFromWatchlist: %v", err)
			}
			if len(store.created) != 0 {
				t.Fatalf("created = %+v, want none", store.created)
			}
			if !state.Requestable || state.Reason != "" {
				t.Fatalf("state = %+v, want requestable so the card offers the Request button", state)
			}
			other := heatTitle()
			other.TMDBID = 950
			if _, err := svc.RequestFromWatchlist(context.Background(), testViewer(1), other); err != nil {
				t.Fatalf("RequestFromWatchlist: %v", err)
			}
			if len(store.follows) != 0 {
				t.Fatalf("follows = %v, want none while the setting is off", store.follows)
			}
		})
	}
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

func TestWatchlistRemoveUnfollows(t *testing.T) {
	store := newFakeStore()
	activeRequestFor(store, 949)
	svc := newWatchlistTestService(store)
	if _, err := svc.RequestFromWatchlist(context.Background(), testViewer(1), heatTitle()); err != nil {
		t.Fatalf("RequestFromWatchlist: %v", err)
	}
	if err := svc.WithdrawWatchlistRequest(context.Background(), testViewer(1), MediaTypeMovie, 949); err != nil {
		t.Fatalf("WithdrawWatchlistRequest: %v", err)
	}
	if len(store.follows) != 0 {
		t.Fatalf("follows = %v, want none", store.follows)
	}
	if store.requests["req-owner"].Outcome != OutcomeActive {
		t.Fatal("the other account's request was touched")
	}
}

// A request made under a TMDB ID that TMDB later replaced still belongs to the
// title: the card shows it, a repeat add neither duplicates nor refollows it
// under the new ID, and the viewer's own watchlist request is withdrawn
// through the title's former ID.
func TestWatchlistRequestsFollowTheTitleAcrossTMDBRepoints(t *testing.T) {
	viewer := testViewer(1)
	repointed := WatchlistTitle{MediaType: MediaTypeMovie, TMDBID: 5000, FormerTMDBIDs: []int{949}, Title: "Heat", Year: 1995}

	t.Run("someone else's request under the old ID is followed", func(t *testing.T) {
		store := newFakeStore()
		activeRequestFor(store, 949)
		svc := newWatchlistTestService(store)
		state, err := svc.RequestFromWatchlist(context.Background(), viewer, repointed)
		if err != nil {
			t.Fatalf("RequestFromWatchlist: %v", err)
		}
		if len(store.created) != 0 {
			t.Fatalf("created = %+v, want no duplicate under the new ID", store.created)
		}
		followed, _ := store.FollowedRequests(context.Background(), []string{"req-owner"}, viewer)
		// Another account's request ID stays hidden; its status shows.
		if !followed["req-owner"] || state.Status != StatusPending || !state.Following {
			t.Fatalf("state = %+v, follows = %v; want the old-ID request followed and reported", state, followed)
		}
	})

	t.Run("the viewer's own watchlist request under the old ID", func(t *testing.T) {
		store := newFakeStore()
		svc := newWatchlistTestService(store)
		if _, err := svc.RequestFromWatchlist(context.Background(), viewer, heatTitle()); err != nil {
			t.Fatalf("first add: %v", err)
		}
		req := store.active[MediaTypeMovie][949]
		states, err := svc.WatchlistRequestStates(context.Background(), viewer, []WatchlistTitle{repointed})
		if err != nil {
			t.Fatalf("WatchlistRequestStates: %v", err)
		}
		state := states[WatchlistKey{MediaType: MediaTypeMovie, TMDBID: 5000}]
		if state.RequestID != req.ID || !state.RequestedByViewer {
			t.Fatalf("state = %+v, want the request made under the former ID", state)
		}
		if _, err := svc.RequestFromWatchlist(context.Background(), viewer, repointed); err != nil {
			t.Fatalf("repeat add: %v", err)
		}
		if len(store.created) != 1 {
			t.Fatalf("created %d requests, want 1", len(store.created))
		}
		// The API withdraws under every ID the title has had.
		for _, id := range []int{5000, 949} {
			if err := svc.WithdrawWatchlistRequest(context.Background(), viewer, MediaTypeMovie, id); err != nil {
				t.Fatalf("WithdrawWatchlistRequest(%d): %v", id, err)
			}
		}
		if req.Outcome != OutcomeCancelled {
			t.Fatalf("request outcome = %s, want canceled", req.Outcome)
		}
	})
}

// Deleting a profile cancels the requests its watchlist made that nothing has
// been sent for, and leaves direct, sent and other profiles' requests alone.
func TestWithdrawProfileWatchlistRequests(t *testing.T) {
	store := newFakeStore()
	svc := newWatchlistTestService(store)
	seed := func(id, profileID string, status Status, source Source) *Request {
		req := &Request{
			ID: id, MediaType: MediaTypeMovie, TMDBID: len(store.requests) + 1, Title: id,
			Status: status, Outcome: OutcomeActive, Source: source,
			RequestedByUserID: 1, RequestedByProfileID: profileID,
		}
		store.requests[id] = req
		store.active[MediaTypeMovie][req.TMDBID] = req
		return req
	}
	pending := seed("watchlist-pending", "kids", StatusPending, SourceWatchlist)
	direct := seed("direct-pending", "kids", StatusPending, SourceDirect)
	sent := seed("watchlist-sent", "kids", StatusApproved, SourceWatchlist)
	store.targets = map[string][]Target{sent.ID: {{ID: 1, RequestID: sent.ID, Status: StatusQueued}}}
	other := seed("other-profile", "parent", StatusPending, SourceWatchlist)

	if err := svc.WithdrawProfileWatchlistRequests(context.Background(), 1, "kids"); err != nil {
		t.Fatal(err)
	}
	if pending.Outcome != OutcomeCancelled {
		t.Fatalf("watchlist request of the deleted profile = %s, want canceled", pending.Outcome)
	}
	for _, req := range []*Request{direct, sent, other} {
		if req.Outcome != OutcomeActive {
			t.Fatalf("request %s = %s, want left alone", req.ID, req.Outcome)
		}
	}
}
