package requests

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestAdminQueueFiltersDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insert := func(id string, userID, tmdbID int, title string, mediaType MediaType, status Status, outcome Outcome) {
		t.Helper()
		insertLifecycleRequest(t, repo, id, userID, tmdbID, StatusPending)
		if _, err := pool.Exec(ctx, `UPDATE media_requests SET title = $2, media_type = $3, status = $4, outcome = $5 WHERE id = $1`,
			id, title, mediaType, status, outcome); err != nil {
			t.Fatal(err)
		}
	}
	insert("pending", 1, 101, "Severance", MediaTypeSeries, StatusPending, OutcomeActive)
	insert("approved", 2, 102, "Heat", MediaTypeMovie, StatusApproved, OutcomeActive)
	insert("downloading", 1, 103, "100% Wolf", MediaTypeMovie, StatusDownloading, OutcomeActive)
	insert("failed", 2, 104, "Dune", MediaTypeMovie, StatusQueued, OutcomeFailed)
	insert("completed", 1, 105, "The Bear", MediaTypeSeries, StatusCompleted, OutcomeActive)
	insert("declined", 2, 106, "Fight Club", MediaTypeMovie, StatusPending, OutcomeDeclined)

	ids := func(filter ListFilter) []string {
		t.Helper()
		reqs, err := repo.ListAdmin(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(reqs))
		for _, r := range reqs {
			out = append(out, r.ID)
		}
		slices.Sort(out)
		return out
	}
	for _, tc := range []struct {
		name   string
		filter ListFilter
		want   []string
	}{
		{"needs approval", ListFilter{View: AdminViewNeedsApproval}, []string{"pending"}},
		{"in progress", ListFilter{View: AdminViewInProgress}, []string{"approved", "downloading"}},
		{"failed", ListFilter{View: AdminViewFailed}, []string{"failed"}},
		{"done", ListFilter{View: AdminViewDone}, []string{"completed", "declined"}},
		{"title search, any case", ListFilter{Query: "dUnE"}, []string{"failed"}},
		{"a percent sign is literal", ListFilter{Query: "100%"}, []string{"downloading"}},
		{"an underscore is literal", ListFilter{Query: "_"}, []string{}},
		{"a number also matches the TMDB id", ListFilter{Query: "105"}, []string{"completed"}},
		{"a number too big for a TMDB id is only a title search", ListFilter{Query: "99999999999"}, []string{}},
		{"media type", ListFilter{MediaType: MediaTypeSeries}, []string{"completed", "pending"}},
		{"requester and view", ListFilter{RequestedByUserID: 2, View: AdminViewDone}, []string{"declined"}},
	} {
		if got := ids(tc.filter); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}

	counts, err := repo.CountAdminViews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts != (AdminViewCounts{NeedsApproval: 1, InProgress: 2, Failed: 1, Done: 2}) {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestRequestHistoryAndTargetsDatabase(t *testing.T) {
	repo, _ := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "a", 1, 201, StatusPending)
	insertLifecycleRequest(t, repo, "b", 1, 202, StatusPending)
	if _, err := repo.SetStatus(ctx, "a", guardPending, StatusApproved, Viewer{UserID: 1, ProfileID: "profile"}); err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListEvents(ctx, "a", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].EventType != "status_approved" || events[1].EventType != "created" {
		t.Fatalf("events = %+v, want status_approved then created", events)
	}
	if events[0].ActorUserID == nil || *events[0].ActorUserID != 1 {
		t.Fatalf("actor = %v, want account 1", events[0].ActorUserID)
	}

	for _, target := range []Target{{RequestID: "a", Quality: "1080p", Status: StatusQueued}, {RequestID: "a", Quality: "2160p", Status: StatusQueued}, {RequestID: "b", Quality: "1080p", Status: StatusQueued}} {
		if _, err := repo.CreateTarget(ctx, target); err != nil {
			t.Fatal(err)
		}
	}
	byRequest, err := repo.ListTargetsForRequests(ctx, []string{"a", "b", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byRequest["a"]) != 2 || len(byRequest["b"]) != 1 || len(byRequest["missing"]) != 0 {
		t.Fatalf("targets = %+v", byRequest)
	}
}

func TestAdminQueueNeedsAnAdmin(t *testing.T) {
	svc := newTestService(newFakeStore())
	member := testViewer(1)
	if _, err := svc.CountAdminViews(context.Background(), member); !errors.Is(err, ErrForbidden) {
		t.Fatalf("counts: err = %v, want ErrForbidden", err)
	}
	if _, err := svc.ListRequestEvents(context.Background(), member, "r1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("events: err = %v, want ErrForbidden", err)
	}
}

func TestListAdminValidatesFilters(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	admin := Viewer{UserID: 1, ProfileID: "p", IsAdmin: true}
	if _, err := svc.ListAdmin(context.Background(), admin, ListFilter{View: "someday"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown view: err = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.ListAdmin(context.Background(), admin, ListFilter{MediaType: "book"}); !errors.Is(err, ErrInvalidMediaType) {
		t.Fatalf("unknown media type: err = %v, want ErrInvalidMediaType", err)
	}
	if _, err := svc.ListAdmin(context.Background(), admin, ListFilter{View: AdminViewFailed, Query: "dune", MediaType: "tv"}); err != nil {
		t.Fatal(err)
	}
	if got := store.adminFilters[len(store.adminFilters)-1]; got.View != AdminViewFailed || got.Query != "dune" || got.MediaType != MediaTypeSeries {
		t.Fatalf("filter = %+v", got)
	}
}

// A request's history records each aggregate change once, whatever the
// number of targets that caused it.
func TestHistoryRecordsEachChangeOnceDatabase(t *testing.T) {
	repo, _ := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "two", 1, 401, StatusApproved)
	var ids []int64
	for _, quality := range []Quality{"1080p", "2160p"} {
		target, err := repo.CreateTarget(ctx, Target{RequestID: "two", Quality: quality, Status: StatusQueued})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, target.ID)
	}
	for _, id := range ids {
		if _, err := repo.UpdateTargetStatus(ctx, id, StatusQueued, "", "", "", Viewer{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.UpdateTargetStatus(ctx, ids[0], StatusFailed, "", "", "boom", Viewer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateTargetStatus(ctx, ids[1], StatusFailed, "", "", "boom", Viewer{}); err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListEvents(ctx, "two", 20)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for i := len(events) - 1; i >= 0; i-- {
		types = append(types, events[i].EventType)
	}
	want := []string{"created", "approved", "status_queued", "outcome_failed"}
	if !slices.Equal(types, want) {
		t.Fatalf("history = %v, want %v", types, want)
	}
}

func TestAdminClosesAFailedRequest(t *testing.T) {
	store := newFakeStore()
	store.requests["f"] = &Request{ID: "f", MediaType: MediaTypeMovie, TMDBID: 1, Status: StatusQueued, Outcome: OutcomeFailed, RequestedByUserID: 2}
	svc := newTestService(store)

	admin := Viewer{UserID: 1, ProfileID: "a", IsAdmin: true}
	if _, err := svc.Cancel(context.Background(), Viewer{UserID: 2, ProfileID: "p"}, "f", ""); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("owner: err = %v, want ErrInvalidState", err)
	}
	// The frozen v1 cancel keeps refusing a failed request, for admins too.
	if _, err := svc.Cancel(context.Background(), admin, "f", ""); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("v1 admin cancel: err = %v, want ErrInvalidState", err)
	}
	if _, err := svc.AdminCancel(context.Background(), Viewer{UserID: 2, ProfileID: "p"}, "f", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member: err = %v, want ErrForbidden", err)
	}
	closed, err := svc.AdminCancel(context.Background(), admin, "f", "gave up")
	if err != nil || closed.Outcome != OutcomeCancelled {
		t.Fatalf("admin: %+v, %v; want the outcome closed", closed, err)
	}
}

// A target reporting after an admin closed its request leaves the request
// closed.
func TestClosedRequestStaysClosedDatabase(t *testing.T) {
	repo, _ := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "late", 1, 501, StatusApproved)
	target, err := repo.CreateTarget(ctx, Target{RequestID: "late", Quality: "1080p", Status: StatusQueued})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateTargetStatus(ctx, target.ID, StatusFailed, "", "", "boom", Viewer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetOutcome(ctx, "late", guardFailed, OutcomeCancelled, Viewer{UserID: 1, ProfileID: "a"}, "closed"); err != nil {
		t.Fatal(err)
	}
	after, err := repo.UpdateTargetStatus(ctx, target.ID, StatusDownloading, "", "", "", Viewer{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Outcome != OutcomeCancelled {
		t.Fatalf("outcome = %s, want the request to stay closed", after.Outcome)
	}
}
