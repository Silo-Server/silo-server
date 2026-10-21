package handlers

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
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

// stalledDeliveryStore answers the plan delivery lookup only when its context
// ends, as a lookup stuck behind a loaded database would.
type stalledDeliveryStore struct{ playback.PlanStoreV3 }

func (stalledDeliveryStore) PlanDelivery(ctx context.Context, _, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// A slow delivery lookup only loses the metric's label: the summary and the
// playback sample it carries are still kept for the connection.
func TestSlowPlanDeliveryLookupKeepsTheSummaryDB(t *testing.T) {
	pool := connectionTestPool(t)
	ctx := t.Context()
	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username, role) VALUES ($1, 'user') RETURNING id`, fmt.Sprintf("slow-delivery-%d", time.Now().UnixNano())).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE id = $1`, user) })
	event := playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{Event: playback.RouteEventPlanSummaryV3, PlanID: "plan-1", Diagnostics: map[string]string{"throughput_kbps": "60000"}},
		EventID: uuid.NewString(), UserID: user, ConnectionDeviceID: "tv-1", ConnectionKey: "local", ReceivedAt: time.Now(), SampleLifetime: 7 * 24 * time.Hour}
	recordRouteEventV3(stalledDeliveryStore{playback.NewMemoryPlanStoreV3()}, event, &connectionstore.Store{Pool: pool})
	var hasSummary bool
	var sample *int
	if err := pool.QueryRow(ctx, `SELECT plan_summary IS NOT NULL, sample_kbps FROM playback_connections WHERE user_id = $1 AND device_id = 'tv-1'`, user).Scan(&hasSummary, &sample); err != nil {
		t.Fatalf("no connection row after a slow lookup: %v", err)
	}
	if !hasSummary || sample == nil || *sample != 60000 {
		t.Fatalf("after a slow lookup: summary kept %v, sample %v", hasSummary, sample)
	}
}
