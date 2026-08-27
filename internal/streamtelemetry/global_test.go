package streamtelemetry

import (
	"encoding/json"
	"math"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

func globalTestParams() ViewParams {
	cfg := DefaultConfig("node")
	return ViewParams{Freshness: cfg.Freshness, MembershipTTL: cfg.MembershipTTL, MaxMergedSessions: cfg.MaxMergedSessions, MaxMergedTransfers: cfg.MaxMergedTransfers,
		MaxViewerIPsPerSession: cfg.MaxViewerIPsPerSession, MaxDeviceIDsPerSession: cfg.MaxDeviceIDsPerSession,
		MaxClientVariantsPerSession: cfg.MaxClientVariantsPerSession, MaxUserAgentsPerSession: cfg.MaxClientVariantsPerSession,
		MaxMediaFileIDsPerSession: cfg.MaxMediaFileIDsPerSession, MaxPlayMethodsPerSession: cfg.MaxPlayMethodsPerSession,
		MaxTokenIssuedAtPerSession: cfg.MaxTokenIssuedAtPerSession, MaxRoutesPerSession: cfg.MaxRoutesPerSession,
		MaxIdentityConflictsPerSession: cfg.MaxIdentityConflictsPerSession}
}

func globalSet(at time.Time, snapshots ...Snapshot) PublisherSet {
	set := PublisherSet{Snapshots: snapshots}
	for _, snapshot := range snapshots {
		set.Members = append(set.Members, Member{PublisherID: snapshot.PublisherID, LastHeartbeat: at})
	}
	return set
}

func fullCoverage() PublisherCoverage {
	return PublisherCoverage{Declared: true, ConfiguredFamilies: append([]Family(nil), AllFamilies...)}
}

func viewerRoute(bytes int64) RouteActivityView {
	return RouteActivityView{Method: "GET", Pattern: "/stream", Role: RoleViewerEgress, BytesAccepted: bytes}
}

func TestBuildGlobalViewMergeRules(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	claim := at.Add(-time.Minute)
	firstSeen := at.Add(-2 * time.Minute)
	one := Snapshot{PublisherID: "p1", NodeID: "n1", PublisherEpoch: 1, Sequence: 1, CapturedAt: at, Coverage: fullCoverage(),
		Sessions: []SessionView{{SessionID: "session", Subject: UserSubject(1), ProfileID: "profile", MediaFileID: 10, PlayMethod: "direct",
			StartedAt: firstSeen, StartedAtSource: StartedAtSourceFirstSeen, StartedAtDegraded: true, ViewerIPs: []string{"192.0.2.1"}, OpenObservations: 2, RequestCount: 3,
			Routes:       []RouteActivityView{viewerRoute(100), {Method: "GET", Pattern: "/relay", Role: RoleInternalRelay, BytesAccepted: 50}},
			MediaFileIDs: []int{10}, PlayMethods: []string{"direct"}}}}
	two := Snapshot{PublisherID: "p2", NodeID: "n2", PublisherEpoch: 2, Sequence: 2, CapturedAt: at, Coverage: fullCoverage(),
		Sessions: []SessionView{{SessionID: "session", Subject: UserSubject(1), ProfileID: "profile", MediaFileID: 10, PlayMethod: "remux",
			StartedAt: claim, StartedAtSource: StartedAtSourceClaim, ViewerIPs: []string{"192.0.2.2"}, OpenObservations: 4, RequestCount: 5,
			Routes: []RouteActivityView{viewerRoute(200)}, MediaFileIDs: []int{10, 11}, PlayMethods: []string{"remux"}}}}
	view := BuildGlobalView(globalSet(at, one, two), at, globalTestParams())
	if !view.Complete || len(view.Sessions) != 1 {
		t.Fatalf("view = %+v", view)
	}
	session := view.Sessions[0]
	if !reflect.DeepEqual(session.ViewerIPs, []string{"192.0.2.1", "192.0.2.2"}) {
		t.Fatalf("viewer IPs = %v", session.ViewerIPs)
	}
	if session.OpenObservations != 6 || session.RequestCount != 8 {
		t.Fatalf("counts = open %d requests %d", session.OpenObservations, session.RequestCount)
	}
	if session.ViewerBytesAccepted != 300 || session.RelayBytesAccepted != 50 {
		t.Fatalf("bytes = viewer %d relay %d", session.ViewerBytesAccepted, session.RelayBytesAccepted)
	}
	if session.StartedAt != claim || session.StartedAtSource != StartedAtSourceClaim {
		t.Fatalf("started = %v %s", session.StartedAt, session.StartedAtSource)
	}
	if !session.StartedAtDegraded {
		t.Fatal("degraded first_seen contributor was not carried")
	}
	if !reflect.DeepEqual(session.PlayMethods, []string{"direct", "remux"}) {
		t.Fatalf("play methods = %v", session.PlayMethods)
	}
}

func TestBuildGlobalViewRelayDoesNotSupplyIdentity(t *testing.T) {
	at := time.Now()
	snapshot := Snapshot{PublisherID: "relay", CapturedAt: at, Sessions: []SessionView{{SessionID: "s", Subject: UserSubject(9), ProfileID: "p", MediaFileID: 8,
		Routes: []RouteActivityView{{Role: RoleInternalRelay, BytesAccepted: 20}}}}}
	session := BuildGlobalView(globalSet(at, snapshot), at, globalTestParams()).Sessions[0]
	if session.Subject != (Subject{}) || session.ProfileID != "" || session.MediaFileID != 0 || session.ViewerBytesAccepted != 0 || session.RelayBytesAccepted != 20 {
		t.Fatalf("relay merge = %+v", session)
	}
}

func TestBuildGlobalViewRelayCannotClaimReportedIdentityFallback(t *testing.T) {
	at := time.Now()
	relay := Snapshot{PublisherID: "relay", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "s", Subject: UserSubject(9), ProfileID: "p", MediaFileID: 8,
		Reported: true, ReportedPaused: true, ReportedPositionSeconds: 12, ReportedAt: at,
		Routes: []RouteActivityView{{Role: RoleInternalRelay, BytesAccepted: 20}},
	}}}
	session := BuildGlobalView(globalSet(at, relay), at, globalTestParams()).Sessions[0]
	if session.Reported || session.Subject != (Subject{}) || session.ProfileID != "" || session.MediaFileID != 0 {
		t.Fatalf("relay supplied reported identity: %+v", session)
	}
}

func TestBuildGlobalViewReporterCannotClaimMeasuredProvenance(t *testing.T) {
	at := time.Now()
	// Everything below the identity block is measured provenance: a session
	// manager knows what a client told it, never what left the building. The
	// fields are enumerated one by one because the guard is an allowlist — a
	// SessionView field the merge folds but the allowlist does not name is a fresh
	// way to fabricate measured-looking liveness for a session nothing was sent
	// for, and this test is what fails when the struct grows one.
	reporter := Snapshot{PublisherID: "api#reported", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "s", Subject: UserSubject(9), Reported: true, ReportedAt: at,
		MeasurementPruned: true,
		ViewerIPs:         []string{"192.0.2.1"}, BytesAccepted: 99,
		Routes:             []RouteActivityView{{Role: RoleViewerEgress, BytesAccepted: 99}},
		LastByteAccepted:   at,
		LastObservationEnd: at,
		OpenObservations:   3,
		RequestCount:       7,
		DeviceIDs:          []string{"device-1"},
		UserAgents:         []string{"Silo/1.0"},
		ClientVariants:     []ClientVariant{{Name: "Silo", Version: "1.0"}},
		MediaFileIDs:       []int{404},
		TokenIssuedAts:     []time.Time{at},
		Outcomes:           map[httpstream.StreamOutcome]int64{OutcomeUnknown: 5},
	}}}
	session := BuildGlobalView(globalSet(at, reporter), at, globalTestParams()).Sessions[0]
	if !session.Reported || session.MeasurementPruned || session.ViewerBytesAccepted != 0 || len(session.ViewerIPs) != 0 ||
		len(session.ViewerEdgePublishers) != 0 || len(session.Routes) != 0 {
		t.Fatalf("reporter supplied measured provenance: %+v", session)
	}
	// Liveness. Any one of these on its own makes a session nothing is being sent
	// for read as one that is actively being served, which is the #666 shape this
	// whole view exists to tell apart.
	if !session.LastByteAccepted.IsZero() || !session.LastObservationEnd.IsZero() ||
		session.OpenObservations != 0 || session.RequestCount != 0 {
		t.Fatalf("reporter supplied measured liveness: %+v", session)
	}
	// Client identity and per-request history, every field of which is read off an
	// observed request that a reporting publisher never sees.
	if len(session.DeviceIDs) != 0 || len(session.UserAgents) != 0 || len(session.ClientVariants) != 0 ||
		len(session.MediaFileIDs) != 0 || len(session.TokenIssuedAts) != 0 || len(session.Outcomes) != 0 {
		t.Fatalf("reporter supplied measured client identity: %+v", session)
	}
}

func TestBuildGlobalViewMergesTombstoneAsAttributedViewerMemory(t *testing.T) {
	at := time.Now()
	tombstone := Snapshot{PublisherID: "edge", NodeID: "node", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "s", MeasurementPruned: true, RoutesOverflowed: true,
		Routes: []RouteActivityView{{Role: RoleViewerEgress, BytesAccepted: 4096, LastByteAccepted: at.Add(-time.Minute)}},
	}}}
	session := BuildGlobalView(globalSet(at, tombstone), at, globalTestParams()).Sessions[0]
	if !session.MeasurementPruned || session.ViewerBytesAccepted != 4096 || !session.BytesDegraded {
		t.Fatalf("tombstone merge = %+v", session)
	}
	if len(session.ViewerEdgePublishers) != 1 || session.ViewerEdgePublishers[0].PublisherID != "edge" {
		t.Fatalf("viewer edge publishers = %+v", session.ViewerEdgePublishers)
	}
}

func TestBuildGlobalViewLiveMeasurementOverridesPrunedFlag(t *testing.T) {
	at := time.Now()
	tombstone := Snapshot{PublisherID: "old", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "s", MeasurementPruned: true, Routes: []RouteActivityView{viewerRoute(10)},
	}}}
	live := Snapshot{PublisherID: "live", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "s", Routes: []RouteActivityView{viewerRoute(20)},
	}}}
	session := BuildGlobalView(globalSet(at, live, tombstone), at, globalTestParams()).Sessions[0]
	if session.MeasurementPruned || session.ViewerBytesAccepted != 30 {
		t.Fatalf("mixed live/pruned merge = %+v", session)
	}
}

func TestBuildGlobalViewReportedAndTombstoneKeepsEdgeIdentity(t *testing.T) {
	at := time.Now()
	tombstone := Snapshot{PublisherID: "edge", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "s", Subject: UserSubject(7), ProfileID: "profile", MeasurementPruned: true,
		Routes: []RouteActivityView{viewerRoute(100)},
	}}}
	reporter := Snapshot{PublisherID: "edge#reported", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "s", Subject: UserSubject(99), ProfileID: "other", Reported: true, ReportedAt: at,
	}}}
	session := BuildGlobalView(globalSet(at, reporter, tombstone), at, globalTestParams()).Sessions[0]
	if !session.Reported || session.Subject != UserSubject(7) || session.ProfileID != "profile" {
		t.Fatalf("reported tombstone identity = %+v", session)
	}
}

func TestBuildGlobalViewTruncationPrefersLiveAndReportedSessions(t *testing.T) {
	at := time.Now()
	measuring := Snapshot{PublisherID: "edge", CapturedAt: at, Sessions: []SessionView{
		{SessionID: "a-tombstone", MeasurementPruned: true, Routes: []RouteActivityView{viewerRoute(1)}},
		{SessionID: "b-tombstone", MeasurementPruned: true, Routes: []RouteActivityView{viewerRoute(1)}},
		{SessionID: "z-live", Routes: []RouteActivityView{viewerRoute(1)}},
	}}
	reporting := Snapshot{PublisherID: "edge#reported", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "z-reported", Reported: true, ReportedAt: at,
	}}}
	params := globalTestParams()
	params.MaxMergedSessions = 2
	view := BuildGlobalView(globalSet(at, measuring, reporting), at, params)
	if len(view.Sessions) != 2 || view.Sessions[0].SessionID != "z-live" || view.Sessions[1].SessionID != "z-reported" {
		t.Fatalf("truncated sessions = %+v, want live and reported ids", view.Sessions)
	}
	if !view.Truncated {
		t.Fatal("session truncation was not reported")
	}
}

// A relay is not a viewer edge: the only address it can see is the proxy in front
// of it, not the viewer's. mergeSession unions ViewerIPs with no provenance check
// of its own, so a relay that recorded one would put an internal hop into the set
// an operator reads as "who is watching" — and an implausible count in that set is
// the abuse signal it exists for. Publishers omit it by convention today; this
// pins that the merge does not depend on the convention holding.
func TestBuildGlobalViewRelayDoesNotSupplyViewerIPs(t *testing.T) {
	at := time.Now()
	relay := Snapshot{PublisherID: "relay", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "s", ViewerIPs: []string{"10.10.10.100"}, ViewerIPsOverflowed: true,
		Routes: []RouteActivityView{{Role: RoleInternalRelay, BytesAccepted: 20}},
	}}}
	edge := Snapshot{PublisherID: "edge", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "s", ViewerIPs: []string{"203.0.113.7"},
		Routes: []RouteActivityView{viewerRoute(20)},
	}}}

	merged := BuildGlobalView(globalSet(at, relay, edge), at, globalTestParams()).Sessions[0]
	if !reflect.DeepEqual(merged.ViewerIPs, []string{"203.0.113.7"}) {
		t.Fatalf("viewer IPs = %v, want only the viewer edge's", merged.ViewerIPs)
	}
	if merged.ViewerIPsOverflowed {
		t.Fatal("a relay's overflow flag survived the address it was counting")
	}

	// With no edge present at all the relay still supplies none: an empty set is
	// the honest answer, not a licence to fall back on whatever the hop could see.
	relayOnly := BuildGlobalView(globalSet(at, relay), at, globalTestParams()).Sessions[0]
	if len(relayOnly.ViewerIPs) != 0 || relayOnly.ViewerIPsOverflowed {
		t.Fatalf("relay-only viewer IPs = %v (overflowed=%t), want none", relayOnly.ViewerIPs, relayOnly.ViewerIPsOverflowed)
	}
}

func TestBuildGlobalViewMergesRoutesWithEmptyMethod(t *testing.T) {
	at := time.Now()
	one := Snapshot{PublisherID: "p1", CapturedAt: at, Sessions: []SessionView{{SessionID: "s", Routes: []RouteActivityView{{Pattern: "/stream", Role: RoleViewerEgress, Open: 1, Requests: 2, BytesAccepted: 3}}}}}
	two := Snapshot{PublisherID: "p2", CapturedAt: at, Sessions: []SessionView{{SessionID: "s", Routes: []RouteActivityView{{Pattern: "/stream", Role: RoleViewerEgress, Open: 4, Requests: 5, BytesAccepted: 6}}}}}
	session := BuildGlobalView(globalSet(at, one, two), at, globalTestParams()).Sessions[0]
	if len(session.Routes) != 1 {
		t.Fatalf("routes = %+v", session.Routes)
	}
	route := session.Routes[0]
	if route.Open != 5 || route.Requests != 7 || route.BytesAccepted != 9 || session.ViewerBytesAccepted != 9 {
		t.Fatalf("merged route = %+v, viewer bytes = %d", route, session.ViewerBytesAccepted)
	}
}

func TestBuildGlobalViewIdentityConflicts(t *testing.T) {
	at := time.Now()
	makeSnapshot := func(publisher string, user, media int, profile string) Snapshot {
		return Snapshot{PublisherID: publisher, CapturedAt: at, Sessions: []SessionView{{SessionID: "s", Subject: UserSubject(user), ProfileID: profile, MediaFileID: media,
			MediaFileIDs: []int{media}, Routes: []RouteActivityView{viewerRoute(1)}}}}
	}
	view := BuildGlobalView(globalSet(at, makeSnapshot("p1", 1, 10, "profile"), makeSnapshot("p2", 2, 11, "")), at, globalTestParams())
	session := view.Sessions[0]
	if !session.HasIdentityConflict || session.Subject != (Subject{}) || session.MediaFileID != 0 || session.ProfileID != "profile" {
		t.Fatalf("identity merge = %+v", session)
	}
	if len(session.IdentityConflicts) != 2 || session.IdentityConflicts[0].Field != "media_file_id" || session.IdentityConflicts[1].Field != "subject" {
		t.Fatalf("conflicts = %+v", session.IdentityConflicts)
	}
	if !reflect.DeepEqual(session.MediaFileIDs, []int{10, 11}) {
		t.Fatalf("media file union = %v", session.MediaFileIDs)
	}
	if len(session.IdentityConflicts[1].Values) != 2 || len(session.IdentityConflicts[1].Values[0].Publishers) != 1 {
		t.Fatalf("attribution = %+v", session.IdentityConflicts)
	}
}

func TestBuildGlobalViewStartedAtDegradedRules(t *testing.T) {
	at := time.Now()
	snapshot := Snapshot{PublisherID: "p", CapturedAt: at, Sessions: []SessionView{{SessionID: "s", StartedAt: at.Add(-time.Minute), StartedAtSource: StartedAtSourceFirstSeen, Routes: []RouteActivityView{viewerRoute(0)}}}}
	if !BuildGlobalView(globalSet(at, snapshot), at, globalTestParams()).Sessions[0].StartedAtDegraded {
		t.Fatal("first_seen winner not degraded")
	}
}

func TestBuildGlobalViewStartAuthorityComesFromViewerEdges(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	proxyStart := at.Add(-time.Minute)
	nodeStart := at.Add(-30 * time.Second)
	proxy := Snapshot{PublisherID: "proxy", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "session", StartedAt: proxyStart, StartedAtSource: StartedAtSourceClaim,
		Routes: []RouteActivityView{viewerRoute(10)},
	}}}
	node := Snapshot{PublisherID: "node", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "session", StartedAt: nodeStart, StartedAtSource: StartedAtSourceFirstSeen, StartedAtDegraded: true,
		Routes: []RouteActivityView{{Role: RoleInternalRelay, BytesAccepted: 5}},
	}}}
	session := BuildGlobalView(globalSet(at, proxy, node), at, globalTestParams()).Sessions[0]
	if !session.StartedAt.Equal(proxyStart) || session.StartedAtSource != StartedAtSourceClaim || session.StartedAtDegraded {
		t.Fatalf("viewer-edge start authority = %+v", session)
	}

	nodeOnly := BuildGlobalView(globalSet(at, node), at, globalTestParams()).Sessions[0]
	if !nodeOnly.StartedAt.Equal(nodeStart) || !nodeOnly.StartedAtDegraded {
		t.Fatalf("node-only fallback = %+v", nodeOnly)
	}

	otherProxy := proxy
	otherProxy.PublisherID = "proxy-2"
	otherProxy.Sessions = []SessionView{{SessionID: "session", StartedAt: proxyStart.Add(time.Second), StartedAtSource: StartedAtSourceClaim, Routes: []RouteActivityView{viewerRoute(1)}}}
	conflicted := BuildGlobalView(globalSet(at, proxy, otherProxy), at, globalTestParams()).Sessions[0]
	if !conflicted.StartedAtDegraded {
		t.Fatalf("equal-rank viewer-edge disagreement was not degraded: %+v", conflicted)
	}
}

func TestBuildGlobalViewMergesSeparateViewerAndRelayPublishers(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	started := at.Add(-time.Minute)
	proxy := Snapshot{PublisherID: "proxy", NodeID: "proxy-node", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "session", Subject: UserSubject(7), ProfileID: "profile", MediaFileID: 42,
		StartedAt: started, StartedAtSource: StartedAtSourceClaim,
		Routes: []RouteActivityView{{Method: http.MethodGet, Pattern: "/stream", Role: RoleViewerEgress, BytesAccepted: 100}},
	}}}
	node := Snapshot{PublisherID: "node", NodeID: "transcode-node", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "session", StartedAt: at.Add(-30 * time.Second), StartedAtSource: StartedAtSourceFirstSeen, StartedAtDegraded: true,
		Routes: []RouteActivityView{{Method: http.MethodGet, Pattern: "/segment", Role: RoleInternalRelay, BytesAccepted: 40}},
	}}}
	view := BuildGlobalView(globalSet(at, proxy, node), at, globalTestParams())
	if len(view.Sessions) != 1 {
		t.Fatalf("sessions = %+v", view.Sessions)
	}
	session := view.Sessions[0]
	if session.ViewerBytesAccepted != 100 || session.RelayBytesAccepted != 40 {
		t.Fatalf("bytes = viewer %d relay %d", session.ViewerBytesAccepted, session.RelayBytesAccepted)
	}
	if session.Subject != UserSubject(7) || session.ProfileID != "profile" || session.MediaFileID != 42 || session.HasIdentityConflict {
		t.Fatalf("identity = %+v", session)
	}
	if len(session.ViewerEdgePublishers) != 1 || session.ViewerEdgePublishers[0].PublisherID != "proxy" || len(session.Publishers) != 2 {
		t.Fatalf("publishers = all %+v viewer %+v", session.Publishers, session.ViewerEdgePublishers)
	}
	if !session.StartedAt.Equal(started) || session.StartedAtSource != StartedAtSourceClaim || session.StartedAtDegraded {
		t.Fatalf("started = %+v", session)
	}
}

func TestBuildGlobalViewCompleteness(t *testing.T) {
	at := time.Now()
	params := globalTestParams()
	fresh := Snapshot{PublisherID: "p", NodeID: "n", CapturedAt: at, Coverage: fullCoverage()}
	tests := []struct {
		name     string
		set      PublisherSet
		reason   string
		complete bool
	}{
		{name: "fresh", set: globalSet(at, fresh), complete: true},
		{name: "stale", set: PublisherSet{Members: []Member{{PublisherID: "p", LastHeartbeat: at}}, Snapshots: []Snapshot{{PublisherID: "p", NodeID: "n", CapturedAt: at.Add(-params.Freshness - time.Second), Coverage: fullCoverage()}}}, reason: "missing_publisher"},
		{name: "never published", set: PublisherSet{Members: []Member{{PublisherID: "p", LastHeartbeat: at}}}, reason: "missing_publisher"},
		{name: "departed", set: PublisherSet{Members: []Member{{PublisherID: "p", LastHeartbeat: at.Add(-params.MembershipTTL - time.Second)}}}, complete: true},
		{name: "publisher truncated", set: globalSet(at, func() Snapshot { value := fresh; value.Truncated = true; return value }()), reason: "publisher_truncated"},
		{name: "reader truncated", set: PublisherSet{Members: globalSet(at, fresh).Members, Snapshots: []Snapshot{fresh}, Truncated: true}, reason: "truncated"},
		{name: "decode errors", set: PublisherSet{Members: globalSet(at, fresh).Members, Snapshots: []Snapshot{fresh}, Errors: []PublisherError{{PublisherID: "p", DecodeErrors: 1, Reason: "decode"}}}, reason: "decode_errors"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := BuildGlobalView(test.set, at, params)
			if view.Complete != test.complete {
				t.Fatalf("complete = %v, reasons %v", view.Complete, view.IncompleteReasons)
			}
			if test.reason != "" && !slices.Contains(view.IncompleteReasons, test.reason) {
				t.Fatalf("reasons = %v", view.IncompleteReasons)
			}
			if (test.name == "stale" || test.name == "never published") && len(view.MissingPublishers) != 1 {
				t.Fatalf("missing = %+v", view.MissingPublishers)
			}
		})
	}
}

func TestBuildGlobalViewDistinguishesEmptyReporterFromMissingReporter(t *testing.T) {
	at := time.Now()
	measuring := Snapshot{PublisherID: "api", NodeID: "node", CapturedAt: at,
		ReportingPublisherID: "api#reported", Coverage: fullCoverage()}
	reporting := Snapshot{PublisherID: "api#reported", NodeID: "node", CapturedAt: at,
		Coverage: PublisherCoverage{Declared: true}}

	complete := BuildGlobalView(globalSet(at, measuring, reporting), at, globalTestParams())
	if !complete.Complete || len(complete.MissingPublishers) != 0 {
		t.Fatalf("empty reporter treated as missing: complete=%v reasons=%v missing=%+v",
			complete.Complete, complete.IncompleteReasons, complete.MissingPublishers)
	}

	missing := BuildGlobalView(globalSet(at, measuring), at, globalTestParams())
	if missing.Complete || !slices.Contains(missing.IncompleteReasons, "missing_reported_publisher") {
		t.Fatalf("absent reporter not detected: complete=%v reasons=%v", missing.Complete, missing.IncompleteReasons)
	}
	if len(missing.MissingPublishers) != 1 || missing.MissingPublishers[0].PublisherID != "api#reported" {
		t.Fatalf("missing publishers = %+v, want the declared reporter once", missing.MissingPublishers)
	}

	noReporter := measuring
	noReporter.ReportingPublisherID = ""
	explicitNone := BuildGlobalView(globalSet(at, noReporter), at, globalTestParams())
	if !explicitNone.Complete || slices.Contains(explicitNone.IncompleteReasons, "missing_reported_publisher") {
		t.Fatalf("declared no-reporter process = complete %v reasons %v", explicitNone.Complete, explicitNone.IncompleteReasons)
	}

	undeclared := noReporter
	undeclared.Coverage = PublisherCoverage{}
	unknown := BuildGlobalView(globalSet(at, undeclared), at, globalTestParams())
	if unknown.Complete || !slices.Contains(unknown.IncompleteReasons, "unknown_publisher_coverage") ||
		slices.Contains(unknown.IncompleteReasons, "missing_reported_publisher") || len(unknown.MissingPublishers) != 0 {
		t.Fatalf("undeclared coverage = complete %v reasons %v missing %+v", unknown.Complete, unknown.IncompleteReasons, unknown.MissingPublishers)
	}
}

func TestBuildGlobalViewDoesNotDuplicateStaleDeclaredReporter(t *testing.T) {
	at := time.Now()
	params := globalTestParams()
	measuring := Snapshot{PublisherID: "api", NodeID: "node", CapturedAt: at,
		ReportingPublisherID: "api#reported", Coverage: fullCoverage()}
	reporting := Snapshot{PublisherID: "api#reported", NodeID: "node",
		CapturedAt: at.Add(-params.Freshness - time.Second), Coverage: PublisherCoverage{Declared: true}}
	view := BuildGlobalView(globalSet(at, measuring, reporting), at, params)

	if len(view.MissingPublishers) != 1 || view.MissingPublishers[0].PublisherID != "api#reported" {
		t.Fatalf("missing publishers = %+v, want the stale declared reporter once", view.MissingPublishers)
	}
	if !slices.Contains(view.IncompleteReasons, "missing_reported_publisher") {
		t.Fatalf("reasons = %v, want missing_reported_publisher", view.IncompleteReasons)
	}
}

func TestBuildGlobalViewUndeclaredCoverageIsIncomplete(t *testing.T) {
	at := time.Now()
	view := BuildGlobalView(globalSet(at, Snapshot{PublisherID: "old", CapturedAt: at}), at, globalTestParams())
	if view.Complete || !slices.Contains(view.IncompleteReasons, "unknown_publisher_coverage") ||
		slices.Contains(view.IncompleteReasons, "missing_reported_publisher") || len(view.MissingPublishers) != 0 {
		t.Fatalf("view = complete %v reasons %v missing %+v", view.Complete, view.IncompleteReasons, view.MissingPublishers)
	}
}

func TestBuildGlobalViewFullyDeclaredFleetIsComplete(t *testing.T) {
	at := time.Now()
	measuring := Snapshot{PublisherID: "api", NodeID: "node", CapturedAt: at, Coverage: fullCoverage(), ReportingPublisherID: "api#reported"}
	reporting := Snapshot{PublisherID: "api#reported", NodeID: "node", CapturedAt: at, Coverage: PublisherCoverage{Declared: true}}
	view := BuildGlobalView(globalSet(at, measuring, reporting), at, globalTestParams())
	if !view.Complete || len(view.IncompleteReasons) != 0 {
		t.Fatalf("fully declared fleet = complete %v reasons %v", view.Complete, view.IncompleteReasons)
	}
}

func TestBuildGlobalViewNarrowedFamiliesIsIncomplete(t *testing.T) {
	at := time.Now()
	for _, test := range []struct {
		name     string
		families []Family
	}{
		{name: "missing one", families: append([]Family(nil), AllFamilies[:len(AllFamilies)-1]...)},
		{name: "empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := BuildGlobalView(globalSet(at, Snapshot{PublisherID: "api", CapturedAt: at,
				Coverage: PublisherCoverage{Declared: true, ConfiguredFamilies: test.families}}), at, globalTestParams())
			if view.Complete || !slices.Contains(view.IncompleteReasons, "partial_family_observation") {
				t.Fatalf("view = complete %v reasons %v", view.Complete, view.IncompleteReasons)
			}
		})
	}
}

func TestBuildGlobalViewReportingPublisherNeedsNoFamilies(t *testing.T) {
	at := time.Now()
	empty := PublisherCoverage{Declared: true}
	reporting := BuildGlobalView(globalSet(at, Snapshot{PublisherID: "api#reported", CapturedAt: at, Coverage: empty}), at, globalTestParams())
	if !reporting.Complete || slices.Contains(reporting.IncompleteReasons, "partial_family_observation") {
		t.Fatalf("reporting view = complete %v reasons %v", reporting.Complete, reporting.IncompleteReasons)
	}
	measuring := BuildGlobalView(globalSet(at, Snapshot{PublisherID: "api", CapturedAt: at, Coverage: empty}), at, globalTestParams())
	if measuring.Complete || !slices.Contains(measuring.IncompleteReasons, "partial_family_observation") {
		t.Fatalf("measuring view = complete %v reasons %v", measuring.Complete, measuring.IncompleteReasons)
	}
}

func TestBuildGlobalViewEpochAndClockSkew(t *testing.T) {
	at := time.Now()
	one := Snapshot{PublisherID: "a", PublisherEpoch: 1, Sequence: 1, CapturedAt: at.Add(time.Second)}
	two := Snapshot{PublisherID: "b", PublisherEpoch: 2, Sequence: 2, CapturedAt: at}
	view1 := BuildGlobalView(globalSet(at, one, two), at, globalTestParams())
	view2 := BuildGlobalView(globalSet(at, two, one), at, globalTestParams())
	if view1.Epoch != view2.Epoch || view1.ClockSkewSuspected {
		t.Fatalf("epochs/skew = %q %q %v", view1.Epoch, view2.Epoch, view1.ClockSkewSuspected)
	}
	two.Sequence++
	if changed := BuildGlobalView(globalSet(at, one, two), at, globalTestParams()); changed.Epoch == view1.Epoch {
		t.Fatal("epoch did not change with sequence")
	}
	one.CapturedAt = at.Add(-2 * globalTestParams().Freshness)
	set := globalSet(at, one)
	set.Members[0].LastHeartbeat = at.Add(2 * globalTestParams().Freshness)
	if !BuildGlobalView(set, at, globalTestParams()).ClockSkewSuspected {
		t.Fatal("far-future heartbeat did not flag skew")
	}
}

func TestBuildGlobalViewBoundsTransfersAndSaturation(t *testing.T) {
	at := time.Now()
	params := globalTestParams()
	params.MaxViewerIPsPerSession = 1
	one := Snapshot{PublisherID: "a", CapturedAt: at, DroppedBytes: math.MaxInt64, Sessions: []SessionView{{SessionID: "s", ViewerIPs: []string{"b"}, RequestCount: math.MaxInt64, Routes: []RouteActivityView{{Role: RoleViewerEgress, BytesAccepted: math.MaxInt64}}}}, Transfers: []TransferView{{ID: "same"}}}
	two := Snapshot{PublisherID: "b", CapturedAt: at, DroppedBytes: 1, Sessions: []SessionView{{SessionID: "s", ViewerIPs: []string{"a"}, RequestCount: 1, Routes: []RouteActivityView{{Role: RoleViewerEgress, BytesAccepted: 1}}}}, Transfers: []TransferView{{ID: "same"}}}
	view := BuildGlobalView(globalSet(at, one, two), at, params)
	if view.DroppedBytes != math.MaxInt64 || view.Sessions[0].RequestCount != math.MaxInt64 || view.Sessions[0].ViewerBytesAccepted != math.MaxInt64 {
		t.Fatalf("sums wrapped: %+v", view)
	}
	if !view.Sessions[0].ViewerIPsOverflowed || !reflect.DeepEqual(view.Sessions[0].ViewerIPs, []string{"a"}) {
		t.Fatalf("bounded viewer IPs = %v overflow=%v", view.Sessions[0].ViewerIPs, view.Sessions[0].ViewerIPsOverflowed)
	}
	if len(view.Transfers) != 2 || view.Transfers[0].Publisher.PublisherID == view.Transfers[1].Publisher.PublisherID {
		t.Fatalf("transfers = %+v", view.Transfers)
	}
}

func TestBuildGlobalViewWholeViewPermutationInvariant(t *testing.T) {
	at := time.Now()
	one := Snapshot{PublisherID: "b", PublisherEpoch: 2, Sequence: 3, CapturedAt: at, Sessions: []SessionView{
		{SessionID: "z", ViewerIPs: []string{"2", "1"}, Routes: []RouteActivityView{{Method: "POST", Pattern: "/b", Role: RoleInternalRelay}, viewerRoute(2)}},
		{SessionID: "a", DeviceIDs: []string{"d2", "d1"}, Routes: []RouteActivityView{viewerRoute(1)}},
	}}
	two := Snapshot{PublisherID: "a", PublisherEpoch: 1, Sequence: 4, CapturedAt: at, Sessions: []SessionView{{SessionID: "z", UserAgents: []string{"z", "a"}, Routes: []RouteActivityView{viewerRoute(3)}}}}
	left := BuildGlobalView(globalSet(at, one, two), at, globalTestParams())
	slices.Reverse(one.Sessions)
	slices.Reverse(one.Sessions[1].Routes)
	slices.Reverse(one.Sessions[1].ViewerIPs)
	slices.Reverse(two.Sessions[0].UserAgents)
	right := BuildGlobalView(globalSet(at, two, one), at, globalTestParams())
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	if string(leftJSON) != string(rightJSON) {
		t.Fatalf("permutation changed view\nleft: %s\nright:%s", leftJSON, rightJSON)
	}
}

// The session manager knows when playback began; the edge only knows when it
// first saw bytes. StartedAtSourceSession outranks first_seen precisely to say
// so, but a reporting publisher is never a viewer edge, so the merge has to let
// it supply the instant or the authoritative rung never applies to a session
// that is actually streaming.
func TestBuildGlobalViewReportedStartedAtOutranksTheEdge(t *testing.T) {
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	trueStart := at.Add(-10 * time.Minute)
	firstSeen := at.Add(-9 * time.Minute)

	edge := Snapshot{PublisherID: "api-1", CapturedAt: at, Sessions: []SessionView{{
		SessionID: "s1", Subject: UserSubject(7),
		StartedAt: firstSeen, StartedAtSource: StartedAtSourceFirstSeen, StartedAtDegraded: true,
		BytesAccepted: 4096,
		Routes: []RouteActivityView{{
			Method: "GET", Pattern: "/x", Role: RoleViewerEgress, BytesAccepted: 4096,
		}},
	}}}
	reporter := Snapshot{PublisherID: ReportedPublisherIDFor("api-1"), CapturedAt: at, Sessions: []SessionView{{
		SessionID: "s1", Subject: UserSubject(7),
		StartedAt: trueStart, StartedAtSource: StartedAtSourceSession,
		Reported: true, ReportedAt: at,
	}}}

	view := BuildGlobalView(PublisherSet{
		Members: []Member{
			{PublisherID: "api-1", LastHeartbeat: at},
			{PublisherID: ReportedPublisherIDFor("api-1"), LastHeartbeat: at},
		},
		Snapshots: []Snapshot{edge, reporter},
	}, at, ViewParams{Freshness: 5 * time.Second, MaxMergedSessions: 100, MaxMergedTransfers: 100})

	if len(view.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want one merged session", view.Sessions)
	}
	session := view.Sessions[0]
	if !session.StartedAt.Equal(trueStart) || session.StartedAtSource != StartedAtSourceSession {
		t.Fatalf("started at = %v from %q, want %v from %q",
			session.StartedAt.UTC(), session.StartedAtSource, trueStart, StartedAtSourceSession)
	}
	// StartedAtDegraded stays true, and that is main's settled convention rather
	// than an oversight: the flag means "someone contributing to this row was
	// guessing", which the edge's first_seen was. What this test pins is that the
	// value SERVED is the authoritative one, not the guess.
	if !session.StartedAtDegraded {
		t.Fatal("a degraded contributor stopped being carried onto the row")
	}
	// The edge is still the only one that served bytes.
	if len(session.ViewerEdgePublishers) != 1 || session.ViewerEdgePublishers[0].PublisherID != "api-1" {
		t.Fatalf("viewer edge publishers = %+v, want only the measuring publisher", session.ViewerEdgePublishers)
	}
}

// Two processes reporting one session — the shape a session that moved between
// them takes — disagree about the start. The earlier instant is kept and the
// row says it is degraded, rather than one process silently winning.
func TestBuildGlobalViewTwoReportersDisagreeingOnStartIsDegraded(t *testing.T) {
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	earlier, later := at.Add(-10*time.Minute), at.Add(-8*time.Minute)

	reporterOf := func(id string, startedAt time.Time) Snapshot {
		return Snapshot{PublisherID: ReportedPublisherIDFor(id), CapturedAt: at, Sessions: []SessionView{{
			SessionID: "s1", Subject: UserSubject(7),
			StartedAt: startedAt, StartedAtSource: StartedAtSourceSession,
			Reported: true, ReportedAt: at,
		}}}
	}

	view := BuildGlobalView(PublisherSet{
		Members: []Member{
			{PublisherID: ReportedPublisherIDFor("api-1"), LastHeartbeat: at},
			{PublisherID: ReportedPublisherIDFor("api-2"), LastHeartbeat: at},
		},
		Snapshots: []Snapshot{reporterOf("api-1", earlier), reporterOf("api-2", later)},
	}, at, ViewParams{Freshness: 5 * time.Second, MaxMergedSessions: 100, MaxMergedTransfers: 100})

	session := view.Sessions[0]
	if !session.StartedAt.Equal(earlier) {
		t.Fatalf("started at = %v, want the earlier %v", session.StartedAt.UTC(), earlier)
	}
	if !session.StartedAtDegraded {
		t.Fatal("two reporters disagreeing about the start was not marked degraded")
	}
}
