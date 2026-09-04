package notifications

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/requests"
)

type recordedServerRequestEvent struct {
	event string
	info  RequestEventInfo
}

type recordingRequestLifecycleBackend struct {
	serverEvents       []recordedServerRequestEvent
	personalDeliveries chan string
}

func newRecordingRequestLifecycleBackend() *recordingRequestLifecycleBackend {
	return &recordingRequestLifecycleBackend{personalDeliveries: make(chan string, 1)}
}

func (b *recordingRequestLifecycleBackend) PostServerChannelRequestEvent(
	_ context.Context,
	event string,
	info RequestEventInfo,
) {
	b.serverEvents = append(b.serverEvents, recordedServerRequestEvent{event: event, info: info})
}

func (b *recordingRequestLifecycleBackend) dispatchRequestLifecycle(
	_ context.Context,
	_ requests.Request,
	deliveryType string,
) error {
	b.personalDeliveries <- deliveryType
	return nil
}

func requestApprovalFixture() requests.Request {
	return requests.Request{
		ID:                   "req-1",
		MediaType:            requests.MediaTypeMovie,
		TMDBID:               550,
		Title:                "Fight Club",
		RequestedByUserID:    7,
		RequestedByProfileID: "profile-7",
	}
}

func newTestRequestLifecycleNotifier(backend requestLifecycleBackend) *RequestLifecycleNotifier {
	return newRequestLifecycleNotifier(backend, slog.New(slog.DiscardHandler))
}

func assertApprovalServerEvent(t *testing.T, backend *recordingRequestLifecycleBackend) {
	t.Helper()
	if len(backend.serverEvents) != 1 {
		t.Fatalf("server events = %+v, want one approval event", backend.serverEvents)
	}
	got := backend.serverEvents[0]
	if got.event != ServerChannelEventRequestApproved || got.info.RequestID != "req-1" {
		t.Fatalf("server event = %+v, want request.approved for req-1", got)
	}
}

func TestRequestApprovedAutomaticKeepsServerEventAndSkipsPersonalDelivery(t *testing.T) {
	backend := newRecordingRequestLifecycleBackend()
	notifier := newTestRequestLifecycleNotifier(backend)

	notifier.RequestApproved(context.Background(), requestApprovalFixture(), requests.ApprovalOriginAutomatic)

	assertApprovalServerEvent(t, backend)
	select {
	case deliveryType := <-backend.personalDeliveries:
		t.Fatalf("personal delivery = %q, want none", deliveryType)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRequestApprovedManualKeepsServerAndPersonalDelivery(t *testing.T) {
	backend := newRecordingRequestLifecycleBackend()
	notifier := newTestRequestLifecycleNotifier(backend)

	notifier.RequestApproved(context.Background(), requestApprovalFixture(), requests.ApprovalOriginManual)

	assertApprovalServerEvent(t, backend)
	select {
	case deliveryType := <-backend.personalDeliveries:
		if deliveryType != DeliveryTypeRequestApproved {
			t.Fatalf("personal delivery = %q, want %q", deliveryType, DeliveryTypeRequestApproved)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for manual approval personal delivery")
	}
}

func TestRequestApprovedUnknownOriginRetainsPersonalDelivery(t *testing.T) {
	backend := newRecordingRequestLifecycleBackend()
	notifier := newTestRequestLifecycleNotifier(backend)

	notifier.RequestApproved(context.Background(), requestApprovalFixture(), requests.ApprovalOrigin("future"))

	select {
	case deliveryType := <-backend.personalDeliveries:
		if deliveryType != DeliveryTypeRequestApproved {
			t.Fatalf("personal delivery = %q, want %q", deliveryType, DeliveryTypeRequestApproved)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for conservative personal delivery")
	}
}
