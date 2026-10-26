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

func TestAdminPlaybackSessionUsesNegotiatedLocation(t *testing.T) {
	row := handlers.AdminPlaybackSessionView{ClientIP: "192.168.1.8", StreamLocation: "remote"}
	if got := adminPlaybackSessionOf(row).StreamLocation; got != "remote" {
		t.Fatalf("negotiated location = %q, want remote", got)
	}
	row.StreamLocation = ""
	if got := adminPlaybackSessionOf(row).StreamLocation; got != "local" {
		t.Fatalf("legacy location = %q, want local", got)
	}
}

func (*fakeAdminPlaybackSessions) AdminPlaybackSessionsAvailable() bool { return true }
func (f *fakeAdminPlaybackSessions) ReadAdminPlaybackSessions(_ context.Context, query handlers.PlaybackSessionsQuery, after string, limit int) ([]handlers.AdminPlaybackSessionView, error) {
	f.calls++
	f.lastLimit = limit
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("offset", 3600))
	rows := []handlers.AdminPlaybackSessionView{
		{SessionID: "b", UserID: 7, ProfileID: "child", MediaFileID: 42, RequestedMediaFileID: 41, StartedAt: at, UpdatedAt: at, RoutingExecutionNodeID: new(9), RoutingNetworkProvider: new("tailscale"), TargetAudioChannels: new(2), SourceAudioChannels: new(8), EffectivePlayMethod: "transcode", IsJellyfinClient: true, HasPlaybackControl: true},
		{SessionID: "a", UserID: 7, ProfileID: "primary", MediaFileID: 44, RequestedMediaFileID: 44, ClientIP: "127.0.0.1", StartedAt: at, UpdatedAt: at},
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

// The native API names copied video with converted audio Direct Stream and
// carries the transport's output format; the frozen bridge keeps its alpha shape.
func TestAdminSessionOutputFormatProjection(t *testing.T) {
	view := handlers.AdminPlaybackSessionView{EffectivePlayMethod: "audio", OutputContainer: "fmp4", OutputProtocol: "hls", StartedAt: time.Now(), UpdatedAt: time.Now()}
	if row := adminPlaybackSessionOf(view); row.EffectivePlayMethod != directStreamPlayMethod || row.OutputContainer != "fmp4" || row.OutputProtocol != "hls" {
		t.Fatalf("admin projection = %+v", row)
	}
	if got := playbackSessionOf(view).EffectivePlayMethod; got != directStreamPlayMethod {
		t.Fatalf("household effective_play_method = %q", got)
	}
	legacy, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(legacy), `"effective_play_method":"audio"`) || strings.Contains(string(legacy), "output_") {
		t.Fatalf("bridge payload changed: %s", legacy)
	}

	rec := do(t, NewHandler(pilotDeps(nil, nil)), "GET", Prefix+"/admin/sessions/capabilities", "", bearer(adminToken))
	var caps AdminPlaybackSessionCapabilitiesOutputBody
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil || rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if !caps.OutputFormat || !caps.StreamLocation || !slices.Contains(caps.EffectivePlayMethodValues, directStreamPlayMethod) || slices.Contains(caps.EffectivePlayMethodValues, "audio") {
		t.Fatalf("capabilities = %+v", caps)
	}
}
