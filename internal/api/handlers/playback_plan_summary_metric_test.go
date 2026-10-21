package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// deliveryLookupStore answers the optional plan delivery lookup the route
// event writer uses to label summary metrics, and signals each write it starts.
type deliveryLookupStore struct {
	playback.PlanStoreV3
	delivery string
	err      error
	writes   chan struct{}
}

func (s deliveryLookupStore) PlanDelivery(context.Context, string, string) (string, error) {
	return s.delivery, s.err
}

func (s deliveryLookupStore) RecordRouteEvent(ctx context.Context, event playback.RouteEventRecordV3) (bool, error) {
	s.writes <- struct{}{}
	return s.PlanStoreV3.RecordRouteEvent(ctx, event)
}

// stallRatioSamples reads the web series of silo_playback_stall_ratio for one
// delivery label: zero when the series does not exist yet.
func stallRatioSamples(t *testing.T, delivery string) uint64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "silo_playback_stall_ratio" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["client"] == "web" && labels["delivery"] == delivery {
				return metric.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}

// The plan lookup only labels a metric. A summary is accepted whether or not
// it answers, and a failed lookup leaves the delivery unknown.
func TestReportRouteEventV2LabelsPlanSummaryDelivery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		delivery string
		err      error
		label    string
	}{
		{"lookup answers", string(playback.DeliveryRemuxHLSV3), nil, string(playback.DeliveryRemuxHLSV3)},
		{"lookup fails", "", errors.New("plan store unavailable"), "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPlaybackServiceFixture(t)
			identity, err := f.handler.PlanStoreV3.GetAttemptIdentity(f.ctx, f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			store := deliveryLookupStore{PlanStoreV3: f.handler.PlanStoreV3, delivery: tc.delivery, err: tc.err, writes: make(chan struct{}, 2)}
			f.handler.PlanStoreV3 = store
			caller := f.caller
			caller.ClientName = "Silo Web"
			before := stallRatioSamples(t, tc.label)
			report := func(event string, diagnostics map[string]string) {
				t.Helper()
				err := f.handler.ReportRouteEventV2(f.ctx, caller, PlaybackRouteEventCommand{
					EventID: uuid.NewString(),
					Event: playback.RouteEventV3{
						ProtocolVersion:   playback.ProtocolV3,
						PlaybackAttemptID: identity.PlaybackAttemptID,
						SessionID:         f.session.ID,
						PlanID:            "plan-1",
						Event:             event,
						Diagnostics:       diagnostics,
					},
				})
				if err != nil {
					t.Fatalf("%s rejected: %v", event, err)
				}
			}
			report(playback.RouteEventPlanSummaryV3, map[string]string{"played_ms": "100000", "stall_total_ms": "6000", "buffering_mode": "always", "buffering_action": "lowered"})
			report(playback.RouteEventFirstFrameV3, map[string]string{"first_frame_ms": "1000"})
			// One consumer drains the queue in order, so once the second write
			// starts the summary ahead of it has been observed.
			for range 2 {
				select {
				case <-store.writes:
				case <-time.After(5 * time.Second):
					t.Fatal("route events were not written")
				}
			}
			if got := stallRatioSamples(t, tc.label); got != before+1 {
				t.Fatalf("stall ratio samples with delivery %q = %d, want %d", tc.label, got, before+1)
			}
		})
	}
}
