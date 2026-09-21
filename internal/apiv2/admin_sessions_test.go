package apiv2

import (
	"cmp"
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
)

type fakeAdminPlaybackSessions struct{ calls, lastLimit int }

func (*fakeAdminPlaybackSessions) AdminPlaybackSessionsAvailable() bool { return true }
func (f *fakeAdminPlaybackSessions) ReadAdminPlaybackSessions(_ context.Context, query handlers.PlaybackSessionsQuery, after string, limit int) ([]handlers.AdminPlaybackSessionView, error) {
	f.calls++
	f.lastLimit = limit
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("offset", 3600))
	rows := []handlers.AdminPlaybackSessionView{
		{SessionID: "b", UserID: 7, ProfileID: "child", MediaFileID: 42, RequestedMediaFileID: 41, StartedAt: at, UpdatedAt: at, RoutingExecutionNodeID: new(9), RoutingNetworkProvider: new("tailscale"), TargetAudioChannels: new(2), SourceAudioChannels: new(8), EffectivePlayMethod: "transcode", IsJellyfinClient: true, HasPlaybackControl: true},
		{SessionID: "a", UserID: 7, ProfileID: "primary", MediaFileID: 44, RequestedMediaFileID: 44, StartedAt: at, UpdatedAt: at},
	}
	slices.SortFunc(rows, func(a, b handlers.AdminPlaybackSessionView) int { return cmp.Compare(a.SessionID, b.SessionID) })
	out := []handlers.AdminPlaybackSessionView{}
	for _, row := range rows {
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
func TestAdminPlaybackSessionReadProjection(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeAdminPlaybackSessions)
	deps.AdminPlaybackSessions = f
	h := NewHandler(deps)
	path := Prefix + "/admin/sessions"
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	if f.calls != 0 {
		t.Fatal("member reached loader")
	}
	rec := do(t, h, "GET", path+"?limit=1", "", bearer(adminToken))
	var page Collection[AdminPlaybackSession]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(page.Items) != 1 || page.Items[0].SessionID != "a" || page.Items[0].UserID != "7" || page.Items[0].ProfileID != "primary" || page.Page == nil || !page.Page.HasMore {
		t.Fatal(rec.Code, rec.Body.String())
	}
	cursor := url.QueryEscape(page.Page.NextCursor)
	rec = do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", bearer(adminToken))
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	row := page.Items[0]
	if rec.Code != 200 || row.SessionID != "b" || row.RoutingNetworkProvider == nil || *row.RoutingNetworkProvider != "tailscale" || row.ProfileID != "child" || row.MediaFileID != "42" || row.RequestedMediaFileID != "41" || row.RoutingExecutionNodeID == nil || *row.RoutingExecutionNodeID != "9" || *row.TargetAudioChannels != 2 || *row.SourceAudioChannels != 8 || !row.IsJellyfinClient || !row.HasPlaybackControl || page.Page.HasMore {
		t.Fatal(rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"started_at":"2026-01-02T02:04:05.123Z"`) {
		t.Fatal(rec.Body.String())
	}
	requireProblem(t, do(t, h, "GET", path+"?limit=2&cursor="+cursor, "", bearer(adminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, "GET", path+"?user_id=7&limit=1&cursor="+cursor, "", bearer(adminToken)), TypeInvalidCursor)
	requireProblem(t, do(t, h, "GET", path+"?user_id=0", "", bearer(adminToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, "GET", path+"?limit=1&cursor="+cursor, "", actingRequestAdmin), TypeInvalidCursor)
	missing := NewHandler(pilotDeps(nil, nil))
	requireProblem(t, do(t, missing, "GET", path, "", bearer(adminToken)), TypeDependencyUnavailable)
	rec = do(t, missing, "GET", path+"/capabilities", "", bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatal(rec.Body.String())
	}
}
func TestAdminNodeSessionObservations(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeAdminNodeSessions)
	deps.AdminNodeSessions = f
	h := NewHandler(deps)
	path := Prefix + "/admin/node-sessions"
	requireProblem(t, do(t, h, "GET", path, "", bearer(memberToken)), TypePermissionDenied)
	requireProblem(t, do(t, h, "GET", path+"?node_id=0", "", bearer(adminToken)), TypeValidationFailed)
	if f.calls != 0 {
		t.Fatal("invalid read reached source")
	}
	rec := do(t, h, "GET", path+"?limit=1&node_id=9", "", bearer(adminToken))
	var out AdminNodeSessionsOutput
	if err := json.Unmarshal(rec.Body.Bytes(), &out.Body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || f.node != 9 || out.Body.Undecodable != 1 || len(out.Body.Items) != 1 || !out.Body.Page.HasMore || out.Body.Items[0].AuthUserID != "7" || out.Body.Items[0].StartedAt != nil {
		t.Fatal(rec.Code, rec.Body.String())
	}
	first := out.Body.Items[0].SessionID
	cursor := url.QueryEscape(out.Body.Page.NextCursor)
	rec = do(t, h, "GET", path+"?limit=1&node_id=9&cursor="+cursor, "", bearer(adminToken))
	if err := json.Unmarshal(rec.Body.Bytes(), &out.Body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(out.Body.Items) != 1 || out.Body.Page.HasMore || out.Body.Items[0].SessionID == first {
		t.Fatal(rec.Body.String())
	}
	requireProblem(t, do(t, h, "GET", path+"?limit=1&node_id=8&cursor="+cursor, "", bearer(adminToken)), TypeInvalidCursor)
}

// The declared maximum page size must reach the loader instead of failing as an
// internal error partway down the stack.
func TestAdminPlaybackSessionsAcceptDeclaredMaximumLimit(t *testing.T) {
	deps := pilotDeps(nil, nil)
	f := new(fakeAdminPlaybackSessions)
	deps.AdminPlaybackSessions = f
	h := NewHandler(deps)

	for _, limit := range []string{"150", "200"} {
		rec := do(t, h, "GET", Prefix+"/admin/sessions?limit="+limit, "", bearer(adminToken))
		if rec.Code != 200 {
			t.Fatalf("limit=%s: %d %s", limit, rec.Code, rec.Body.String())
		}
		want, err := strconv.Atoi(limit)
		if err != nil || f.lastLimit != want {
			t.Fatalf("limit=%s reached the loader as %d", limit, f.lastLimit)
		}
	}

	// The summary operation declares its own, smaller maximum; its whole
	// declared range must stay inside the application bound too.
	rec := do(t, h, "GET", Prefix+"/admin/sessions/summary?limit=100", "", bearer(adminToken))
	if rec.Code != 200 {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body.String())
	}
	// Above the declared maximum the request is a client error, not a 500.
	requireProblem(t, do(t, h, "GET", Prefix+"/admin/sessions?limit=201", "", bearer(adminToken)), TypeValidationFailed)
}

func adminSessionFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "admin_playback_sessions", operationID: "listAdminPlaybackSessions", method: "GET", path: Prefix + "/admin/sessions", headers: bearer(adminToken), status: 200, schema: "#/components/schemas/CollectionAdminPlaybackSession", assertHeaders: []string{"Content-Type"}, scenario: "Diagnostic rows retain account/profile and chosen/requested file distinctions with canonical string IDs and instants."},
	}
}

func TestAdminSessionNetworkProviderProjection(t *testing.T) {
	for _, provider := range []*string{nil, new(""), new("tailscale")} {
		view := handlers.AdminPlaybackSessionView{RoutingNetworkProvider: provider, StartedAt: time.Now(), UpdatedAt: time.Now()}
		wire, err := json.Marshal(adminPlaybackSessionOf(view))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(wire, &fields); err != nil {
			t.Fatal(err)
		}
		raw, present := fields["routing_network_provider"]
		if present != (provider != nil) {
			t.Fatalf("wrong field presence: %s", wire)
		}
		if present {
			var got string
			if err := json.Unmarshal(raw, &got); err != nil || got != *provider {
				t.Fatalf("provider = %s", raw)
			}
		}
		legacy, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(legacy), "routing_network_provider") {
			t.Fatal("v2 field leaked into v1")
		}
	}
}
