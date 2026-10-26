package requests

import (
	"context"
	"errors"
	"testing"
)

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
