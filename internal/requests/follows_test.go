package requests

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// activeRequestFor seeds an active request for the title, owned by another
// account's profile.
func activeRequestFor(store *fakeStore, tmdbID int) *Request {
	req := &Request{
		ID: "req-owner", MediaType: MediaTypeMovie, TMDBID: tmdbID, Title: "Heat",
		Status: StatusPending, Outcome: OutcomeActive,
		RequestedByUserID: 2, RequestedByProfileID: "owner-profile",
	}
	store.requests[req.ID] = req
	store.active[MediaTypeMovie][tmdbID] = req
	return req
}

// Profile ids repeat across accounts (every account from before profiles has
// a "default" one), so a profile on another account with the requester's
// profile id is a follower, not the requester.
func TestFollowSameProfileIDOnAnotherAccount(t *testing.T) {
	store := newFakeStore()
	req := activeRequestFor(store, 949)
	req.RequestedByProfileID = "default"
	svc := newTestService(store)
	viewer := Viewer{UserID: 1, ProfileID: "default"}

	state, err := svc.Follow(context.Background(), viewer, MediaTypeMovie, 949)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if !state.Following || state.RequestedByViewer {
		t.Fatalf("state = %+v, want following and not requested by the viewer", state)
	}
	followers, _ := store.titleFollowers(MediaTypeMovie, 949)
	if len(followers) != 1 || followers[0] != (Follower{UserID: 1, ProfileID: "default"}) {
		t.Fatalf("followers = %+v, want the other account's default profile", followers)
	}
}

// A title's open request is what makes it followable: a series partly in the
// library can have one for its missing seasons, and a title in the library
// with no open request has nothing to follow.
func TestFollowNeedsAnOpenRequestNotAnEmptyLibrary(t *testing.T) {
	store := newFakeStore()
	activeRequestFor(store, 949)
	svc := NewService(store, &fakeTMDBClient{}, presentMovie(949))
	svc.SetUserRepository(requestUserRepo{})
	if _, err := svc.Follow(context.Background(), testViewer(1), MediaTypeMovie, 949); err != nil {
		t.Fatalf("follow an open request for a title partly in the library: %v", err)
	}
	if _, err := svc.Follow(context.Background(), testViewer(1), MediaTypeMovie, 950); !errors.Is(err, ErrNotRequested) {
		t.Fatalf("follow a title with no open request: err = %v, want ErrNotRequested", err)
	}
}

func TestFollowRefusesBlockedAccount(t *testing.T) {
	store := newFakeStore()
	activeRequestFor(store, 949)
	store.limit = &UserLimit{UserID: 1, LimitMode: LimitModeBlocked, ApprovalMode: ApprovalModeInherit}
	if _, err := newTestService(store).Follow(context.Background(), testViewer(1), MediaTypeMovie, 949); !errors.Is(err, ErrUserBlocked) {
		t.Fatalf("err = %v, want ErrUserBlocked", err)
	}
}

// The server-wide announcement has no per-recipient dedupe, so it goes out
// once, from the pass whose stamp took, and not from a pass whose stamp failed
// and is retried.
func TestNotifyFulfilledAnnouncesOnceAfterTheStamp(t *testing.T) {
	store := newFakeStore()
	store.requests["req1"] = completedRequestFixture("req1", 42)
	store.unnotified = []string{"req1"}
	store.markErr = errors.New("stamp failed")
	notifier := &fakeNotifier{}
	svc := NewService(store, &fakeTMDBClient{}, presentMovie(42))
	svc.SetFulfillmentNotifier(notifier)

	svc.notifyFulfilledPending(context.Background())
	if len(notifier.requestIDs) != 1 || len(notifier.announced) != 0 {
		t.Fatalf("after a failed stamp: delivered %v, announced %v; want delivered and not announced", notifier.requestIDs, notifier.announced)
	}

	store.markErr = nil
	svc.notifyFulfilledPending(context.Background())
	svc.notifyFulfilledPending(context.Background())
	if !slices.Equal(notifier.announced, []string{"req1"}) {
		t.Fatalf("announced = %v, want req1 once", notifier.announced)
	}
}

func TestNotifyFulfilledKeepsFollowersWhenDispatchFails(t *testing.T) {
	store := newFakeStore()
	store.requests["req1"] = completedRequestFixture("req1", 42)
	store.unnotified = []string{"req1"}
	store.seedFollow(MediaTypeMovie, 42, Viewer{UserID: 3, ProfileID: "follower-profile"})
	svc := NewService(store, &fakeTMDBClient{}, presentMovie(42))
	svc.SetFulfillmentNotifier(&fakeNotifier{err: errors.New("dispatch failed")})

	svc.notifyFulfilledPending(context.Background())

	if len(store.notified) != 0 || !slices.Equal(store.unnotified, []string{"req1"}) {
		t.Fatalf("failed dispatch: notified = %v, pending = %v; want no stamp and req1 pending", store.notified, store.unnotified)
	}
	if followers, _ := store.titleFollowers(MediaTypeMovie, 42); len(followers) != 1 {
		t.Fatalf("followers after a failed dispatch = %+v, want kept for the retry", followers)
	}
}

// A failed clear must leave the request unstamped, or its follows would
// outlive it and fire for a later request of the title.
func TestNotifyFulfilledRetriesWhenClearingFollowersFails(t *testing.T) {
	store := newFakeStore()
	store.requests["req1"] = completedRequestFixture("req1", 42)
	store.unnotified = []string{"req1"}
	store.seedFollow(MediaTypeMovie, 42, Viewer{UserID: 3, ProfileID: "follower-profile"})
	store.clearErr = errors.New("clear failed")
	svc := NewService(store, &fakeTMDBClient{}, presentMovie(42))
	svc.SetFulfillmentNotifier(&fakeNotifier{})

	svc.notifyFulfilledPending(context.Background())
	if len(store.unnotified) != 1 {
		t.Fatalf("unnotified after a failed clear = %v, want the request kept for a retry", store.unnotified)
	}

	store.clearErr = nil
	svc.notifyFulfilledPending(context.Background())
	if len(store.unnotified) != 0 {
		t.Fatalf("unnotified after the retry = %v, want none", store.unnotified)
	}
	if followers, _ := store.titleFollowers(MediaTypeMovie, 42); len(followers) != 0 {
		t.Fatalf("followers after the retry = %+v, want cleared", followers)
	}
}

// Declining or withdrawing a request clears its title's follows in the same
// transaction. A cleanup after the commit could run once a replacement
// request had gathered followers of its own, and remove theirs.
func TestClosingRequestForgetsFollowsDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	follower := Viewer{UserID: 1, ProfileID: "profile-a"}
	for _, tc := range []struct {
		id      string
		tmdbID  int
		outcome Outcome
	}{
		{"declined", 971, OutcomeDeclined},
		{"withdrawn", 972, OutcomeCancelled},
	} {
		insertLifecycleRequest(t, repo, tc.id, 5, tc.tmdbID, StatusPending)
		if err := repo.FollowTitle(ctx, MediaTypeMovie, tc.tmdbID, follower); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.SetOutcome(ctx, tc.id, guardWithdrawable, tc.outcome, Viewer{}, ""); err != nil {
			t.Fatal(err)
		}
		if followers, err := titleFollowers(ctx, pool, MediaTypeMovie, tc.tmdbID); err != nil || len(followers) != 0 {
			t.Fatalf("followers once %s committed = %+v, err = %v; want none", tc.id, followers, err)
		}
	}

	// A failed request can sit beside a newer open request for the same
	// title. Closing the failed one leaves the open request's follows alone.
	insertLifecycleRequest(t, repo, "failed", 5, 973, StatusApproved)
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET outcome = 'failed' WHERE id = 'failed'`); err != nil {
		t.Fatal(err)
	}
	insertLifecycleRequest(t, repo, "open", 6, 973, StatusPending)
	if err := repo.FollowTitle(ctx, MediaTypeMovie, 973, follower); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetOutcome(ctx, "failed", StateGuard{Outcomes: []Outcome{OutcomeFailed}}, OutcomeCancelled, Viewer{}, ""); err != nil {
		t.Fatal(err)
	}
	if followers, err := titleFollowers(ctx, pool, MediaTypeMovie, 973); err != nil || len(followers) != 1 {
		t.Fatalf("followers of the open request after closing the failed one = %+v, err = %v; want one", followers, err)
	}
}

// titleFollowers lists every follow on a title, whichever request it waits for.
func titleFollowers(ctx context.Context, pool *pgxpool.Pool, mediaType MediaType, tmdbID int) ([]Follower, error) {
	rows, err := pool.Query(ctx, `SELECT user_id, profile_id FROM media_request_follows
		WHERE media_type = $1 AND tmdb_id = $2 ORDER BY user_id, profile_id`, mediaType, tmdbID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Follower, error) {
		var f Follower
		err := row.Scan(&f.UserID, &f.ProfileID)
		return f, err
	})
}
