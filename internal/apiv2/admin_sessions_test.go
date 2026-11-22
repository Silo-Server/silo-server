package apiv2

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type fakeAdminPlaybackSessions struct{ calls, lastLimit int }

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
