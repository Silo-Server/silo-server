package requests

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
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

func TestFollowTitleSomeoneElseRequested(t *testing.T) {
	store := newFakeStore()
	activeRequestFor(store, 949)
	svc := newTestService(store)

	state, err := svc.Follow(context.Background(), testViewer(1), MediaTypeMovie, 949)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if !state.Following || state.RequestedByViewer || state.Requestable || state.Reason != "already_requested" || state.RequestID != "" {
		t.Fatalf("state = %+v, want following, not requestable, request id hidden from another account", state)
	}
	followed, _ := store.FollowedRequests(context.Background(), []string{"req-owner"}, testViewer(1))
	if !followed["req-owner"] {
		t.Fatal("follow was not stored")
	}
	if _, err := svc.Follow(context.Background(), testViewer(1), MediaTypeMovie, 949); err != nil {
		t.Fatalf("second Follow: %v (want idempotent)", err)
	}

	if err := svc.Unfollow(context.Background(), testViewer(1), MediaTypeMovie, 949); err != nil {
		t.Fatalf("Unfollow: %v", err)
	}
	followed, _ = store.FollowedRequests(context.Background(), []string{"req-owner"}, testViewer(1))
	if followed["req-owner"] {
		t.Fatal("follow survived Unfollow")
	}
}

func TestFollowOwnRequestStoresNothing(t *testing.T) {
	store := newFakeStore()
	req := activeRequestFor(store, 949)
	req.RequestedByUserID, req.RequestedByProfileID = 1, "profile-1"
	svc := newTestService(store)

	state, err := svc.Follow(context.Background(), testViewer(1), MediaTypeMovie, 949)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if !state.Following || !state.RequestedByViewer || state.RequestID != "req-owner" {
		t.Fatalf("state = %+v, want following, requested by the viewer, with its request id", state)
	}
	if len(store.follows) != 0 {
		t.Fatalf("follows = %v, want none: the requester is always notified", store.follows)
	}
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

func TestFollowRefusesTitleWithoutActiveRequest(t *testing.T) {
	svc := newTestService(newFakeStore())
	if _, err := svc.Follow(context.Background(), testViewer(1), MediaTypeMovie, 949); !errors.Is(err, ErrNotRequested) {
		t.Fatalf("err = %v, want ErrNotRequested", err)
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

func TestFollowRefusesWhenRequestsDisabled(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = false
	activeRequestFor(store, 949)
	if _, err := newTestService(store).Follow(context.Background(), testViewer(1), MediaTypeMovie, 949); !errors.Is(err, ErrRequestsDisabled) {
		t.Fatalf("err = %v, want ErrRequestsDisabled", err)
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

// A declined or withdrawn request is no longer on its way, so its title's
// follows are dropped rather than left where the follower cannot see them.
func TestWithdrawingRequestForgetsFollows(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withdraw func(*Service) error
	}{
		{"decline", func(s *Service) error {
			_, err := s.Decline(context.Background(), Viewer{UserID: 9, IsAdmin: true}, "req-owner", "")
			return err
		}},
		{"cancel", func(s *Service) error {
			_, err := s.Cancel(context.Background(), Viewer{UserID: 2, ProfileID: "owner-profile"}, "req-owner", "")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			activeRequestFor(store, 949)
			svc := newTestService(store)
			if _, err := svc.Follow(context.Background(), testViewer(1), MediaTypeMovie, 949); err != nil {
				t.Fatalf("Follow: %v", err)
			}
			if err := tc.withdraw(svc); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if followers, _ := store.titleFollowers(MediaTypeMovie, 949); len(followers) != 0 {
				t.Fatalf("followers after %s = %+v, want none", tc.name, followers)
			}
		})
	}
}

func TestSearchMarksFollowedAndOwnTitles(t *testing.T) {
	store := newFakeStore()
	activeRequestFor(store, 949)
	own := &Request{ID: "req-own", MediaType: MediaTypeMovie, TMDBID: 950, Status: StatusPending, Outcome: OutcomeActive,
		RequestedByUserID: 1, RequestedByProfileID: "profile-1"}
	store.requests[own.ID] = own
	store.active[MediaTypeMovie][950] = own
	other := &Request{ID: "req-other", MediaType: MediaTypeMovie, TMDBID: 951, Status: StatusPending, Outcome: OutcomeActive,
		RequestedByUserID: 3, RequestedByProfileID: "someone"}
	store.requests[other.ID] = other
	store.active[MediaTypeMovie][951] = other
	if err := store.FollowTitle(context.Background(), MediaTypeMovie, 949, testViewer(1)); err != nil {
		t.Fatal(err)
	}
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{page: &tmdb.MediaPage{Page: 1, Results: []tmdb.MediaResult{
		{ID: 949, MediaType: "movie", Title: "Heat"},
		{ID: 950, MediaType: "movie", Title: "Ronin"},
		{ID: 951, MediaType: "movie", Title: "Thief"},
	}}})

	page, err := svc.Search(context.Background(), testViewer(1), "heat", MediaTypeMovie, 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	following := map[int]bool{}
	for _, r := range page.Results {
		following[r.TMDBID] = r.Request.Following
	}
	if !following[949] || !following[950] || following[951] {
		t.Fatalf("following = %v, want the followed and the own title, not the other account's", following)
	}
}

func TestNotifyFulfilledTellsFollowersAndClearsThem(t *testing.T) {
	store := newFakeStore()
	store.requests["req1"] = completedRequestFixture("req1", 42)
	store.unnotified = []string{"req1"}
	store.seedFollow(MediaTypeMovie, 42, Viewer{UserID: 3, ProfileID: "follower-profile"})
	notifier := &fakeNotifier{}
	svc := NewService(store, &fakeTMDBClient{}, presentMovie(42))
	svc.SetFulfillmentNotifier(notifier)

	svc.notifyFulfilledPending(context.Background())

	if len(notifier.followers) != 1 || len(notifier.followers[0]) != 1 || notifier.followers[0][0] != (Follower{UserID: 3, ProfileID: "follower-profile"}) {
		t.Fatalf("followers handed to the notifier = %+v, want the one follower", notifier.followers)
	}
	if followers, _ := store.titleFollowers(MediaTypeMovie, 42); len(followers) != 0 {
		t.Fatalf("followers after notifying = %+v, want cleared", followers)
	}
}

// A series can have a completed request waiting for the library beside a newer
// open request for other seasons. Its notification goes to its own follows;
// the open request's follows wait for it.
func TestNotifyFulfilledLeavesFollowsOfNewerRequest(t *testing.T) {
	store := newFakeStore()
	store.requests["req1"] = completedRequestFixture("req1", 42)
	store.unnotified = []string{"req1"}
	store.seedFollowFor(MediaTypeMovie, 42, Viewer{UserID: 3, ProfileID: "early"}, "req1")
	store.seedFollowFor(MediaTypeMovie, 42, Viewer{UserID: 4, ProfileID: "late"}, "req2")
	notifier := &fakeNotifier{}
	svc := NewService(store, &fakeTMDBClient{}, presentMovie(42))
	svc.SetFulfillmentNotifier(notifier)

	svc.notifyFulfilledPending(context.Background())

	if len(notifier.followers) != 1 || !slices.Equal(notifier.followers[0], []Follower{{UserID: 3, ProfileID: "early"}}) {
		t.Fatalf("followers handed to the notifier = %+v, want only req1's follower", notifier.followers)
	}
	left, _ := store.ListRequestFollowers(context.Background(), Request{ID: "req2", MediaType: MediaTypeMovie, TMDBID: 42})
	if !slices.Equal(left, []Follower{{UserID: 4, ProfileID: "late"}}) {
		t.Fatalf("req2's followers = %+v, want kept", left)
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

func TestFollowsDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	// The schema copy has no user_profiles foreign key; the migration's key is
	// exercised by the migrated database, not here.
	if err := repo.FollowTitle(ctx, MediaTypeMovie, 949, Viewer{UserID: 1, ProfileID: "profile-a"}); !errors.Is(err, ErrNotRequested) {
		t.Fatalf("follow with no open request: err = %v, want ErrNotRequested", err)
	}
	insertLifecycleRequest(t, repo, "movie-949", 5, 949, StatusPending)
	insertLifecycleRequest(t, repo, "series-949", 5, 1, StatusPending)
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET media_type = 'series', tmdb_id = 949 WHERE id = 'series-949'`); err != nil {
		t.Fatal(err)
	}
	a := Viewer{UserID: 1, ProfileID: "profile-a"}
	b := Viewer{UserID: 2, ProfileID: "profile-b"}
	for range 2 {
		if err := repo.FollowTitle(ctx, MediaTypeMovie, 949, a); err != nil {
			t.Fatalf("follow (idempotent): %v", err)
		}
	}
	if err := repo.FollowTitle(ctx, MediaTypeMovie, 949, b); err != nil {
		t.Fatal(err)
	}
	if err := repo.FollowTitle(ctx, MediaTypeSeries, 949, a); err != nil {
		t.Fatal(err)
	}

	followers, err := repo.ListRequestFollowers(ctx, Request{ID: "movie-949", MediaType: MediaTypeMovie, TMDBID: 949})
	if err != nil {
		t.Fatal(err)
	}
	if len(followers) != 2 {
		t.Fatalf("movie followers = %+v, want two (the series follow is a different title)", followers)
	}
	followed, err := repo.FollowedRequests(ctx, []string{"movie-949", "series-949", "other"}, b)
	if err != nil {
		t.Fatal(err)
	}
	if !followed["movie-949"] || len(followed) != 1 {
		t.Fatalf("followed = %v, want only movie-949", followed)
	}

	if err := repo.ClearRequestFollowers(ctx, Request{ID: "movie-949", MediaType: MediaTypeMovie, TMDBID: 949}, []Follower{{UserID: a.UserID, ProfileID: a.ProfileID}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UnfollowTitle(ctx, MediaTypeMovie, 949, b); err != nil {
		t.Fatal(err)
	}
	if followers, _ := repo.ListRequestFollowers(ctx, Request{ID: "movie-949", MediaType: MediaTypeMovie, TMDBID: 949}); len(followers) != 0 {
		t.Fatalf("movie followers after clear and unfollow = %+v, want none", followers)
	}
	if followers, _ := repo.ListRequestFollowers(ctx, Request{ID: "series-949", MediaType: MediaTypeSeries, TMDBID: 949}); len(followers) != 1 {
		t.Fatalf("series followers = %+v, want the one untouched follow", followers)
	}

	// Two accounts' profiles can share an id; each keeps its own follow.
	mine := Viewer{UserID: 1, ProfileID: "default"}
	theirs := Viewer{UserID: 2, ProfileID: "default"}
	for _, v := range []Viewer{mine, theirs} {
		if err := repo.FollowTitle(ctx, MediaTypeMovie, 949, v); err != nil {
			t.Fatalf("follow as account %d: %v", v.UserID, err)
		}
	}
	if followers, _ := repo.ListRequestFollowers(ctx, Request{ID: "movie-949", MediaType: MediaTypeMovie, TMDBID: 949}); len(followers) != 2 {
		t.Fatalf("followers sharing a profile id = %+v, want one per account", followers)
	}
	if followed, _ := repo.FollowedRequests(ctx, []string{"movie-949"}, Viewer{UserID: 3, ProfileID: "default"}); followed["movie-949"] {
		t.Fatal("a third account's default profile sees the others' follow")
	}
	if err := repo.UnfollowTitle(ctx, MediaTypeMovie, 949, mine); err != nil {
		t.Fatal(err)
	}
	followers, err = repo.ListRequestFollowers(ctx, Request{ID: "movie-949", MediaType: MediaTypeMovie, TMDBID: 949})
	if err != nil {
		t.Fatal(err)
	}
	if len(followers) != 1 || followers[0] != (Follower{UserID: theirs.UserID, ProfileID: theirs.ProfileID}) {
		t.Fatalf("followers after one account unfollowed = %+v, want only the other account's", followers)
	}
	if err := repo.ClearRequestFollowers(ctx, Request{ID: "movie-949", MediaType: MediaTypeMovie, TMDBID: 949}, []Follower{{UserID: mine.UserID, ProfileID: mine.ProfileID}}); err != nil {
		t.Fatal(err)
	}
	if followers, _ := repo.ListRequestFollowers(ctx, Request{ID: "movie-949", MediaType: MediaTypeMovie, TMDBID: 949}); len(followers) != 1 {
		t.Fatalf("clearing one account's follow removed %+v, want the other account's kept", followers)
	}

	if _, err := repo.SetOutcome(ctx, "series-949", guardWithdrawable, OutcomeCancelled, Viewer{}, ""); err != nil {
		t.Fatal(err)
	}
	if followers, _ := repo.ListRequestFollowers(ctx, Request{ID: "series-949", MediaType: MediaTypeSeries, TMDBID: 949}); len(followers) != 0 {
		t.Fatalf("series followers after the withdrawal = %+v, want none", followers)
	}
	if followers, _ := repo.ListRequestFollowers(ctx, Request{ID: "movie-949", MediaType: MediaTypeMovie, TMDBID: 949}); len(followers) != 1 {
		t.Fatalf("movie followers after the series withdrawal = %+v, want the one left", followers)
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

func completeLifecycleRequest(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `UPDATE media_requests SET status = 'completed', completed_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

// A series can have completed requests still waiting for the library beside a
// newer open request for other seasons. Each request's notification goes to
// the follows made while it was open; clearing them spares a profile that
// followed again since, and declining the open request keeps the others.
func TestRequestFollowersDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	a := Viewer{UserID: 1, ProfileID: "profile-a"}
	b := Viewer{UserID: 2, ProfileID: "profile-b"}
	follow := func(v Viewer) {
		t.Helper()
		if err := repo.FollowTitle(ctx, MediaTypeMovie, 975, v); err != nil {
			t.Fatal(err)
		}
	}
	list := func(id string) []Follower {
		t.Helper()
		followers, err := repo.ListRequestFollowers(ctx, Request{ID: id, MediaType: MediaTypeMovie, TMDBID: 975})
		if err != nil {
			t.Fatal(err)
		}
		return followers
	}
	insertLifecycleRequest(t, repo, "older", 5, 975, StatusPending)
	follow(a)
	completeLifecycleRequest(t, pool, "older")
	insertLifecycleRequest(t, repo, "newer", 6, 975, StatusPending)
	follow(b)
	completeLifecycleRequest(t, pool, "newer")
	if got := list("older"); !slices.Equal(got, []Follower{{UserID: 1, ProfileID: "profile-a"}}) {
		t.Fatalf("older's followers = %+v, want profile-a", got)
	}
	if got := list("newer"); !slices.Equal(got, []Follower{{UserID: 2, ProfileID: "profile-b"}}) {
		t.Fatalf("newer's followers = %+v, want profile-b", got)
	}

	// profile-b unfollows and follows a third request while newer's
	// notification is going out; newer's clear leaves the new follow.
	if err := repo.UnfollowTitle(ctx, MediaTypeMovie, 975, b); err != nil {
		t.Fatal(err)
	}
	insertLifecycleRequest(t, repo, "third", 7, 975, StatusPending)
	follow(b)
	if err := repo.ClearRequestFollowers(ctx, Request{ID: "newer", MediaType: MediaTypeMovie, TMDBID: 975}, []Follower{{UserID: 2, ProfileID: "profile-b"}}); err != nil {
		t.Fatal(err)
	}
	if got := list("third"); len(got) != 1 {
		t.Fatalf("third's followers after newer's clear = %+v, want profile-b kept", got)
	}
	// profile-a, still waiting for older, follows third too.
	follow(a)
	if got := list("third"); len(got) != 2 {
		t.Fatalf("third's followers = %+v, want profile-a and profile-b", got)
	}
	if followed, err := repo.FollowedRequests(ctx, []string{"third"}, a); err != nil || !followed["third"] {
		t.Fatalf("profile-a follows third = %v, err = %v; want true", followed, err)
	}

	if _, err := repo.SetOutcome(ctx, "third", guardWithdrawable, OutcomeDeclined, Viewer{}, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := titleFollowers(ctx, pool, MediaTypeMovie, 975); err != nil || !slices.Equal(got, []Follower{{UserID: 1, ProfileID: "profile-a"}}) {
		t.Fatalf("follows after declining third = %+v, err = %v; want older's kept", got, err)
	}
}

// A follow that commits while a completion is under way belongs to the
// request it read, even though the completion's timestamp, taken when its
// transaction began, is earlier than the follow's.
func TestFollowDuringCompletionDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "req", 5, 976, StatusPending)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT now()`); err != nil {
		t.Fatal(err)
	}
	if err := repo.FollowTitle(ctx, MediaTypeMovie, 976, Viewer{UserID: 1, ProfileID: "profile-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE media_requests SET status = 'completed', completed_at = now() WHERE id = 'req'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var inverted bool
	if err := pool.QueryRow(ctx, `SELECT f.created_at > r.completed_at FROM media_request_follows f
		JOIN media_requests r ON r.id = 'req' WHERE f.tmdb_id = 976`).Scan(&inverted); err != nil || !inverted {
		t.Fatalf("follow made after the completion's timestamp = %v, err = %v; the race was not reproduced", inverted, err)
	}
	followers, err := repo.ListRequestFollowers(ctx, Request{ID: "req", MediaType: MediaTypeMovie, TMDBID: 976})
	if err != nil || len(followers) != 1 {
		t.Fatalf("followers = %+v, err = %v; want the follow made during the completion", followers, err)
	}
}

// A follow survives its request failing: the title's next request takes it,
// including when the requester's new request replaces the failed one.
func TestNewRequestAdoptsFollowsOfFailedRequestDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	for _, tc := range []struct {
		tmdbID  int
		replace bool
	}{{977, false}, {978, true}} {
		insertLifecycleRequest(t, repo, fmt.Sprintf("failed-%d", tc.tmdbID), 5, tc.tmdbID, StatusApproved)
		if err := repo.FollowTitle(ctx, MediaTypeMovie, tc.tmdbID, Viewer{UserID: 1, ProfileID: "profile-a"}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE media_requests SET outcome = 'failed' WHERE id = $1`, fmt.Sprintf("failed-%d", tc.tmdbID)); err != nil {
			t.Fatal(err)
		}
		retry := fmt.Sprintf("retry-%d", tc.tmdbID)
		if _, err := repo.CreateRequest(ctx, CreateRequestRecord{
			ID:            retry,
			Input:         CreateRequestInput{MediaType: MediaTypeMovie, TMDBID: tc.tmdbID, Title: "Retry"},
			Status:        StatusPending,
			Outcome:       OutcomeActive,
			Requester:     Viewer{UserID: 5, ProfileID: "profile"},
			ReplaceFailed: tc.replace,
		}); err != nil {
			t.Fatal(err)
		}
		followers, err := repo.ListRequestFollowers(ctx, Request{ID: retry, MediaType: MediaTypeMovie, TMDBID: tc.tmdbID})
		if err != nil || len(followers) != 1 {
			t.Fatalf("replace=%v: the new request's followers = %+v, err = %v; want the failed request's follow", tc.replace, followers, err)
		}
	}
}

// An unfollow that runs while a new request takes over a failed request's
// follows waits for it and removes the moved follow, rather than returning
// with the profile still following the new request.
func TestUnfollowDuringAdoptionDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	follower := Viewer{UserID: 1, ProfileID: "profile-a"}
	insertLifecycleRequest(t, repo, "failed", 5, 979, StatusApproved)
	if err := repo.FollowTitle(ctx, MediaTypeMovie, 979, follower); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET outcome = 'failed' WHERE id = 'failed'`); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var adopter int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&adopter); err != nil {
		t.Fatal(err)
	}
	retry, err := repo.insertRequest(ctx, tx, CreateRequestRecord{
		ID:        "retry",
		Input:     CreateRequestInput{MediaType: MediaTypeMovie, TMDBID: 979, Title: "Retry"},
		Requester: Viewer{UserID: 6, ProfileID: "profile"},
	}, StatusPending, OutcomeActive, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := adoptTitleFollows(ctx, tx, retry); err != nil {
		t.Fatal(err)
	}

	unfollowed := make(chan error, 1)
	go func() { unfollowed <- repo.UnfollowTitle(ctx, MediaTypeMovie, 979, follower) }()
	for blocked := false; !blocked; {
		select {
		case err := <-unfollowed:
			t.Fatalf("unfollow finished while the adoption was open: err = %v, want it to wait", err)
		default:
		}
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, adopter).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if !blocked {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-unfollowed; err != nil {
		t.Fatal(err)
	}
	if followers, err := titleFollowers(ctx, pool, MediaTypeMovie, 979); err != nil || len(followers) != 0 {
		t.Fatalf("follows after the unfollow = %+v, err = %v; want none", followers, err)
	}
}

// A follow racing a withdrawal must not outlive it: the follow waits for the
// withdrawal to commit, sees the request closed, and inserts nothing, so the
// follow cleanup that ran with the withdrawal leaves no stray follower behind.
func TestFollowWaitsForConcurrentWithdrawalDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "req-race", 5, 959, StatusPending)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var withdrawer int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&withdrawer); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE media_requests SET outcome = 'cancelled', updated_at = now() WHERE id = 'req-race'`); err != nil {
		t.Fatal(err)
	}

	followed := make(chan error, 1)
	go func() {
		followed <- repo.FollowTitle(ctx, MediaTypeMovie, 959, Viewer{UserID: 1, ProfileID: "profile-a"})
	}()
	// Wait until the follow is blocked behind the open withdrawal. A follow
	// that does not wait finishes first and is caught below.
	for blocked := false; !blocked; {
		select {
		case err := <-followed:
			t.Fatalf("follow finished while the withdrawal was open: err = %v, want it to wait", err)
		default:
		}
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, withdrawer).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if !blocked {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM media_request_follows WHERE media_type = 'movie' AND tmdb_id = 959`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if err := <-followed; !errors.Is(err, ErrNotRequested) {
		t.Fatalf("follow after the withdrawal: err = %v, want ErrNotRequested", err)
	}
	if followers, err := titleFollowers(ctx, pool, MediaTypeMovie, 959); err != nil || len(followers) != 0 {
		t.Fatalf("followers after the withdrawal = %+v, err = %v; want none", followers, err)
	}
}
