package apiv2

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

type fakeAdminPlaybackSessions struct {
	calls, lastLimit int
	// nodeServed names a session a proxy node delivers to the viewer.
	nodeServed string
}

func (*fakeAdminPlaybackSessions) AdminPlaybackSessionsAvailable() bool { return true }
func (f *fakeAdminPlaybackSessions) ReadAdminPlaybackSessions(_ context.Context, query handlers.PlaybackSessionsQuery, after string, limit int) ([]handlers.AdminPlaybackSessionView, error) {
	f.calls++
	f.lastLimit = limit
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("offset", 3600))
	rows := []handlers.AdminPlaybackSessionView{
		{SessionID: "b", UserID: 7, ProfileID: "child", MediaFileID: 42, RequestedMediaFileID: 41, StartedAt: at, UpdatedAt: at, RoutingExecutionNodeID: new(9), RoutingNetworkProvider: new("tailscale"), TargetAudioChannels: new(2), SourceAudioChannels: new(8), EffectivePlayMethod: "transcode", IsJellyfinClient: true, HasPlaybackControl: true},
		{SessionID: "a", UserID: 7, ProfileID: "primary", MediaFileID: 44, RequestedMediaFileID: 44, ClientIP: "127.0.0.1", StartedAt: at, UpdatedAt: at,
			Subtitle: &playback.SessionSubtitle{TrackID: "file:44:subtitle:2", Language: "en", Codec: "subrip", Format: "vtt", Source: "external", Delivery: "sidecar", HearingImpaired: true}},
	}
	slices.SortFunc(rows, func(a, b handlers.AdminPlaybackSessionView) int { return cmp.Compare(a.SessionID, b.SessionID) })
	out := []handlers.AdminPlaybackSessionView{}
	for _, row := range rows {
		if row.SessionID == f.nodeServed {
			row.RoutingEgress, row.RoutingEgressNodeID = "proxy", new(5)
		}
		if row.SessionID > after && (query.UserID == 0 || row.UserID == query.UserID) {
			out = append(out, row)
		}
	}
	return out[:min(len(out), limit+1)], nil
}

func (f *fakeAdminPlaybackSessions) ReadAdminPlaybackSummary(ctx context.Context, query handlers.PlaybackSessionsQuery, limit int) ([]handlers.AdminPlaybackSessionView, int, error) {
	rows, err := f.ReadAdminPlaybackSessions(ctx, query, "", 100)
	for i := range rows {
		rows[i].ClientIP = "192.0.2.1"
		rows[i].ClientUserAgent = "private-agent"
		rows[i].ProfileName = "Private profile"
	}
	return rows[:min(len(rows), limit)], len(rows), err
}

type fakeAdminNodeSessions struct{ calls, node int }

func (*fakeAdminNodeSessions) Available() bool { return true }
func (f *fakeAdminNodeSessions) Read(_ context.Context, node int) (nodesessions.ListResult, error) {
	f.calls++
	f.node = node
	return nodesessions.ListResult{Undecodable: 1, Sessions: []nodesessions.SessionInfo{
		{NodeURL: "https://node.invalid", SessionID: "second", AuthUserID: 7, MediaFileID: 42},
		{NodeURL: "https://node.invalid", SessionID: "first", AuthUserID: 7, MediaFileID: 42},
	}}, nil
}

func adminSessionFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_playback_sessions", operationID: "listAdminPlaybackSessions", method: "GET", path: Prefix + "/admin/sessions", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/CollectionAdminPlaybackSession", assertHeaders: []string{"Content-Type"}, scenario: "Diagnostic rows retain account/profile and chosen/requested file distinctions with canonical string IDs and instants."},
	}
}

// adminSessionEgressFixtureCases runs last so the request ids of every earlier
// fixture stay put.
func adminSessionEgressFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_playback_sessions_egress", operationID: "listAdminPlaybackSessions", method: "GET", path: Prefix + "/admin/sessions?include_egress=true", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/CollectionAdminPlaybackSession", assertHeaders: []string{"Content-Type"}, scenario: "With include_egress, a row carries the viewer bytes the merged stream telemetry counted for it, without the relay hop between Silo's own nodes; a session telemetry has no record of omits egress."},
	}
}

type fakeAdminSessionEgress struct {
	view   streamtelemetry.EgressView
	status streamtelemetry.ViewCacheStatus
	reads  int
}

func (f *fakeAdminSessionEgress) SessionEgress(context.Context) (streamtelemetry.EgressView, streamtelemetry.ViewCacheStatus) {
	f.reads++
	return f.view, f.status
}

var egressFixtureAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func egressReport(publisher string, epoch int64, capturedAt time.Time, session string, role streamtelemetry.Role, bytes int64) streamtelemetry.Snapshot {
	return streamtelemetry.Snapshot{PublisherID: publisher, PublisherEpoch: epoch, Sequence: 4, CapturedAt: capturedAt,
		Sessions: []streamtelemetry.SessionView{{SessionID: session, Routes: []streamtelemetry.RouteActivityView{{Method: "GET", Pattern: "/api/v2/stream/{session_id}", Role: role, BytesAccepted: bytes}}}}}
}

// mergedSessionEgress serves what the view cache would for these publisher
// reports, merged by the real BuildGlobalView.
func mergedSessionEgress(distributed bool, reports ...streamtelemetry.Snapshot) *fakeAdminSessionEgress {
	set := streamtelemetry.PublisherSet{Snapshots: reports}
	for _, report := range reports {
		set.Members = append(set.Members, streamtelemetry.Member{PublisherID: report.PublisherID, LastHeartbeat: egressFixtureAt})
	}
	view := streamtelemetry.BuildGlobalView(set, egressFixtureAt, streamtelemetry.ViewParams{Freshness: time.Minute, MembershipTTL: 2 * time.Minute})
	egress := view.Egress()
	egress.Distributed = distributed
	return &fakeAdminSessionEgress{view: egress, status: streamtelemetry.ViewCacheStatus{Available: true, RefreshedAt: egressFixtureAt}}
}

// fixtureSessionEgress merges two publishers' reports for session a, as an API
// server and a transcode node would send them: 1000 bytes to the viewer and a
// 500-byte relay hop between the two, which must not count. Session b has no
// telemetry.
func fixtureSessionEgress() *fakeAdminSessionEgress {
	return mergedSessionEgress(true,
		egressReport("api-1", 1, egressFixtureAt, "a", streamtelemetry.RoleViewerEgress, 1000),
		egressReport("transcode-1", 2, egressFixtureAt, "a", streamtelemetry.RoleInternalRelay, 500))
}

func TestAdminSessionsEgressCapability(t *testing.T) {
	for _, tc := range []struct {
		name   string
		egress AdminSessionEgressService
		want   bool
	}{
		{"telemetry wired", fixtureSessionEgress(), true},
		{"not wired", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := pilotDeps(nil, nil)
			deps.AdminPlaybackSessions = new(fakeAdminPlaybackSessions)
			deps.AdminSessionEgress = tc.egress
			rec := do(t, NewHandler(deps), "GET", Prefix+"/admin/sessions/capabilities", "", bearer(adminToken))
			var body AdminPlaybackSessionCapabilitiesOutputBody
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 {
				t.Fatal(rec.Code, rec.Body.String(), err)
			}
			if body.Egress != tc.want {
				t.Fatalf("egress = %v, want %v", body.Egress, tc.want)
			}
		})
	}
}

func TestAdminSessionsEgressOmittedWhenUnknown(t *testing.T) {
	sessions := func(f func(*streamtelemetry.EgressView, *streamtelemetry.ViewCacheStatus)) *fakeAdminSessionEgress {
		out := fixtureSessionEgress()
		f(&out.view, &out.status)
		return out
	}
	viewerEdge := func(publisher string, capturedAt time.Time, session string, bytes int64) streamtelemetry.Snapshot {
		return egressReport(publisher, 1, capturedAt, session, streamtelemetry.RoleViewerEgress, bytes)
	}
	for _, tc := range []struct {
		name       string
		query      string
		egress     *fakeAdminSessionEgress
		nodeServed string
		wantReads  int
		wantBytes  int64 // session a's bytes; 0 means omitted
	}{
		{name: "fresh view", query: "?include_egress=true", egress: fixtureSessionEgress(), wantReads: 1, wantBytes: 1000},
		{name: "rebuild in flight", query: "?include_egress=true", egress: sessions(func(_ *streamtelemetry.EgressView, s *streamtelemetry.ViewCacheStatus) { s.Stale = true }), wantReads: 1, wantBytes: 1000},
		{name: "not requested", egress: fixtureSessionEgress()},
		{name: "empty page", query: "?include_egress=true&user_id=99", egress: fixtureSessionEgress()},
		{name: "refresh failed", query: "?include_egress=true", egress: sessions(func(_ *streamtelemetry.EgressView, s *streamtelemetry.ViewCacheStatus) {
			s.Stale, s.LastError = true, "redis is down"
		}), wantReads: 1},
		{name: "not built", query: "?include_egress=true", egress: sessions(func(v *streamtelemetry.EgressView, s *streamtelemetry.ViewCacheStatus) {
			*v, *s = streamtelemetry.EgressView{}, streamtelemetry.ViewCacheStatus{}
		}), wantReads: 1},
		{name: "routes capped", query: "?include_egress=true", egress: sessions(func(v *streamtelemetry.EgressView, _ *streamtelemetry.ViewCacheStatus) {
			v.Sessions = map[string]streamtelemetry.SessionEgress{"a": {Bytes: 1000, MeasuredAt: egressFixtureAt, Series: "s", Degraded: true}}
		}), wantReads: 1},
		// A proxy that stopped reporting drops out of session b, which it
		// served; session a, on a live API server, keeps its egress.
		{name: "stale publisher elsewhere", query: "?include_egress=true", egress: mergedSessionEgress(true,
			viewerEdge("api-1", egressFixtureAt, "a", 1000), viewerEdge("proxy-2", egressFixtureAt.Add(-10*time.Minute), "b", 700)), wantReads: 1, wantBytes: 1000},
		// Local telemetry sees only this API server's bytes: a session a node
		// served would read as the API server's share of it.
		{name: "local telemetry, API-served", query: "?include_egress=true", egress: mergedSessionEgress(false, viewerEdge("api-1", egressFixtureAt, "a", 1000)), wantReads: 1, wantBytes: 1000},
		{name: "local telemetry, node-served", query: "?include_egress=true", egress: mergedSessionEgress(false, viewerEdge("api-1", egressFixtureAt, "a", 1000)), nodeServed: "a", wantReads: 1},
		{name: "shared telemetry, node-served", query: "?include_egress=true", egress: mergedSessionEgress(true, viewerEdge("proxy-1", egressFixtureAt, "a", 1000)), nodeServed: "a", wantReads: 1, wantBytes: 1000},
		// A server whose clock runs ahead would give measured_at a time that
		// later observations from the other servers can't pass.
		{name: "counting clock ahead", query: "?include_egress=true", egress: mergedSessionEgress(true,
			viewerEdge("api-1", egressFixtureAt, "a", 1000), viewerEdge("proxy-1", egressFixtureAt.Add(2*time.Minute), "a", 200)), wantReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := pilotDeps(nil, nil)
			deps.AdminPlaybackSessions = &fakeAdminPlaybackSessions{nodeServed: tc.nodeServed}
			deps.AdminSessionEgress = tc.egress
			rec := do(t, NewHandler(deps), "GET", Prefix+"/admin/sessions"+tc.query, "", bearer(adminToken))
			var body struct {
				Items []struct {
					SessionID string           `json:"session_id"`
					Egress    *json.RawMessage `json:"egress"`
				} `json:"items"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 {
				t.Fatal(rec.Code, rec.Body.String(), err)
			}
			if tc.egress.reads != tc.wantReads {
				t.Fatalf("telemetry reads = %d, want %d", tc.egress.reads, tc.wantReads)
			}
			for _, item := range body.Items {
				var egress AdminPlaybackSessionEgress
				if item.Egress != nil {
					if err := json.Unmarshal(*item.Egress, &egress); err != nil {
						t.Fatal(err)
					}
				}
				switch {
				case item.SessionID == "a" && tc.wantBytes != 0:
					if item.Egress == nil || egress.Bytes != tc.wantBytes || egress.Series == "" || egress.MeasuredAt.IsZero() {
						t.Fatalf("session a egress = %s", rec.Body.String())
					}
				case item.Egress != nil:
					t.Fatalf("session %s carried egress: %s", item.SessionID, rec.Body.String())
				}
			}
		})
	}
}
