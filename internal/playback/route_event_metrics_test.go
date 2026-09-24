package playback

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func firstFrameSampleCount(t testing.TB, client string) uint64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := firstFrameSeconds.WithLabelValues(client).(prometheus.Metric).Write(metric); err != nil {
		t.Fatal(err)
	}
	return metric.GetHistogram().GetSampleCount()
}

// TestObserveStoredRouteEventCountsOnlyUsableFirstFrames keeps the histogram
// to first_frame events with a plausible duration, labeled by client family.
func TestObserveStoredRouteEventCountsOnlyUsableFirstFrames(t *testing.T) {
	record := func(event, ms, client string) RouteEventRecordV3 {
		diagnostics := map[string]string{}
		if ms != "" {
			diagnostics["first_frame_ms"] = ms
		}
		return RouteEventRecordV3{RouteEventV3: RouteEventV3{Event: event, Diagnostics: diagnostics}, ClientName: client}
	}
	android := firstFrameSampleCount(t, "android")
	other := firstFrameSampleCount(t, "other")
	for _, ignored := range []RouteEventRecordV3{
		record(RouteEventPlanSelectedV3, "900", "Silo Android"),
		record(RouteEventFirstFrameV3, "", "Silo Android"),
		record(RouteEventFirstFrameV3, "soon", "Silo Android"),
		record(RouteEventFirstFrameV3, "-5", "Silo Android"),
		record(RouteEventFirstFrameV3, "NaN", "Silo Android"),
		record(RouteEventFirstFrameV3, "600001", "Silo Android"),
	} {
		ObserveStoredRouteEvent(ignored)
	}
	if got := firstFrameSampleCount(t, "android"); got != android {
		t.Fatalf("unusable events observed: %d samples", got-android)
	}

	ObserveStoredRouteEvent(record(RouteEventFirstFrameV3, "900", "Silo Android TV"))
	ObserveStoredRouteEvent(record(RouteEventFirstFrameV3, "0", "Silo Android"))
	ObserveStoredRouteEvent(record(RouteEventFirstFrameV3, "1200", "a private client name"))
	if got := firstFrameSampleCount(t, "android"); got != android+2 {
		t.Fatalf("android samples = %d, want 2", got-android)
	}
	if got := firstFrameSampleCount(t, "other"); got != other+1 {
		t.Fatalf("unknown client samples = %d, want 1 under other", got-other)
	}
}

// TestMemoryRecordRouteEventReportsInsert mirrors the Postgres contract: a
// repeated v2 event id inserts nothing, an id-less legacy report always does.
func TestMemoryRecordRouteEventReportsInsert(t *testing.T) {
	store := NewMemoryPlanStoreV3()
	record := RouteEventRecordV3{RouteEventV3: RouteEventV3{PlaybackAttemptID: "attempt-0001", Event: RouteEventFirstFrameV3}, EventID: "8b0b5b62-4d2f-4c55-9a55-1f6d8f0c9a10"}
	for i, want := range []bool{true, false} {
		inserted, err := store.RecordRouteEvent(context.Background(), record)
		if err != nil || inserted != want {
			t.Fatalf("report %d: inserted = %v, err = %v; want %v", i+1, inserted, err, want)
		}
	}
	legacy := record
	legacy.EventID = ""
	for i := range 2 {
		if inserted, err := store.RecordRouteEvent(context.Background(), legacy); err != nil || !inserted {
			t.Fatalf("legacy report %d: inserted = %v, err = %v", i+1, inserted, err)
		}
	}
}
