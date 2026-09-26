package notifications

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/requests"
)

type recordedServerRequestEvent struct {
	event string
	info  RequestEventInfo
}

// recordingRequestLifecycleBackend stands in for *System. Both backend methods
// detach in production, so recording them synchronously here keeps the
// adapter's decisions observable without a goroutine to join.
type recordingRequestLifecycleBackend struct {
	serverEvents       []recordedServerRequestEvent
	personalDeliveries []string
}

func (b *recordingRequestLifecycleBackend) PostServerChannelRequestEvent(
	_ context.Context,
	event string,
	info RequestEventInfo,
) {
	b.serverEvents = append(b.serverEvents, recordedServerRequestEvent{event: event, info: info})
}

func (b *recordingRequestLifecycleBackend) dispatchRequestLifecycleDetached(
	_ context.Context,
	_ requests.Request,
	deliveryType string,
) {
	b.personalDeliveries = append(b.personalDeliveries, deliveryType)
}

// approve runs one approval through the adapter, checks the server-channel
// broadcast that every origin owes, and returns the backend plus whatever was
// logged for the personal-delivery assertions below.
func approve(t *testing.T, origin requests.ApprovalOrigin) (*recordingRequestLifecycleBackend, string) {
	t.Helper()
	var logs bytes.Buffer
	backend := &recordingRequestLifecycleBackend{}
	notifier := &RequestLifecycleNotifier{backend: backend, logger: slog.New(slog.NewTextHandler(&logs, nil))}

	notifier.RequestApproved(context.Background(), requests.Request{
		ID:                   "req-1",
		MediaType:            requests.MediaTypeMovie,
		TMDBID:               550,
		Title:                "Fight Club",
		RequestedByUserID:    7,
		RequestedByProfileID: "profile-7",
	}, origin)

	if len(backend.serverEvents) != 1 {
		t.Fatalf("server events = %+v, want one approval event", backend.serverEvents)
	}
	got := backend.serverEvents[0]
	if got.event != ServerChannelEventRequestApproved || got.info.RequestID != "req-1" {
		t.Fatalf("server event = %+v, want request.approved for req-1", got)
	}
	return backend, logs.String()
}

func TestRequestApprovedByAdminKeepsServerAndPersonalDelivery(t *testing.T) {
	backend, _ := approve(t, requests.ApprovalOriginAdmin)

	want := []string{DeliveryTypeRequestApproved}
	if !slices.Equal(backend.personalDeliveries, want) {
		t.Fatalf("personal deliveries = %v, want %v", backend.personalDeliveries, want)
	}
}

func TestRequestApprovedByPolicyKeepsServerEventAndSkipsPersonalDelivery(t *testing.T) {
	backend, logs := approve(t, requests.ApprovalOriginPolicy)

	if len(backend.personalDeliveries) != 0 {
		t.Fatalf("personal deliveries = %v, want none", backend.personalDeliveries)
	}
	if strings.Contains(logs, "unrecognized request approval origin") {
		t.Fatalf("auto-approval logged an unrecognized origin: %s", logs)
	}
}

// An origin this build does not know about must fail closed: a future
// policy-driven approval path may not silently resurrect the requester notice
// (issue #590). The warning is that trade-off's safety net.
func TestRequestApprovedUnknownOriginSkipsPersonalDeliveryAndWarns(t *testing.T) {
	cases := []struct {
		name   string
		origin requests.ApprovalOrigin
	}{
		{name: "unspecified", origin: requests.ApprovalOriginUnspecified},
		{name: "unknown", origin: requests.ApprovalOrigin("future")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend, logs := approve(t, tc.origin)

			if len(backend.personalDeliveries) != 0 {
				t.Fatalf("personal deliveries = %v, want none", backend.personalDeliveries)
			}
			if !strings.Contains(logs, "unrecognized request approval origin") {
				t.Fatalf("logs = %q, want a warning about the unrecognized origin", logs)
			}
		})
	}
}
