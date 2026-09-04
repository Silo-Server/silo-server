package requests

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeNotifier struct {
	requestIDs []string
	contentIDs []string
	err        error
}

type recordedApproval struct {
	requestID string
	origin    ApprovalOrigin
}

type recordingLifecycleNotifier struct {
	submitted []string
	approved  []recordedApproval
	declined  []string
}

func (n *recordingLifecycleNotifier) RequestSubmitted(_ context.Context, req Request) {
	n.submitted = append(n.submitted, req.ID)
}

func (n *recordingLifecycleNotifier) RequestApproved(_ context.Context, req Request, origin ApprovalOrigin) {
	n.approved = append(n.approved, recordedApproval{requestID: req.ID, origin: origin})
}

func (n *recordingLifecycleNotifier) RequestDeclined(_ context.Context, req Request) {
	n.declined = append(n.declined, req.ID)
}

func (f *fakeNotifier) NotifyFulfilled(_ context.Context, req Request, contentID string) error {
	if f.err != nil {
		return f.err
	}
	f.requestIDs = append(f.requestIDs, req.ID)
	f.contentIDs = append(f.contentIDs, contentID)
	return nil
}

func TestCreateRequestAutoApprovalReportsAutomaticOrigin(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalAutoApprovalEnabled = true
	store.integrations = []Integration{autoApproveRouterInst("router-1", "radarr-key")}
	service := newTestService(store)
	service.SetRouterProvider(&fakeRouterProvider{})
	recorder := &recordingLifecycleNotifier{}
	service.SetLifecycleNotifier(recorder)

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if len(recorder.submitted) != 1 || recorder.submitted[0] != req.ID {
		t.Fatalf("submitted notifications = %v, want [%s]", recorder.submitted, req.ID)
	}
	want := []recordedApproval{{requestID: req.ID, origin: ApprovalOriginAutomatic}}
	if !reflect.DeepEqual(recorder.approved, want) {
		t.Fatalf("approved notifications = %+v, want %+v", recorder.approved, want)
	}
}

func TestApproveReportsManualOrigin(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["req-1"] = &Request{
		ID:                   "req-1",
		MediaType:            MediaTypeMovie,
		TMDBID:               550,
		Title:                "Fight Club",
		Status:               StatusPending,
		Outcome:              OutcomeActive,
		RequestedByUserID:    7,
		RequestedByProfileID: "profile-7",
	}
	service := newTestService(store)
	service.SetRouterProvider(&fakeRouterProvider{})
	recorder := &recordingLifecycleNotifier{}
	service.SetLifecycleNotifier(recorder)

	_, err := service.Approve(context.Background(), Viewer{UserID: 99, IsAdmin: true}, "req-1")
	if err != nil {
		t.Fatalf("Approve returned error: %v", err)
	}
	want := []recordedApproval{{requestID: "req-1", origin: ApprovalOriginManual}}
	if !reflect.DeepEqual(recorder.approved, want) {
		t.Fatalf("approved notifications = %+v, want %+v", recorder.approved, want)
	}
}

func completedRequestFixture(id string, tmdbID int) *Request {
	return &Request{
		ID:                   id,
		MediaType:            MediaTypeMovie,
		TMDBID:               tmdbID,
		Title:                "Fixture Movie",
		Status:               StatusCompleted,
		Outcome:              OutcomeActive,
		RequestedByUserID:    7,
		RequestedByProfileID: "profile-1",
	}
}

func TestNotifyFulfilledPendingNotifiesAndMarks(t *testing.T) {
	store := newFakeStore()
	store.requests["req1"] = completedRequestFixture("req1", 42)
	store.unnotified = []string{"req1"}
	presence := &fakePresence{available: map[MediaType]map[int]bool{
		MediaTypeMovie: {42: true},
	}}
	notifier := &fakeNotifier{}
	service := NewService(store, &fakeTMDBClient{}, presence)
	service.SetFulfillmentNotifier(notifier)

	service.notifyFulfilledPending(context.Background())

	if len(notifier.requestIDs) != 1 || notifier.requestIDs[0] != "req1" {
		t.Fatalf("expected one notification for req1, got %v", notifier.requestIDs)
	}
	if want := fakePresenceContentID(MediaTypeMovie, 42); notifier.contentIDs[0] != want {
		t.Fatalf("expected content id %q, got %q", want, notifier.contentIDs[0])
	}
	if len(store.notified) != 1 || store.notified[0] != "req1" {
		t.Fatalf("expected req1 marked notified, got %v", store.notified)
	}
}

func TestNotifyFulfilledPendingWaitsForCatalogMatch(t *testing.T) {
	store := newFakeStore()
	store.requests["req1"] = completedRequestFixture("req1", 42)
	store.unnotified = []string{"req1"}
	notifier := &fakeNotifier{}
	service := NewService(store, &fakeTMDBClient{}, &fakePresence{})
	service.SetFulfillmentNotifier(notifier)

	service.notifyFulfilledPending(context.Background())

	if len(notifier.requestIDs) != 0 {
		t.Fatalf("expected no notification before catalog match, got %v", notifier.requestIDs)
	}
	if len(store.notified) != 0 {
		t.Fatalf("expected request to stay pending, got marked %v", store.notified)
	}
	if len(store.unnotified) != 1 {
		t.Fatalf("expected request to remain in the pending set")
	}
}

func TestNotifyFulfilledPendingRetriesAfterNotifierError(t *testing.T) {
	store := newFakeStore()
	store.requests["req1"] = completedRequestFixture("req1", 42)
	store.unnotified = []string{"req1"}
	presence := &fakePresence{available: map[MediaType]map[int]bool{
		MediaTypeMovie: {42: true},
	}}
	notifier := &fakeNotifier{err: errors.New("dispatch failed")}
	service := NewService(store, &fakeTMDBClient{}, presence)
	service.SetFulfillmentNotifier(notifier)

	service.notifyFulfilledPending(context.Background())

	if len(store.notified) != 0 {
		t.Fatalf("a failed dispatch must not mark the request notified, got %v", store.notified)
	}
	if len(store.unnotified) != 1 {
		t.Fatalf("expected request to remain pending for the next run")
	}
}
