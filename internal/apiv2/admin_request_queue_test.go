package apiv2

import (
	"context"
	"net/http"
	"testing"
	"time"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// fakeQueueAdmin adds view counts and request history to the admin request
// fake.
type fakeQueueAdmin struct {
	*fakeAdminRequests
	eventsFor string
}

func (f *fakeQueueAdmin) CountAdminViews(_ context.Context, v mediarequests.Viewer) (mediarequests.AdminViewCounts, error) {
	f.viewer = v
	return mediarequests.AdminViewCounts{NeedsApproval: 3, InProgress: 2, Failed: 1, Done: 9}, nil
}

func (f *fakeQueueAdmin) ListRequestEvents(_ context.Context, _ mediarequests.Viewer, id string) ([]mediarequests.RequestEvent, error) {
	if id != "r-1" {
		return nil, mediarequests.ErrNotFound
	}
	f.eventsFor = id
	actor := 7
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	return []mediarequests.RequestEvent{
		{ID: 2, RequestID: id, EventType: "outcome_declined", ActorUserID: &actor, ActorUsername: "admin", Message: "Not this month", CreatedAt: at},
		{ID: 1, RequestID: id, EventType: "created", CreatedAt: at.Add(-time.Hour)},
	}, nil
}

func queueAdminHandler(f *fakeQueueAdmin) http.Handler {
	deps := requestDeps(fixtureRequests())
	deps.AdminRequests = f
	return NewHandler(deps)
}

func TestAdminRequestQueueFiltersCountsAndHistory(t *testing.T) {
	f := &fakeQueueAdmin{fakeAdminRequests: fixtureAdminRequests()}
	h := queueAdminHandler(f)

	list := do(t, h, http.MethodGet, Prefix+"/admin/requests?view=failed&q=dune&media_type=movie&requested_by_user_id=7", "", actingRequestAdmin)
	if list.Code != 200 {
		t.Fatal(list.Code, list.Body.String())
	}
	if got := f.filter; got.View != mediarequests.AdminViewFailed || got.Query != "dune" || got.MediaType != mediarequests.MediaTypeMovie || got.RequestedByUserID != 7 {
		t.Fatalf("filter = %+v", got)
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/requests?view=someday", "", actingRequestAdmin), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/requests?requested_by_user_id=0", "", actingRequestAdmin), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/requests?requested_by_user_id=2147483648", "", actingRequestAdmin), TypeValidationFailed)

	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/requests/counts", "", requestOwner), TypePermissionDenied)
	var counts AdminRequestCounts
	rec := do(t, h, http.MethodGet, Prefix+"/admin/requests/counts", "", actingRequestAdmin)
	decodeBody(t, rec.Body, &counts)
	if rec.Code != 200 || counts != (AdminRequestCounts{NeedsApproval: 3, InProgress: 2, Failed: 1, Done: 9}) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	var history struct {
		Items []AdminRequestEvent `json:"items"`
	}
	rec = do(t, h, http.MethodGet, Prefix+"/admin/requests/r-1/events", "", actingRequestAdmin)
	decodeBody(t, rec.Body, &history)
	if rec.Code != 200 || len(history.Items) != 2 || history.Items[0].Type != "outcome_declined" ||
		history.Items[0].ActorUserID != "7" || history.Items[0].ActorUsername != "admin" || history.Items[1].ActorUserID != "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/requests/missing/events", "", actingRequestAdmin), TypeNotFound)
}
