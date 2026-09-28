package requests

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

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
	followed, _ := store.FollowedTitles(context.Background(), MediaTypeMovie, []int{949}, testViewer(1))
	if !followed[949] {
		t.Fatal("follow was not stored")
	}
	if _, err := svc.Follow(context.Background(), testViewer(1), MediaTypeMovie, 949); err != nil {
		t.Fatalf("second Follow: %v (want idempotent)", err)
	}

	if err := svc.Unfollow(context.Background(), testViewer(1), MediaTypeMovie, 949); err != nil {
		t.Fatalf("Unfollow: %v", err)
	}
	followed, _ = store.FollowedTitles(context.Background(), MediaTypeMovie, []int{949}, testViewer(1))
	if followed[949] {
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
	followers, _ := store.ListRequestFollowers(context.Background(), Request{MediaType: MediaTypeMovie, TMDBID: 949})
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
			if followers, _ := store.ListRequestFollowers(context.Background(), Request{MediaType: MediaTypeMovie, TMDBID: 949}); len(followers) != 0 {
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
	if followers, _ := store.ListRequestFollowers(context.Background(), Request{MediaType: MediaTypeMovie, TMDBID: 42}); len(followers) != 0 {
		t.Fatalf("followers after notifying = %+v, want cleared", followers)
	}
}

// A series can have a completed request waiting for the library beside a newer
// open request for other seasons. Its notification goes to the follows made
// before it completed; one made since was made for the open request.
func TestNotifyFulfilledLeavesFollowsOfNewerRequest(t *testing.T) {
	store := newFakeStore()
	completed := time.Now().Add(-time.Hour)
	req := completedRequestFixture("req1", 42)
	req.CompletedAt = &completed
	store.requests["req1"] = req
	store.unnotified = []string{"req1"}
	early := Viewer{UserID: 3, ProfileID: "early"}
	late := Viewer{UserID: 4, ProfileID: "late"}
	store.seedFollowAt(MediaTypeMovie, 42, early, completed.Add(-time.Minute))
	store.seedFollowAt(MediaTypeMovie, 42, late, completed.Add(time.Minute))
	notifier := &fakeNotifier{}
	svc := NewService(store, &fakeTMDBClient{}, presentMovie(42))
	svc.SetFulfillmentNotifier(notifier)

	svc.notifyFulfilledPending(context.Background())

	if len(notifier.followers) != 1 || !slices.Equal(notifier.followers[0], []Follower{{UserID: 3, ProfileID: "early"}}) {
		t.Fatalf("followers handed to the notifier = %+v, want only the follow made before completion", notifier.followers)
	}
	left, _ := store.ListRequestFollowers(context.Background(), Request{MediaType: MediaTypeMovie, TMDBID: 42})
	if !slices.Equal(left, []Follower{{UserID: 4, ProfileID: "late"}}) {
		t.Fatalf("followers left = %+v, want the newer request's follower", left)
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

	if followers, _ := store.ListRequestFollowers(context.Background(), Request{MediaType: MediaTypeMovie, TMDBID: 42}); len(followers) != 1 {
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
	if followers, _ := store.ListRequestFollowers(context.Background(), Request{MediaType: MediaTypeMovie, TMDBID: 42}); len(followers) != 0 {
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

	followers, err := repo.ListRequestFollowers(ctx, Request{MediaType: MediaTypeMovie, TMDBID: 949})
	if err != nil {
		t.Fatal(err)
	}
	if len(followers) != 2 {
		t.Fatalf("movie followers = %+v, want two (the series follow is a different title)", followers)
	}
	followed, err := repo.FollowedTitles(ctx, MediaTypeMovie, []int{949, 950}, a)
	if err != nil {
		t.Fatal(err)
	}
	if !followed[949] || followed[950] {
		t.Fatalf("followed = %v, want only 949", followed)
	}

	if err := repo.ClearRequestFollowers(ctx, Request{MediaType: MediaTypeMovie, TMDBID: 949}, []Follower{{UserID: a.UserID, ProfileID: a.ProfileID}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UnfollowTitle(ctx, MediaTypeMovie, 949, b); err != nil {
		t.Fatal(err)
	}
	if followers, _ := repo.ListRequestFollowers(ctx, Request{MediaType: MediaTypeMovie, TMDBID: 949}); len(followers) != 0 {
		t.Fatalf("movie followers after clear and unfollow = %+v, want none", followers)
	}
	if followers, _ := repo.ListRequestFollowers(ctx, Request{MediaType: MediaTypeSeries, TMDBID: 949}); len(followers) != 1 {
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
	if followers, _ := repo.ListRequestFollowers(ctx, Request{MediaType: MediaTypeMovie, TMDBID: 949}); len(followers) != 2 {
		t.Fatalf("followers sharing a profile id = %+v, want one per account", followers)
	}
	if followed, _ := repo.FollowedTitles(ctx, MediaTypeMovie, []int{949}, Viewer{UserID: 3, ProfileID: "default"}); followed[949] {
		t.Fatal("a third account's default profile sees the others' follow")
	}
	if err := repo.UnfollowTitle(ctx, MediaTypeMovie, 949, mine); err != nil {
		t.Fatal(err)
	}
	followers, err = repo.ListRequestFollowers(ctx, Request{MediaType: MediaTypeMovie, TMDBID: 949})
	if err != nil {
		t.Fatal(err)
	}
	if len(followers) != 1 || followers[0] != (Follower{UserID: theirs.UserID, ProfileID: theirs.ProfileID}) {
		t.Fatalf("followers after one account unfollowed = %+v, want only the other account's", followers)
	}
	if err := repo.ClearRequestFollowers(ctx, Request{MediaType: MediaTypeMovie, TMDBID: 949}, []Follower{{UserID: mine.UserID, ProfileID: mine.ProfileID}}); err != nil {
		t.Fatal(err)
	}
	if followers, _ := repo.ListRequestFollowers(ctx, Request{MediaType: MediaTypeMovie, TMDBID: 949}); len(followers) != 1 {
		t.Fatalf("clearing one account's follow removed %+v, want the other account's kept", followers)
	}

	if _, err := repo.SetOutcome(ctx, "series-949", guardWithdrawable, OutcomeCancelled, Viewer{}, ""); err != nil {
		t.Fatal(err)
	}
	if followers, _ := repo.ListRequestFollowers(ctx, Request{MediaType: MediaTypeSeries, TMDBID: 949}); len(followers) != 0 {
		t.Fatalf("series followers after the withdrawal = %+v, want none", followers)
	}
	if followers, _ := repo.ListRequestFollowers(ctx, Request{MediaType: MediaTypeMovie, TMDBID: 949}); len(followers) != 1 {
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
		if followers, err := repo.ListRequestFollowers(ctx, Request{MediaType: MediaTypeMovie, TMDBID: tc.tmdbID}); err != nil || len(followers) != 0 {
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
	if followers, err := repo.ListRequestFollowers(ctx, Request{MediaType: MediaTypeMovie, TMDBID: 973}); err != nil || len(followers) != 1 {
		t.Fatalf("followers of the open request after closing the failed one = %+v, err = %v; want one", followers, err)
	}
}

// A completed request still waiting for the library keeps the follows made
// before it completed when a newer request for the title is declined; the
// follows made for the declined request go.
func TestDecliningNewerRequestKeepsCompletedRequestFollowsDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	early := Viewer{UserID: 1, ProfileID: "profile-early"}
	late := Viewer{UserID: 2, ProfileID: "profile-late"}
	insertLifecycleRequest(t, repo, "done", 5, 974, StatusPending)
	if err := repo.FollowTitle(ctx, MediaTypeMovie, 974, early); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET status = 'completed', completed_at = now() WHERE id = 'done'`); err != nil {
		t.Fatal(err)
	}
	insertLifecycleRequest(t, repo, "next", 6, 974, StatusPending)
	if err := repo.FollowTitle(ctx, MediaTypeMovie, 974, late); err != nil {
		t.Fatal(err)
	}
	// Pin the follow times either side of the completion.
	if _, err := pool.Exec(ctx, `
		UPDATE media_request_follows f SET created_at = r.completed_at
		  + CASE WHEN f.profile_id = 'profile-early' THEN -interval '1 minute' ELSE interval '1 minute' END
		FROM media_requests r WHERE r.id = 'done' AND f.tmdb_id = 974`); err != nil {
		t.Fatal(err)
	}
	done, err := repo.GetRequest(ctx, "done")
	if err != nil {
		t.Fatal(err)
	}
	followers, err := repo.ListRequestFollowers(ctx, *done)
	if err != nil || !slices.Equal(followers, []Follower{{UserID: 1, ProfileID: "profile-early"}}) {
		t.Fatalf("followers of the completed request = %+v, err = %v; want the early follow only", followers, err)
	}

	if _, err := repo.SetOutcome(ctx, "next", guardWithdrawable, OutcomeDeclined, Viewer{}, ""); err != nil {
		t.Fatal(err)
	}
	followers, err = repo.ListRequestFollowers(ctx, Request{MediaType: MediaTypeMovie, TMDBID: 974})
	if err != nil || !slices.Equal(followers, []Follower{{UserID: 1, ProfileID: "profile-early"}}) {
		t.Fatalf("followers after declining the newer request = %+v, err = %v; want the completed request's follower kept", followers, err)
	}
}

// Two completed requests for one title can both wait for the library, and the
// newer one can arrive first. Each tells only the follows made while it was
// the title's open request, and a clear spares a follow made again since.
func TestRequestFollowersWindowDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "older", 5, 975, StatusPending)
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET status = 'completed', completed_at = now() - interval '2 hours' WHERE id = 'older'`); err != nil {
		t.Fatal(err)
	}
	insertLifecycleRequest(t, repo, "newer", 6, 975, StatusPending)
	if _, err := pool.Exec(ctx, `UPDATE media_requests SET status = 'completed', completed_at = now() - interval '1 hour' WHERE id = 'newer'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_request_follows (media_type, tmdb_id, user_id, profile_id, created_at) VALUES
		('movie', 975, 1, 'profile-older', now() - interval '3 hours'),
		('movie', 975, 2, 'profile-newer', now() - interval '90 minutes')`); err != nil {
		t.Fatal(err)
	}
	get := func(id string) Request {
		t.Helper()
		req, err := repo.GetRequest(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return *req
	}
	older, newer := get("older"), get("newer")
	for _, tc := range []struct {
		req  Request
		want Follower
	}{
		{newer, Follower{UserID: 2, ProfileID: "profile-newer"}},
		{older, Follower{UserID: 1, ProfileID: "profile-older"}},
	} {
		followers, err := repo.ListRequestFollowers(ctx, tc.req)
		if err != nil || !slices.Equal(followers, []Follower{tc.want}) {
			t.Fatalf("followers of %s = %+v, err = %v; want %+v", tc.req.ID, followers, err, tc.want)
		}
	}

	// profile-newer unfollowed and followed again while the notification was
	// going out; the new follow is for a later request.
	if _, err := pool.Exec(ctx, `UPDATE media_request_follows SET created_at = now() WHERE profile_id = 'profile-newer'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClearRequestFollowers(ctx, newer, []Follower{{UserID: 2, ProfileID: "profile-newer"}}); err != nil {
		t.Fatal(err)
	}
	left, err := repo.ListRequestFollowers(ctx, Request{MediaType: MediaTypeMovie, TMDBID: 975})
	if err != nil || len(left) != 2 {
		t.Fatalf("follows after the clear = %+v, err = %v; want both kept", left, err)
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
	if followers, err := repo.ListRequestFollowers(ctx, Request{MediaType: MediaTypeMovie, TMDBID: 959}); err != nil || len(followers) != 0 {
		t.Fatalf("followers after the withdrawal = %+v, err = %v; want none", followers, err)
	}
}
