package apiv2

import (
	"context"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type fakeAdminPlaybackHistory struct {
	calls  int
	filter handlers.AdminPlaybackHistoryFilter
	after  *handlers.AdminPlaybackHistoryPageKey
	limit  int
	err    error
	// episode returns an episode attempt with its series placement.
	episode bool
}

func (f *fakeAdminPlaybackHistory) ListAdminPlaybackHistoryPage(_ context.Context, filter handlers.AdminPlaybackHistoryFilter, after *handlers.AdminPlaybackHistoryPageKey, limit int) (handlers.AdminPlaybackHistoryPage, error) {
	f.calls++
	f.filter, f.after, f.limit = filter, after, limit
	if f.err != nil {
		return handlers.AdminPlaybackHistoryPage{}, f.err
	}
	duration := 5400.5
	// A play resumed near its end: watched_seconds is the end position, the
	// measurement is the two minutes actually watched.
	played, start, paused := 120.0, 3480.0, 15.0
	since, until := fixedTime().Add(-3*time.Minute), fixedTime().Add(-time.Minute)
	ip, device, platform, decision, reason := "2001:db8::7", "iphone-1", "iOS", "direct", "stopped"
	rows := []handlers.AdminPlaybackHistoryRow{{
		SessionID: "sess-9007199254740993", UserID: 9007199254740993, Username: "laura", ProfileID: "p-owner", ProfileName: "Laura",
		MediaItemID: "movie-1", MediaFileID: 42, MediaTitle: "Synthetic Movie", MediaType: "movie", PlayMethod: "direct_play",
		StartedAt: fixedTime().Add(-time.Hour), EndedAt: fixedTime().Add(123456 * time.Nanosecond), WatchedSeconds: 3600, DurationSeconds: &duration, Completed: true,
		PlayedSeconds: &played, Measured: true, StartPositionSeconds: &start, PausedSeconds: &paused,
		MeasuredSince: &since, MeasuredUntil: &until, ClientIP: &ip, DeviceID: &device, DevicePlatform: &platform,
		// audio_decision stays null, as on a row recorded before decisions were.
		VideoDecision: &decision, EndReason: &reason,
	}}
	if f.episode {
		season, episode := 2, 4
		rows[0].MediaType, rows[0].MediaTitle = "episode", "Synthetic Episode"
		rows[0].SeriesTitle, rows[0].SeasonNumber, rows[0].EpisodeNumber = "Synthetic Series", &season, &episode
	}
	if after != nil {
		rows = []handlers.AdminPlaybackHistoryRow{}
	}
	return handlers.AdminPlaybackHistoryPage{Items: rows, HasMore: after == nil}, nil
}

func adminPlaybackHistoryFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "list_admin_playback_history_ok", operationID: opListAdminPlaybackHistory, method: http.MethodGet, path: Prefix + "/admin/playback-history?limit=1", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminPlaybackHistoryCollection", scenario: "Administrator playback history page carries string IDs, instants, a nullable duration and a scoped cursor."},
		{name: "get_admin_playback_history_capabilities_ok", operationID: "getAdminPlaybackHistoryCapabilities", method: http.MethodGet, path: Prefix + "/admin/playback-history/capabilities", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/AdminPlaybackHistoryCapabilities", scenario: "Administrator playback history capabilities name the window filter and the entry fields the server records."},
	}
}
