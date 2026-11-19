package handlers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

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
