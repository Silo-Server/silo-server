package streamtelemetry

import (
	"encoding/json"
	"math"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"
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

func viewerRoute(bytes int64) RouteActivityView {
	return RouteActivityView{Method: "GET", Pattern: "/stream", Role: RoleViewerEgress, BytesAccepted: bytes}
}

func TestBuildGlobalViewMergeRules(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	claim := at.Add(-time.Minute)
	firstSeen := at.Add(-2 * time.Minute)
	one := Snapshot{PublisherID: "p1", NodeID: "n1", PublisherEpoch: 1, Sequence: 1, CapturedAt: at,
		Sessions: []SessionView{{SessionID: "session", Subject: UserSubject(1), ProfileID: "profile", MediaFileID: 10, PlayMethod: "direct",
			StartedAt: firstSeen, StartedAtSource: StartedAtSourceFirstSeen, StartedAtDegraded: true, ViewerIPs: []string{"192.0.2.1"}, OpenObservations: 2, RequestCount: 3,
			Routes:       []RouteActivityView{viewerRoute(100), {Method: "GET", Pattern: "/relay", Role: RoleInternalRelay, BytesAccepted: 50}},
			MediaFileIDs: []int{10}, PlayMethods: []string{"direct"}}}}
	two := Snapshot{PublisherID: "p2", NodeID: "n2", PublisherEpoch: 2, Sequence: 2, CapturedAt: at,
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
	fresh := Snapshot{PublisherID: "p", NodeID: "n", CapturedAt: at}
	tests := []struct {
		name     string
		set      PublisherSet
		reason   string
		complete bool
	}{
		{name: "fresh", set: globalSet(at, fresh), complete: true},
		{name: "stale", set: PublisherSet{Members: []Member{{PublisherID: "p", LastHeartbeat: at}}, Snapshots: []Snapshot{{PublisherID: "p", NodeID: "n", CapturedAt: at.Add(-params.Freshness - time.Second)}}}, reason: "missing_publisher"},
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
