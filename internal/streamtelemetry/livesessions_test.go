package streamtelemetry

import (
	"testing"
	"time"
)

func TestLiveByteFactsCarryIdentityBytesAndReportedState(t *testing.T) {
	at := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	started := at.Add(-time.Hour)
	view := GlobalMonitoringView{Sessions: []GlobalSessionView{{
		SessionID: "s1", Subject: UserSubject(42), ProfileID: "p1", MediaFileID: 77,
		PlayMethods: []string{"direct"}, StartedAt: started,
		Reported: true, ReportedPaused: true, ReportedPositionSeconds: 128.5,
		ViewerBytesAccepted: 900, RelayBytesAccepted: 100, BytesDegraded: true,
		LastByteAccepted: at, OpenObservations: 2, RequestCount: 7,
		ViewerIPs: []string{"203.0.113.4", "198.51.100.9"}, RealtimeConnectionAlive: true,
		HasIdentityConflict:  true,
		Publishers:           []PublisherRef{{PublisherID: "api"}, {PublisherID: "api#reported"}},
		ViewerEdgePublishers: []PublisherRef{{PublisherID: "api"}},
	}}}

	facts := LiveByteFactsFromGlobalView(view)["s1"]
	if facts.Subject != UserSubject(42) || facts.ProfileID != "p1" || facts.MediaFileID != 77 {
		t.Fatalf("identity = %+v", facts)
	}
	if facts.PlayMethod != "direct" || !facts.StartedAt.Equal(started) {
		t.Fatalf("play method / start = %q %v", facts.PlayMethod, facts.StartedAt)
	}
	if !facts.Reported || !facts.ReportedPaused || facts.ReportedPositionSeconds != 128.5 {
		t.Fatalf("reported state dropped: %+v", facts)
	}
	if facts.ViewerBytes != 900 || facts.RelayBytes != 100 {
		t.Fatalf("bytes = viewer %d relay %d, want 900/100", facts.ViewerBytes, facts.RelayBytes)
	}
	if !facts.BytesDegraded || !facts.RealtimeAlive || !facts.IdentityConflict {
		t.Fatalf("flags dropped: %+v", facts)
	}
	if len(facts.ViewerIPs) != 2 || facts.ViewerIPs[0] != "198.51.100.9" {
		t.Fatalf("viewer IPs = %v, want both, sorted", facts.ViewerIPs)
	}
	if len(facts.Publishers) != 2 || len(facts.ViewerEdgePublishers) != 1 {
		t.Fatalf("publishers = %v / %v", facts.Publishers, facts.ViewerEdgePublishers)
	}
}

// A play method is only carried when publishers agree, so a disagreement reads
// as "unknown" rather than as one publisher's answer.
func TestLiveByteFactsOmitDisputedPlayMethod(t *testing.T) {
	view := GlobalMonitoringView{Sessions: []GlobalSessionView{{
		SessionID: "s1", PlayMethods: []string{"direct", "transcode"},
	}}}
	if got := LiveByteFactsFromGlobalView(view)["s1"].PlayMethod; got != "" {
		t.Fatalf("play method = %q, want empty when publishers disagree", got)
	}
}

func TestLiveByteFactsViewerRecencyIgnoresRelayRoutes(t *testing.T) {
	viewerAt := time.Unix(100, 0)
	relayAt := time.Unix(200, 0)
	view := GlobalMonitoringView{Sessions: []GlobalSessionView{
		{SessionID: "mixed", LastByteAccepted: relayAt, OpenObservations: 5, MeasurementPruned: true, Routes: []RouteActivityView{
			{Role: RoleViewerEgress, Open: 2, LastByteAccepted: viewerAt},
			{Role: RoleInternalRelay, Open: 3, LastByteAccepted: relayAt},
		}},
		{SessionID: "relay-only", LastByteAccepted: relayAt, OpenObservations: 3, Routes: []RouteActivityView{
			{Role: RoleInternalRelay, Open: 3, LastByteAccepted: relayAt},
		}},
	}}
	facts := LiveByteFactsFromGlobalView(view)
	if got := facts["mixed"]; !got.ViewerLastByteAt.Equal(viewerAt) || got.ViewerOpenObservations != 2 || !got.MeasurementPruned {
		t.Fatalf("mixed viewer recency = %+v", got)
	}
	if got := facts["relay-only"]; !got.ViewerLastByteAt.IsZero() || got.ViewerOpenObservations != 0 || !got.LastByteAt.Equal(relayAt) {
		t.Fatalf("relay-only viewer recency = %+v", got)
	}
}

// The merged view carries a byte level, not a rate. The rate is the delta between
// two builds, so it cannot exist on the first sighting of a session.
func TestUpdateRatesNeedsTwoSightings(t *testing.T) {
	cache := &ViewCache{}
	base := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	view := GlobalMonitoringView{Sessions: []GlobalSessionView{{SessionID: "s1", ViewerBytesAccepted: 1000}}}

	cache.updateRatesLocked(view, base)
	if cache.rates["s1"].known {
		t.Fatal("rate known after a single sighting; there is no interval to divide by")
	}

	// 125 000 bytes over 10s is 1 Mbit/s == 100 kbps in this unit.
	view.Sessions[0].ViewerBytesAccepted = 1000 + 125_000
	cache.updateRatesLocked(view, base.Add(10*time.Second))
	sample := cache.rates["s1"]
	if !sample.known {
		t.Fatal("rate still unknown after a second sighting")
	}
	if got := sample.kbps; got < 99.9 || got > 100.1 {
		t.Fatalf("rate = %f kbps, want ~100", got)
	}
}

// Two builds a few milliseconds apart would divide a small delta by a near-zero
// interval. The previous answer is carried instead of reporting a spike.
func TestUpdateRatesCarriesPreviousSampleWhenBuildsAreTooClose(t *testing.T) {
	cache := &ViewCache{}
	base := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	view := GlobalMonitoringView{Sessions: []GlobalSessionView{{SessionID: "s1", ViewerBytesAccepted: 0}}}
	cache.updateRatesLocked(view, base)
	view.Sessions[0].ViewerBytesAccepted = 125_000
	cache.updateRatesLocked(view, base.Add(10*time.Second))
	settled := cache.rates["s1"]

	view.Sessions[0].ViewerBytesAccepted = 125_100
	cache.updateRatesLocked(view, base.Add(10*time.Second+50*time.Millisecond))
	if got := cache.rates["s1"]; got.kbps != settled.kbps || !got.known {
		t.Fatalf("rate = %+v, want the previous sample %+v carried", got, settled)
	}
}

// Bytes are monotonic per session; a decrease means the session was re-keyed or a
// publisher dropped out. Restarting beats reporting a negative rate.
func TestUpdateRatesRestartsOnByteDecrease(t *testing.T) {
	cache := &ViewCache{}
	base := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	view := GlobalMonitoringView{Sessions: []GlobalSessionView{{SessionID: "s1", ViewerBytesAccepted: 500_000}}}
	cache.updateRatesLocked(view, base)
	view.Sessions[0].ViewerBytesAccepted = 1000
	cache.updateRatesLocked(view, base.Add(10*time.Second))
	if sample := cache.rates["s1"]; sample.known {
		t.Fatalf("rate = %+v, want unknown rather than negative", sample)
	}
}

// The table is rebuilt from each view rather than pruned, so it cannot retain a
// session that has ended or outgrow the merged-session cap.
func TestUpdateRatesDropsSessionsAbsentFromTheNewView(t *testing.T) {
	cache := &ViewCache{}
	base := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	cache.updateRatesLocked(GlobalMonitoringView{Sessions: []GlobalSessionView{
		{SessionID: "gone"}, {SessionID: "stays"},
	}}, base)
	cache.updateRatesLocked(GlobalMonitoringView{Sessions: []GlobalSessionView{
		{SessionID: "stays"},
	}}, base.Add(10*time.Second))

	if _, ok := cache.rates["gone"]; ok {
		t.Fatal("ended session still tracked")
	}
	if _, ok := cache.rates["stays"]; !ok {
		t.Fatal("live session dropped")
	}
}

// The rate key fingerprints where the bytes came from, and a reporting publisher
// is not one of those places: ViewerBytesAccepted sums viewer-egress routes only,
// so the session manager joining or leaving changes nothing about the total it is
// a delta of. Keying on every contributor blanked the delivery rate on a
// perfectly healthy stream the moment its session manager started reporting it,
// and the dashboard rendered that as "not yet known" on a session it had been
// measuring for hours.
func TestUpdateRatesSurvivesAReportingPublisherJoining(t *testing.T) {
	cache := &ViewCache{}
	base := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	edge := []PublisherRef{{PublisherID: "api"}}
	view := GlobalMonitoringView{Sessions: []GlobalSessionView{{
		SessionID: "s1", ViewerBytesAccepted: 0,
		Publishers: edge, ViewerEdgePublishers: edge,
	}}}
	cache.updateRatesLocked(view, base)

	// Same viewer edge, same bytes, one extra publisher that delivered none of
	// them. 125 000 bytes over 10s is 1 Mbit/s == 100 kbps in this unit.
	view.Sessions[0].ViewerBytesAccepted = 125_000
	view.Sessions[0].Publishers = []PublisherRef{{PublisherID: "api"}, {PublisherID: "api#reported"}}
	cache.updateRatesLocked(view, base.Add(10*time.Second))

	sample := cache.rates["s1"]
	if !sample.known {
		t.Fatal("rate reset by a publisher that contributed no viewer bytes")
	}
	if got := sample.kbps; got < 99.9 || got > 100.1 {
		t.Fatalf("rate = %f kbps, want ~100", got)
	}

	// A change to the viewer edge itself still resets: those totals genuinely are
	// summed over different publishers, so the level jump is not this interval's
	// traffic.
	view.Sessions[0].ViewerBytesAccepted = 250_000
	view.Sessions[0].ViewerEdgePublishers = []PublisherRef{{PublisherID: "api"}, {PublisherID: "node-2"}}
	cache.updateRatesLocked(view, base.Add(20*time.Second))
	if cache.rates["s1"].known {
		t.Fatal("rate survived a viewer-edge publisher joining; the two totals are not comparable")
	}
}
