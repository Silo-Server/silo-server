package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeAdminInsights struct {
	lastRefresh bool
	err         error
}

func (f *fakeAdminInsights) coverage() handlers.AdminInsightsCoverage {
	oldest := fixedTime().AddDate(0, 0, -29)
	return handlers.AdminInsightsCoverage{
		OldestHistoryAt:    &oldest,
		OldestMeasuredAt:   &oldest,
		OldestRouteFactsAt: &oldest,
		OldestMetricAt:     &oldest,
		TotalSessions:      48,
		MeasuredSessions:   46,
		RouteFactsSessions: 44,
	}
}

func (f *fakeAdminInsights) ReadAdminInsightsActivity(_ context.Context, q handlers.AdminInsightsWindowQuery, refresh bool) (*handlers.AdminInsightsActivity, error) {
	f.lastRefresh = refresh
	if f.err != nil {
		return nil, f.err
	}
	if q.TZ == "Invalid/Zone" {
		return nil, handlers.ErrInvalidInsightsQuery
	}
	peakAt := fixedTime().Add(-2 * time.Hour)
	return &handlers.AdminInsightsActivity{
		From: fixedTime().AddDate(0, 0, -29), To: fixedTime(), TZ: q.TZ, BucketUnit: handlers.InsightsBucketDay,
		Coverage: f.coverage(),
		Summary: handlers.AdminInsightsActivitySummary{
			Plays: 42, Sessions: 48, Movies: 12, Episodes: 30, WatchSeconds: 86400, MeasuredWatchSeconds: 84000,
			CompletedPlays: 35, CompletionRate: 0.8333,
			DirectPlays: 30, RemuxPlays: 7, TranscodePlays: 5,
			UniqueAccounts: 4, UniqueProfiles: 6, PeakConcurrentStreams: 4, PeakConcurrentAt: &peakAt,
		},
		Buckets: []handlers.AdminInsightsActivityBucket{{
			BucketStart: fixedTime().AddDate(0, 0, -1), LabelDate: fixedTime().AddDate(0, 0, -1).Format("2006-01-02"),
			Plays: 10, WatchSeconds: 18000, Direct: 7, Remux: 2, Transcode: 1, Movies: 3, Episodes: 7,
			ActiveAccounts: 3, ActiveProfiles: 4,
		}},
		Heatmap: []handlers.AdminInsightsHeatmapCell{{DayOfWeek: 4, Hour: 20, Plays: 8, WatchSeconds: 14400}},
		PeakStreams: []handlers.AdminInsightsPeakStreamsPoint{{
			Date: fixedTime().AddDate(0, 0, -1).Format("2006-01-02"), PeakStreams: 4, PeakDirect: 3, PeakRemux: 1, PeakTranscode: 1,
			PeakStreamsDirect: 2, PeakStreamsRemux: 1, PeakStreamsTranscode: 1, PeakStreamsAt: &peakAt,
		}},
		EndReasons: []handlers.AdminInsightsLabeledCount{{Key: "stopped", Sessions: 39}, {Key: "expired", Sessions: 6}, {Key: "unknown", Sessions: 3}},
	}, nil
}

func (f *fakeAdminInsights) ReadAdminInsightsPeople(_ context.Context, q handlers.AdminInsightsPeopleQuery, refresh bool) (*handlers.AdminInsightsPeople, error) {
	f.lastRefresh = refresh
	if f.err != nil {
		return nil, f.err
	}
	lastSeen := fixedTime().Add(-15 * time.Minute)
	topClient := "Silo Apple"
	return &handlers.AdminInsightsPeople{
		From: fixedTime().AddDate(0, 0, -29), To: fixedTime(), TZ: q.TZ,
		TotalAccounts: 1, ActiveAccounts: 1, ActiveProfiles: 2, LiveAccounts: 1, TotalWatchSeconds: 50400,
		Coverage: f.coverage(),
		Accounts: []handlers.AdminInsightsPersonAccount{{
			UserID: 7, Username: "alex", Role: "admin", IsLive: true,
			Plays: 24, Sessions: 27, Movies: 8, Episodes: 16, WatchSeconds: 50400,
			MeasuredSessions: 27, MeasuredShare: 1.0, DirectPlays: 20, RemuxPlays: 2, TranscodePlays: 2,
			DirectRate: 0.8333, CompletedPlays: 21, CompletionRate: 0.875,
			TopClient: &topClient, LastSeenAt: &lastSeen,
			Profiles: []handlers.AdminInsightsPersonProfile{{
				ProfileID: "p-main", ProfileName: "Alex", IsLive: true, Plays: 18, Sessions: 20, WatchSeconds: 40000,
				MeasuredSessions: 20, DirectPlays: 16, RemuxPlays: 1, TranscodePlays: 1, CompletedPlays: 16,
				CompletionRate: 0.8889, LastSeenAt: &lastSeen,
			}},
		}},
		TopProfiles: []handlers.AdminInsightsTopProfile{{UserID: 7, Username: "alex", ProfileID: "p-main", ProfileName: "Alex", Plays: 18, WatchSeconds: 40000}},
	}, nil
}

func (f *fakeAdminInsights) ReadAdminInsightsDevices(_ context.Context, q handlers.AdminInsightsDevicesQuery, refresh bool) (*handlers.AdminInsightsDevices, error) {
	f.lastRefresh = refresh
	if f.err != nil {
		return nil, f.err
	}
	lastSeen := fixedTime().Add(-15 * time.Minute)
	return &handlers.AdminInsightsDevices{
		From: fixedTime().AddDate(0, 0, -29), To: fixedTime(), TZ: q.TZ,
		Coverage: f.coverage(),
		Summary: handlers.AdminInsightsDevicesSummary{
			TotalSessions: 48, DirectSessions: 34, RemuxSessions: 8, TranscodeSessions: 6, DirectRate: 0.7083,
			RouteFactsSessions: 44, HWTranscodeSessions: 5, SWTranscodeSessions: 1,
			SubtitleBurnInSessions: 2, HDRToneMapSessions: 2, HDRToneMapHWSessions: 2, HDRToneMapSWSessions: 0,
		},
		Codecs: []string{"hevc", "h264"},
		Matrix: []handlers.AdminInsightsClientMatrixRow{{
			ClientName: "Silo Apple", DevicePlatform: "tvOS", IsJellyfinClient: false,
			TotalSessions: 20, DirectSessions: 18, RemuxSessions: 1, TranscodeSessions: 1, DirectRate: 0.9,
			Cells: []handlers.AdminInsightsClientCodecCell{{
				SourceVideoCodec: "hevc", Sessions: 12, DirectSessions: 11, RemuxSessions: 1, TranscodeSessions: 0, DirectRate: 0.9167,
			}},
		}},
		AdaptationReasons:    []handlers.AdminInsightsAdaptationReason{{Reason: "subtitle_burn_in", Sessions: 2, ShareOfAdapted: 0.1429}},
		HWAccelBreakdown:     []handlers.AdminInsightsLabeledCount{{Key: "vaapi", Sessions: 5}, {Key: "software", Sessions: 1}},
		ToneMapBreakdown:     []handlers.AdminInsightsLabeledCount{{Key: "hardware", Sessions: 2}},
		SubtitleBurnInCodecs: []handlers.AdminInsightsLabeledCount{{Key: "hdmv_pgs_subtitle", Sessions: 2}},
		SourceResolutions:    []handlers.AdminInsightsLabeledCount{{Key: "2160p", Sessions: 20}, {Key: "1080p", Sessions: 26}, {Key: "unknown", Sessions: 2}},
		DeliveredResolutions: []handlers.AdminInsightsLabeledCount{{Key: "2160p", Sessions: 17}, {Key: "1080p", Sessions: 27}, {Key: "unknown", Sessions: 4}},
		ResolutionChanges:    []handlers.AdminInsightsResolutionChange{{From: "2160p", To: "1080p", Sessions: 3}},
		BucketUnit:           handlers.InsightsBucketDay,
		ClientVersions: []handlers.AdminInsightsClientVersionRow{{
			ClientName: "Silo Apple", DevicePlatform: "tvOS", ClientVersion: "1.4.2", Sessions: 18, LastSeenAt: &lastSeen,
		}},
		ProtocolBuckets: []handlers.AdminInsightsProtocolBucket{{
			BucketStart: fixedTime().AddDate(0, 0, -1), LabelDate: fixedTime().AddDate(0, 0, -1).Format("2006-01-02"),
			NativeSessions: 9, JellyfinSessions: 3,
		}},
		Subtitles: handlers.AdminInsightsSubtitleUsage{
			Sessions: 44, SubtitledSessions: 12, ForcedSessions: 2, HearingImpairedSessions: 1,
			Languages:  []handlers.AdminInsightsLabeledCount{{Key: "en", Sessions: 9}, {Key: "es", Sessions: 3}},
			Deliveries: []handlers.AdminInsightsLabeledCount{{Key: "embedded", Sessions: 8}, {Key: "burn_in", Sessions: 2}, {Key: "sidecar", Sessions: 2}},
		},
		Audio: handlers.AdminInsightsAudioUsage{
			Sessions:          44,
			Languages:         []handlers.AdminInsightsLabeledCount{{Key: "en", Sessions: 38}, {Key: "ja", Sessions: 6}},
			Decisions:         []handlers.AdminInsightsLabeledCount{{Key: "direct", Sessions: 34}, {Key: "transcode", Sessions: 6}, {Key: "remux", Sessions: 4}},
			DeliveredChannels: []handlers.AdminInsightsLabeledCount{{Key: "surround", Sessions: 24}, {Key: "stereo", Sessions: 20}},
		},
	}, nil
}

func (f *fakeAdminInsights) ReadAdminInsightsBandwidth(_ context.Context, q handlers.AdminInsightsBandwidthQuery, refresh bool) (*handlers.AdminInsightsBandwidth, error) {
	f.lastRefresh = refresh
	if f.err != nil {
		return nil, f.err
	}
	oldest := fixedTime().AddDate(0, 0, -29)
	peakAt := fixedTime().Add(-3 * time.Hour)
	nodeType := "proxy"
	return &handlers.AdminInsightsBandwidth{
		From: oldest, To: fixedTime(), TZ: q.TZ,
		OldestMetricAt: &oldest, OldestRouteFactsAt: &oldest, HasRemoteNodes: true,
		Summary: handlers.AdminInsightsBandwidthSummary{
			TotalEgressBytes: 900_000_000_000, PlaybackEgressBytes: 810_000_000_000,
			DownloadEgressBytes: 90_000_000_000, APIEgressBytes: 300_000_000_000, NodeEgressBytes: 600_000_000_000,
			AvgDailyBytes: 30_000_000_000, PeakEgressKbps: 145_000, PeakEgressAt: &peakAt,
			PeakDownloadKbps: 42_000, ActiveDays: 30, RemoteSessions: 31, LocalSessions: 14, UnknownLocationSessions: 3,
		},
		Sources: []handlers.AdminInsightsEgressSource{
			{Key: "api", Kind: "api", Name: "API server", EgressBytes: 300_000_000_000, PeakEgressKbps: 65_000, ShareOfTotal: 0.3333},
			{Key: "node:1", Kind: "node", NodeID: 1, Name: "edge-eu-1", NodeType: &nodeType, EgressBytes: 600_000_000_000, PeakEgressKbps: 90_000, ShareOfTotal: 0.6667},
			{Key: "node:-1", Kind: "node", NodeID: -1, Name: "archived-edge", NodeType: &nodeType, EgressBytes: 0, PeakEgressKbps: 0, ShareOfTotal: 0},
		},
		Points: []handlers.AdminInsightsBandwidthPoint{{
			Date: fixedTime().AddDate(0, 0, -1).Format("2006-01-02"), EgressBytes: 30_000_000_000,
			PlaybackEgressBytes: 27_000_000_000, DownloadEgressBytes: 3_000_000_000, APIEgressBytes: 10_000_000_000,
			PeakEgressKbps: 145_000, PeakDownloadKbps: 42_000,
			BytesBySource:    map[string]int64{"api": 10_000_000_000, "node:1": 20_000_000_000},
			PeakKbpsBySource: map[string]int64{"api": 65_000, "node:1": 90_000},
		}},
	}, nil
}

func (f *fakeAdminInsights) ReadAdminInsightsTitles(_ context.Context, q handlers.AdminInsightsTitlesQuery, refresh bool) (*handlers.AdminInsightsTitles, error) {
	f.lastRefresh = refresh
	if f.err != nil {
		return nil, f.err
	}
	lastPlayed := fixedTime().Add(-90 * time.Minute)
	year := 2021
	return &handlers.AdminInsightsTitles{
		From: fixedTime().AddDate(0, 0, -29), To: fixedTime(), TZ: q.TZ, Sort: handlers.InsightsTitlesSortPlays,
		Movies: []handlers.AdminInsightsTitleRow{{
			ContentID: "movie-1", Kind: "movie", Title: "Dune", Year: &year, Plays: 6, Viewers: 4, CompletedPlays: 5,
			WatchSeconds: 46000, LastPlayedAt: &lastPlayed,
		}},
		Shows: []handlers.AdminInsightsTitleRow{{
			ContentID: "series-1", Kind: "series", Title: "Severance", Plays: 18, Viewers: 3, CompletedPlays: 16,
			Episodes: 9, WatchSeconds: 54000, LastPlayedAt: &lastPlayed,
		}},
		Libraries: []handlers.AdminInsightsLibraryRow{{LibraryID: 2, Name: "TV Shows", Plays: 18, Viewers: 3, WatchSeconds: 54000}},
		LeastFinished: []handlers.AdminInsightsTitleRow{{
			ContentID: "movie-2", Kind: "movie", Title: "The Glass Harbor", Year: &year, Plays: 4, Viewers: 3, CompletedPlays: 1,
			WatchSeconds: 12000, LastPlayedAt: &lastPlayed,
		}},
	}, nil
}

func (f *fakeAdminInsights) ReadAdminInsightsLibrary(_ context.Context, q handlers.AdminInsightsWindowQuery, refresh bool) (*handlers.AdminInsightsLibrary, error) {
	f.lastRefresh = refresh
	if f.err != nil {
		return nil, f.err
	}
	oldest := fixedTime().AddDate(0, -6, 0)
	median := 2.5
	year := 2024
	return &handlers.AdminInsightsLibrary{
		From: fixedTime().AddDate(0, 0, -29), To: fixedTime(), TZ: q.TZ, OldestHistoryAt: &oldest,
		Summary: handlers.AdminInsightsLibrarySummary{
			Titles: 420, PlayedTitles: 63, PlayedShare: 0.15, NeverStarted: 210, AddedTitles: 12, AddedPlayed: 7,
			MedianDaysToFirstPlay: &median,
		},
		Libraries: []handlers.AdminInsightsLibraryCoverage{
			{LibraryID: 1, Name: "Movies", Titles: 300, PlayedTitles: 41, PlayedShare: 0.1367, NeverStarted: 160, AddedTitles: 9},
			{LibraryID: 2, Name: "TV Shows", Titles: 120, PlayedTitles: 22, PlayedShare: 0.1833, NeverStarted: 50, AddedTitles: 3},
		},
		Genres:         []handlers.AdminInsightsLabeledPlays{{Key: "Drama", Plays: 40, Titles: 22}, {Key: "Science Fiction", Plays: 18, Titles: 7}},
		Decades:        []handlers.AdminInsightsLabeledPlays{{Key: "1990", Plays: 6, Titles: 4}, {Key: "2020", Plays: 51, Titles: 38}},
		ContentRatings: []handlers.AdminInsightsLabeledPlays{{Key: "PG-13", Plays: 25, Titles: 14}, {Key: "TV-MA", Plays: 20, Titles: 9}},
		TimeToFirstPlay: []handlers.AdminInsightsLabeledCount{
			{Key: "day", Sessions: 3}, {Key: "week", Sessions: 3}, {Key: "month", Sessions: 1}, {Key: "later", Sessions: 0}, {Key: "not_yet", Sessions: 5},
		},
		RecentlyAddedUnplayed: []handlers.AdminInsightsUnplayedTitle{{
			ContentID: "movie-9", Kind: "movie", Title: "Civil War", Year: &year, AddedAt: fixedTime().AddDate(0, 0, -2), LibraryID: 1, LibraryName: "Movies",
		}},
	}, nil
}

func (f *fakeAdminInsights) ReadAdminInsightsTitle(_ context.Context, q handlers.AdminInsightsTitleQuery) (*handlers.AdminInsightsTitle, error) {
	if f.err != nil {
		return nil, f.err
	}
	switch q.ContentID {
	case "missing":
		return nil, handlers.ErrInsightsTitleNotFound
	case "ep-1":
		return nil, handlers.ErrInvalidInsightsQuery
	}
	lastPlayed := fixedTime().Add(-90 * time.Minute)
	return &handlers.AdminInsightsTitle{
		From: fixedTime().AddDate(0, 0, -29), To: fixedTime(), TZ: q.TZ, BucketUnit: handlers.InsightsBucketDay,
		ContentID: q.ContentID, Kind: "series", Title: "Severance",
		Summary: handlers.AdminInsightsTitleSummary{Plays: 18, Sessions: 21, Viewers: 3, CompletedPlays: 16, WatchSeconds: 54000, DirectPlays: 15, RemuxPlays: 2, TranscodePlays: 1},
		Buckets: []handlers.AdminInsightsPlaysBucket{{BucketStart: fixedTime().AddDate(0, 0, -1), LabelDate: fixedTime().AddDate(0, 0, -1).Format("2006-01-02"), Plays: 4, WatchSeconds: 12000}},
		Viewers: []handlers.AdminInsightsTitleViewer{{
			UserID: 7, Username: "alex", ProfileID: "p-main", ProfileName: "Alex", Plays: 9, CompletedPlays: 8, WatchSeconds: 27000,
			LastPlayedAt: &lastPlayed, LastEpisode: &handlers.AdminInsightsEpisodeRef{ContentID: "ep-9", SeasonNumber: 1, EpisodeNumber: 9, Title: "The We We Are"},
		}},
		Episodes: []handlers.AdminInsightsTitleEpisode{{
			AdminInsightsEpisodeRef: handlers.AdminInsightsEpisodeRef{ContentID: "ep-1", SeasonNumber: 1, EpisodeNumber: 1, Title: "Good News About Hell"},
			Plays:                   3, Viewers: 3, WatchSeconds: 9000,
		}},
	}, nil
}

func adminInsightsFixtureCases() []fixtureCase {
	return []fixtureCase{
		{name: "get_admin_insights_capabilities_ok", operationID: "getAdminInsightsCapabilities", method: http.MethodGet, path: Prefix + "/admin/insights/capabilities", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control", "ETag"}, schema: "#/components/schemas/AdminInsightsCapabilities", scenario: "Administrator insights capabilities advertise available Monitor -> Insights tabs and archival datasets."},
		{name: "get_admin_insights_activity_ok", operationID: "getAdminInsightsActivity", method: http.MethodGet, path: Prefix + "/admin/insights/activity?range=30d&tz=UTC", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminInsightsActivity", scenario: "Administrator insights activity returns summary KPIs, local-timezone buckets, viewing heatmap, and peak concurrent streams."},
		{name: "get_admin_insights_people_ok", operationID: "getAdminInsightsPeople", method: http.MethodGet, path: Prefix + "/admin/insights/people?range=30d&tz=UTC", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminInsightsPeople", scenario: "Administrator insights people returns account and nested household profile viewing leaderboards."},
		{name: "get_admin_insights_devices_ok", operationID: "getAdminInsightsDevices", method: http.MethodGet, path: Prefix + "/admin/insights/devices?range=30d&tz=UTC&min_sessions=5", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminInsightsDevices", scenario: "Administrator insights devices returns client x codec direct-play matrix, adaptation reasons, transcode breakdowns, and source vs delivered resolution classes."},
		{name: "get_admin_insights_bandwidth_ok", operationID: "getAdminInsightsBandwidth", method: http.MethodGet, path: Prefix + "/admin/insights/bandwidth?range=30d&tz=UTC", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminInsightsBandwidth", scenario: "Administrator insights bandwidth returns daily archive + live egress totals and per-node breakdown."},
		{name: "get_admin_insights_titles_ok", operationID: "getAdminInsightsTitles", method: http.MethodGet, path: Prefix + "/admin/insights/titles?range=30d&tz=UTC&sort=plays&limit=10", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminInsightsTitles", scenario: "Administrator insights titles ranks the most watched movies, series and libraries."},
		{name: "get_admin_insights_title_ok", operationID: "getAdminInsightsTitle", method: http.MethodGet, path: Prefix + "/admin/insights/titles/series-1?range=30d&tz=UTC", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminInsightsTitle", scenario: "Administrator insights title detail returns a series' plays over time, viewers, and episodes."},
		{name: "get_admin_insights_library_ok", operationID: "getAdminInsightsLibrary", method: http.MethodGet, path: Prefix + "/admin/insights/library?range=30d&tz=UTC", headers: actingRequestAdmin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminInsightsLibrary", scenario: "Administrator insights library returns per-library coverage, plays by genre, decade and rating, and how soon new titles get a first play."},
	}
}

func TestAdminInsightsValidationAndMonitorScope(t *testing.T) {
	deps := pilotDeps(nil, nil)
	svc := &fakeAdminInsights{}
	deps.AdminInsights = svc
	deps.AdminPlaybackHistory = &fakeAdminPlaybackHistory{}
	users := map[int]*models.User{
		1: {ID: 1, Role: "user", Enabled: true},
		2: {ID: 2, Role: "admin", Enabled: true},
	}
	deps.Auth = apimw.NewAuthMiddleware(fakeTokens{}, fakeSessions{}, fakeAPIKeys{keys: map[string]*models.APIKey{
		"sa_monitor":        {ID: 1, UserID: 2, Scopes: []string{auth.ScopeAdminMonitorRead}},
		"sa_member_monitor": {ID: 2, UserID: 1, Scopes: []string{auth.ScopeAdminMonitorRead}},
		"sa_admin":          {ID: 3, UserID: 2},
	}}, fakeUsers{users: users})
	h := NewHandler(deps)

	// Monitor key can read all insights endpoints and session detail, but cannot force refresh.
	for _, path := range []string{
		"/admin/insights/capabilities",
		"/admin/insights/activity?range=30d&refresh=true",
		"/admin/insights/people?range=30d&refresh=true",
		"/admin/insights/devices?range=30d&refresh=true",
		"/admin/insights/bandwidth?range=30d&refresh=true",
		"/admin/insights/titles?range=30d&refresh=true",
		"/admin/insights/titles/series-1?range=30d",
		"/admin/insights/library?range=30d&refresh=true",
		"/admin/playback-history/sess-1",
	} {
		rec := do(t, h, http.MethodGet, Prefix+path, "", bearer("sa_monitor"))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s with monitor scope: status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
		if svc.lastRefresh {
			t.Fatalf("GET %s allowed monitor key to bypass cache refresh", path)
		}
		requireProblem(t, do(t, h, http.MethodGet, Prefix+path, "", bearer("sa_member_monitor")), TypePermissionDenied)
	}

	// Unscoped admin honors refresh=true.
	if rec := do(t, h, http.MethodGet, Prefix+"/admin/insights/activity?range=7d&refresh=true", "", bearer("sa_admin")); rec.Code != http.StatusOK || !svc.lastRefresh {
		t.Fatalf("admin refresh: code=%d refresh=%v body=%s", rec.Code, svc.lastRefresh, rec.Body.String())
	}

	// Invalid timezone maps to 422 validation_failed; missing session detail maps to 404 not_found.
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/insights/activity?tz=Invalid/Zone", "", bearer("sa_admin")), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/playback-history/missing", "", bearer("sa_admin")), TypeNotFound)

	// A profile filter needs its account: profile ids such as "default" repeat across accounts.
	for _, path := range []string{"/admin/insights/activity?profile_id=default", "/admin/insights/titles?profile_id=default", "/admin/insights/titles/series-1?profile_id=default"} {
		requireProblem(t, do(t, h, http.MethodGet, Prefix+path, "", bearer("sa_admin")), TypeValidationFailed)
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/insights/titles/missing", "", bearer("sa_admin")), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/insights/titles/ep-1", "", bearer("sa_admin")), TypeValidationFailed)

	// Capabilities expose availability and the daily node-egress archive flag.
	rec := do(t, h, http.MethodGet, Prefix+"/admin/insights/capabilities", "", bearer("sa_admin"))
	var caps AdminInsightsCapabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	if !caps.Available || !caps.NodeEgressDaily {
		t.Fatalf("unexpected capabilities: %+v", caps)
	}

	// Synthetic negative node IDs from restored backups must omit node_id on the wire.
	bwRec := do(t, h, http.MethodGet, Prefix+"/admin/insights/bandwidth?range=30d&tz=UTC", "", bearer("sa_admin"))
	var bw AdminInsightsBandwidth
	if err := json.Unmarshal(bwRec.Body.Bytes(), &bw); err != nil {
		t.Fatal(err)
	}
	if len(bw.Sources) != 3 || bw.Sources[1].NodeID == nil || *bw.Sources[1].NodeID != "1" || bw.Sources[2].NodeID != nil {
		t.Fatalf("unexpected bandwidth source node_id wire encoding: %+v", bw.Sources)
	}
}
