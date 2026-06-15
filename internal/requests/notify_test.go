package requests

import (
	"context"
	"errors"
	"testing"
)

type fakeNotifier struct {
	requestIDs []string
	contentIDs []string
	err        error
}

func (f *fakeNotifier) NotifyFulfilled(_ context.Context, req Request, contentID string) error {
	if f.err != nil {
		return f.err
	}
	f.requestIDs = append(f.requestIDs, req.ID)
	f.contentIDs = append(f.contentIDs, contentID)
	return nil
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
