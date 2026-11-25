package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func insightsTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := pgx.Identifier{fmt.Sprintf("insights_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	})
	tables := []string{
		"users", "media_folders", "media_items", "episodes", "media_item_libraries",
		"media_files", "stream_nodes", "admin_playback_history", "playback_sessions_sync",
		"dashboard_metric_samples", "dashboard_node_egress_samples",
		"dashboard_metric_daily", "dashboard_node_egress_daily",
	}
	for _, tbl := range tables {
		if _, err := admin.Exec(t.Context(), fmt.Sprintf("CREATE TABLE %s.%s (LIKE public.%s INCLUDING ALL)", schema, tbl, tbl)); err != nil {
			t.Fatalf("clone table %s: %v", tbl, err)
		}
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type insightsTestSeed struct {
	moviesLibID int
	tvLibID     int
	transcodeID string
}

func seedInsightsFixture(t *testing.T, pool *pgxpool.Pool) insightsTestSeed {
	t.Helper()
	ctx := t.Context()

	var aliceID, bobID, moviesLibID, tvLibID, movieFileID, epFileID, edgeNodeID int
	mustRow := func(sql string, dest *int, args ...any) {
		t.Helper()
		if err := pool.QueryRow(ctx, sql, args...).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustRow(`INSERT INTO users (username, role) VALUES ('alice', 'admin') RETURNING id`, &aliceID)
	mustRow(`INSERT INTO users (username, role) VALUES ('bob', 'user') RETURNING id`, &bobID)
	mustRow(`INSERT INTO media_folders (type, name) VALUES ('movies', 'Movies') RETURNING id`, &moviesLibID)
	mustRow(`INSERT INTO media_folders (type, name) VALUES ('tv', 'TV Shows') RETURNING id`, &tvLibID)
	mustExec(`INSERT INTO media_items (content_id, type, title) VALUES ('movie-1', 'movie', 'Dune'), ('series-1', 'series', 'Severance')`)
	mustExec(`INSERT INTO episodes (content_id, series_id, title, season_number, episode_number) VALUES ('ep-1', 'series-1', 'Good News About Hell', 1, 1)`)
	mustRow(`INSERT INTO media_files (media_folder_id, file_path, content_id, container, codec_video, resolution, codec_audio, audio_channels, bitrate, hdr, video_tracks)
		VALUES ($1, '/movies/dune.mkv', 'movie-1', 'mkv', 'hevc', '4k', 'truehd', 8, 42000, TRUE, '[{"video_range_type":"HDR10"}]'::jsonb) RETURNING id`, &movieFileID, moviesLibID)
	mustRow(`INSERT INTO media_files (media_folder_id, file_path, episode_id, container, codec_video, resolution, codec_audio, audio_channels, bitrate, hdr)
		VALUES ($1, '/tv/s01e01.mkv', 'ep-1', 'mkv', 'h264', '1080p', 'eac3', 6, 8500, FALSE) RETURNING id`, &epFileID, tvLibID)
	mustRow(`INSERT INTO stream_nodes (name, type, url, enabled, healthy) VALUES ('edge-eu-1', 'proxy', 'http://edge-eu-1:8090', TRUE, TRUE) RETURNING id`, &edgeNodeID)

	nowUTC := time.Now().UTC().Truncate(time.Minute)
	yesterdayDay := nowUTC.Truncate(24*time.Hour).AddDate(0, 0, -1)
	twoDaysAgo := nowUTC.Truncate(24*time.Hour).AddDate(0, 0, -2)
	todayStart := nowUTC.Truncate(24 * time.Hour)

	store := NewPGPlaybackAdminStore(pool, nil)
	s1Start := yesterdayDay.Add(22 * time.Hour)
	if err := store.RecordHistory(ctx, AdminPlaybackHistoryEntry{
		SessionID: "sess-1-" + uuid.NewString(), UserID: aliceID, ProfileID: "p-alice", ProfileName: "Alice",
		MediaItemID: "movie-1", MediaFileID: movieFileID, PlayMethod: string(playback.PlayDirect),
		StartedAt: s1Start.Format(time.RFC3339Nano), EndedAt: s1Start.Add(2 * time.Hour).Format(time.RFC3339Nano),
		EndPositionSeconds: 7000, Completed: true, ClientIP: "192.168.1.20",
		Watch:          playback.WatchTime{Measured: true, PlayedSeconds: 7000, Since: s1Start, Until: s1Start.Add(2 * time.Hour)},
		DevicePlatform: "tvOS", ClientName: "Silo Apple", ClientVersion: "1.4.0",
		VideoDecision: "direct", AudioDecision: "direct", EndReason: PlaybackEndStopped,
		OutputContainer: "mkv", OutputProtocol: "http", ReportingNode: "api-1", StreamLocation: "local",
		RoutingExecution: "local", RoutingEgress: "api", RouteFactsRecorded: true,
	}); err != nil {
		t.Fatal(err)
	}

	s2ID := "sess-2-" + uuid.NewString()
	s2Start := yesterdayDay.Add(20 * time.Hour)
	if err := store.RecordHistory(ctx, AdminPlaybackHistoryEntry{
		SessionID: s2ID, UserID: bobID, ProfileID: "p-bob", ProfileName: "Bob",
		MediaItemID: "movie-1", MediaFileID: movieFileID, PlayMethod: string(playback.PlayTranscode),
		StartedAt: s2Start.Format(time.RFC3339Nano), EndedAt: s2Start.Add(45 * time.Minute).Format(time.RFC3339Nano),
		EndPositionSeconds: 2700, Completed: false, ClientIP: "203.0.113.42",
		Watch:          playback.WatchTime{Measured: true, PlayedSeconds: 2400, PausedSeconds: 120, Since: s2Start, Until: s2Start.Add(45 * time.Minute)},
		DevicePlatform: "web", ClientName: "Chrome", ClientVersion: "131",
		VideoDecision: "transcode", AudioDecision: "transcode", EndReason: PlaybackEndStopped,
		Subtitle:         &playback.SessionSubtitle{TrackID: "file:1:subtitle:0", Codec: "hdmv_pgs_subtitle", Source: "embedded", Delivery: "burn_in"},
		TargetVideoCodec: "h264", TargetResolution: "1080p", TargetAudioCodec: "aac", TargetAudioChannels: 2,
		OutputContainer: "fmp4", OutputProtocol: "hls", TargetBitrateKbps: 8000,
		TranscodeHWAccel: "vaapi", ToneMapMode: "hardware",
		AdaptationReasons: []string{"video_codec_unsupported", "subtitle_burn_in"},
		ReportingNode:     "api-1", StreamLocation: "remote", CompatOrigin: true,
		RoutingExecution: "local", RoutingEgress: "proxy", RoutingEgressNodeID: edgeNodeID,
		RouteFactsRecorded: true,
	}); err != nil {
		t.Fatal(err)
	}

	s3Start := twoDaysAgo.Add(15 * time.Hour)
	mustExec(`INSERT INTO admin_playback_history
		(session_id, user_id, profile_id, profile_name, media_item_id, media_file_id, play_method, started_at, ended_at, watched_seconds, completed, client_name, device_platform)
		VALUES ($1, $2, 'p-kids', 'Kids', 'ep-1', $3, 'remux', $4, $5, 1800, TRUE, 'Silo Apple', 'tvOS')`,
		"sess-3-"+uuid.NewString(), aliceID, epFileID, s3Start, s3Start.Add(30*time.Minute))
	mustExec(`INSERT INTO playback_sessions_sync (session_id, user_id, profile_id) VALUES ('live-1', $1, 'p-alice')`, aliceID)

	seedInsightsMetrics(t, pool, edgeNodeID, yesterdayDay, todayStart)
	return insightsTestSeed{moviesLibID: moviesLibID, tvLibID: tvLibID, transcodeID: s2ID}
}

func seedInsightsMetrics(t *testing.T, pool *pgxpool.Pool, edgeNodeID int, yesterdayDay, todayStart time.Time) {
	t.Helper()
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_metric_daily
			(day, peak_streams, peak_direct, peak_remux, peak_transcode, peak_streams_direct, peak_streams_remux, peak_streams_transcode,
			 peak_egress_kbps, peak_download_kbps, egress_bytes, download_egress_bytes, api_egress_bytes, api_peak_egress_kbps,
			 peak_streams_at, peak_egress_at)
		VALUES ($1, 6, 4, 1, 1, 4, 1, 1, 50000, 10000, 1000000000, 200000000, 400000000, 20000, $2, $2)`,
		yesterdayDay.Format("2006-01-02"), yesterdayDay.Add(21*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_node_egress_daily (day, node_id, egress_bytes, peak_egress_kbps)
		VALUES ($1, $2, 600000000, 30000)`, yesterdayDay.Format("2006-01-02"), edgeNodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_metric_samples (bucket, source, streams_total, streams_direct, streams_remux, streams_transcode, egress_kbps, download_egress_kbps)
		VALUES ($1, 'shared', 99, 99, 0, 0, 999999, 0),
		       ($2, 'shared', 3, 2, 0, 1, 10000, 0),
		       ($2, 'proc:api-1', 0, 0, 0, 0, 4000, 1000)`,
		yesterdayDay.Add(12*time.Hour), todayStart.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_node_egress_samples (bucket, node_id, egress_kbps)
		VALUES ($1, $2, 999999), ($3, $2, 10000)`,
		yesterdayDay.Add(12*time.Hour), edgeNodeID, todayStart.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestAdminInsightsQueriesAndHistoryFiltersDB(t *testing.T) {
	pool := insightsTestPool(t)
	seed := seedInsightsFixture(t, pool)
	h := &AdminHandler{pool: pool}

	t.Run("activity", func(t *testing.T) { assertInsightsActivityDB(t, h, seed) })
	t.Run("people", func(t *testing.T) { assertInsightsPeopleDB(t, h) })
	t.Run("devices", func(t *testing.T) { assertInsightsDevicesDB(t, h) })
	t.Run("bandwidth", func(t *testing.T) { assertInsightsBandwidthDB(t, h) })
	t.Run("history_filters_and_detail", func(t *testing.T) { assertInsightsHistoryFiltersAndDetailDB(t, h, seed) })
	t.Run("edge_cases", func(t *testing.T) { assertInsightsEdgeCasesDB(t, pool, h) })
}

func assertInsightsActivityDB(t *testing.T, h *AdminHandler, seed insightsTestSeed) {
	t.Helper()
	ctx := t.Context()
	if _, err := h.ReadAdminInsightsActivity(ctx, AdminInsightsWindowQuery{Range: "7d", TZ: "Mars/Olympus"}, false); !errors.Is(err, ErrInvalidInsightsQuery) {
		t.Fatalf("invalid tz err = %v, want ErrInvalidInsightsQuery", err)
	}
	act, err := h.ReadAdminInsightsActivity(ctx, AdminInsightsWindowQuery{Range: "7d", TZ: "America/New_York"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if act.Summary.Plays != 3 || act.Summary.Sessions != 3 || act.Summary.Movies != 2 || act.Summary.Episodes != 1 ||
		act.Coverage.MeasuredSessions != 2 || act.Coverage.RouteFactsSessions != 2 ||
		act.Summary.PeakConcurrentStreams != 6 || len(act.PeakStreams) != 2 {
		t.Fatalf("unexpected activity: summary=%+v coverage=%+v peaks=%+v", act.Summary, act.Coverage, act.PeakStreams)
	}
	tvAct, err := h.ReadAdminInsightsActivity(ctx, AdminInsightsWindowQuery{Range: "7d", TZ: "UTC", LibraryID: seed.tvLibID}, false)
	if err != nil || tvAct.Summary.Plays != 1 || tvAct.Summary.Episodes != 1 {
		t.Fatalf("library-filtered activity = %+v, %v", tvAct, err)
	}
}

func assertInsightsPeopleDB(t *testing.T, h *AdminHandler) {
	t.Helper()
	people, err := h.ReadAdminInsightsPeople(t.Context(), AdminInsightsPeopleQuery{
		AdminInsightsWindowQuery: AdminInsightsWindowQuery{Range: "7d", TZ: "UTC"},
		Sort:                     "watch_time", Dir: "desc",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if people.ActiveAccounts != 2 || people.ActiveProfiles != 3 || people.LiveAccounts != 1 ||
		people.TotalWatchSeconds <= 0 ||
		len(people.Accounts) != 2 || people.Accounts[0].Username != "alice" || !people.Accounts[0].IsLive || len(people.Accounts[0].Profiles) != 2 {
		t.Fatalf("unexpected people: %+v", people)
	}
}

func assertInsightsDevicesDB(t *testing.T, h *AdminHandler) {
	t.Helper()
	devs, err := h.ReadAdminInsightsDevices(t.Context(), AdminInsightsDevicesQuery{
		AdminInsightsWindowQuery: AdminInsightsWindowQuery{Range: "7d", TZ: "UTC"}, MinSessions: 1,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if devs.Summary.TotalSessions != 3 || devs.Summary.HWTranscodeSessions != 1 || devs.Summary.SubtitleBurnInSessions != 1 ||
		devs.Summary.HDRToneMapHWSessions != 1 || len(devs.Matrix) != 2 || len(devs.AdaptationReasons) != 2 ||
		len(devs.HWAccelBreakdown) == 0 || len(devs.ToneMapBreakdown) != 1 || len(devs.SubtitleBurnInCodecs) != 1 {
		t.Fatalf("unexpected devices: %+v", devs)
	}
	// Bob's transcode of the 4K file to 1080p is the one downscale.
	if len(devs.ResolutionChanges) != 1 || devs.ResolutionChanges[0] != (AdminInsightsResolutionChange{From: "2160p", To: "1080p", Sessions: 1}) {
		t.Fatalf("resolution changes = %+v (source %+v, delivered %+v)", devs.ResolutionChanges, devs.SourceResolutions, devs.DeliveredResolutions)
	}
}

func assertInsightsBandwidthDB(t *testing.T, h *AdminHandler) {
	t.Helper()
	bw, err := h.ReadAdminInsightsBandwidth(t.Context(), AdminInsightsBandwidthQuery{Range: "7d", TZ: "UTC"}, false)
	if err != nil {
		t.Fatal(err)
	}
	wantTotal, wantNode := int64(1_000_000_000+14000*7500), int64(600_000_000+10000*7500)
	if bw.Summary.TotalEgressBytes != wantTotal || bw.Summary.NodeEgressBytes != wantNode ||
		bw.Summary.PeakEgressKbps != 50000 || !bw.HasRemoteNodes || len(bw.Points) != 2 ||
		bw.Summary.RemoteSessions != 1 || bw.Summary.LocalSessions != 1 || bw.Summary.UnknownLocationSessions != 1 {
		t.Fatalf("unexpected bandwidth: summary=%+v points=%+v", bw.Summary, bw.Points)
	}
}

func assertInsightsHistoryFiltersAndDetailDB(t *testing.T, h *AdminHandler, seed insightsTestSeed) {
	t.Helper()
	ctx := t.Context()
	burnIn := true
	page, err := h.ListAdminPlaybackHistoryPage(ctx, AdminPlaybackHistoryFilter{
		PlayMethod: "transcode", SourceVideoCodec: "hevc", SubtitleBurnIn: &burnIn,
		StreamLocation: "remote", AdaptationReason: "subtitle_burn_in", LibraryID: seed.moviesLibID, Search: "dune",
	}, nil, 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].SessionID != seed.transcodeID {
		t.Fatalf("rich filter got %+v, err=%v", page.Items, err)
	}
	detail, err := h.ReadAdminPlaybackHistoryEntry(ctx, seed.transcodeID)
	if err != nil || detail == nil || !detail.RouteFactsRecorded ||
		detail.SourceVideoCodec == nil || *detail.SourceVideoCodec != "hevc" ||
		detail.SourceVideoRangeType == nil || *detail.SourceVideoRangeType != "HDR10" ||
		detail.RoutingEgressNodeName == nil || *detail.RoutingEgressNodeName != "edge-eu-1" ||
		!detail.IsJellyfinClient || detail.LibraryName == nil || *detail.LibraryName != "Movies" {
		t.Fatalf("unexpected history detail: %+v, err=%v", detail, err)
	}
}

func assertInsightsEdgeCasesDB(t *testing.T, pool *pgxpool.Pool, h *AdminHandler) {
	t.Helper()
	ctx := t.Context()

	// 1. Legacy bps bitrate (> 10_000_000) normalizes to kbps in history detail.
	var legacyFileID, userID int
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE username = 'alice'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, content_id, container, codec_video, resolution, bitrate)
		VALUES ((SELECT id FROM media_folders LIMIT 1), '/movies/legacy-bps.mkv', 'deleted-item-1', 'mkv', 'hevc', '4k', 60000000)
		RETURNING id
	`).Scan(&legacyFileID); err != nil {
		t.Fatal(err)
	}
	deletedSess := "sess-del-" + uuid.NewString()
	playAt := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -3).Add(10 * time.Hour)
	if _, err := pool.Exec(ctx, `
		INSERT INTO admin_playback_history
			(session_id, user_id, profile_id, profile_name, media_item_id, media_file_id, play_method, started_at, ended_at, watched_seconds, completed)
		VALUES ($1, $2, 'p-alice', 'Alice', 'deleted-item-1', $3, 'direct', $4, $5, 600, TRUE)
	`, deletedSess, userID, legacyFileID, playAt, playAt.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	detail, err := h.ReadAdminPlaybackHistoryEntry(ctx, deletedSess)
	if err != nil || detail == nil || detail.SourceBitrateKbps == nil || *detail.SourceBitrateKbps != 60000 {
		t.Fatalf("legacy bps bitrate normalization failed: detail=%+v err=%v", detail, err)
	}

	// 2. Deleted catalog item counts identically under other_media in summary and buckets.
	act, err := h.ReadAdminInsightsActivity(ctx, AdminInsightsWindowQuery{Range: "7d", TZ: "UTC"}, false)
	if err != nil {
		t.Fatal(err)
	}
	var bucketOther int64
	for _, b := range act.Buckets {
		bucketOther += b.OtherMedia
	}
	if act.Summary.OtherMedia != 1 || bucketOther != 1 {
		t.Fatalf("deleted item other_media mismatch: summary=%d buckets=%d", act.Summary.OtherMedia, bucketOther)
	}

	// 3. Pruning minute samples after archiving never changes a non-UTC custom
	// date range's peak streams, bandwidth, or per-node totals.
	assertPruneInvariantNonUTCCustomRangeDB(t, pool, h)

	// 4. Alternate edition & selected non-default audio track attribution.
	assertEffectiveMediaAndAudioTrackDB(t, pool, h, userID)

	// 5. Case-insensitive client+platform merge across codecs meets min_sessions.
	assertDevicesCaseInsensitiveMergeDB(t, pool, h, userID)

	// 6. First-time live viewer without finalized history counts in LiveAccounts.
	assertPeopleCountsFirstTimeLiveAccountDB(t, pool, h)

	// 7. History search matches literal % and _ without treating them as wildcards.
	assertHistoryLiteralLikeSearchDB(t, pool, h, userID)

	// 8. People pagination serves pages from the same cached sorted snapshot.
	assertPeopleCacheConsistentAcrossPagesDB(t, pool)
}

func assertPeopleCacheConsistentAcrossPagesDB(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	provider, err := NewAdminInsightsProvider(ctx, pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()

	baseQ := AdminInsightsPeopleQuery{
		AdminInsightsWindowQuery: AdminInsightsWindowQuery{Range: "7d", TZ: "UTC"},
		Sort:                     "watch_time",
		Dir:                      "desc",
		Limit:                    1,
	}
	page1, err := provider.People(ctx, baseQ)
	if err != nil || len(page1.Accounts) != 1 {
		t.Fatalf("page 1 = %+v, %v", page1, err)
	}

	// Insert a huge new play for a third user between page 1 and page 2 without
	// invalidating the provider. Page 2 must read from the cached snapshot so
	// alice/bob never shift or duplicate across pages.
	var daveID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ('dave', 'user') RETURNING id`).Scan(&daveID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if _, err := pool.Exec(ctx, `
		INSERT INTO admin_playback_history
			(session_id, user_id, profile_id, media_item_id, media_file_id, play_method, started_at, ended_at, watched_seconds, played_seconds, measured)
		VALUES ($1, $2, 'p-dave', 'movie-1', 0, 'direct', $3, $4, 99999, 99999, TRUE)
	`, "sess-dave-"+uuid.NewString(), daveID, now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	page2Q := baseQ
	page2Q.Offset = 1
	page2, err := provider.People(ctx, page2Q)
	if err != nil || len(page2.Accounts) != 1 {
		t.Fatalf("page 2 = %+v, %v", page2, err)
	}
	if page2.Accounts[0].UserID == page1.Accounts[0].UserID || page2.Accounts[0].Username == "dave" || page2.TotalAccounts != page1.TotalAccounts {
		t.Fatalf("page 2 drifted from cached snapshot: page1=%+v page2=%+v", page1.Accounts, page2.Accounts)
	}
}

func assertPruneInvariantNonUTCCustomRangeDB(t *testing.T, pool *pgxpool.Pool, h *AdminHandler) {
	t.Helper()
	ctx := t.Context()
	var edgeNodeID int
	if err := pool.QueryRow(ctx, `SELECT id FROM stream_nodes LIMIT 1`).Scan(&edgeNodeID); err != nil {
		t.Fatal(err)
	}
	targetDate := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -5)
	dayStr := targetDate.Format("2006-01-02")
	prevUTCSample := targetDate.Add(-1 * time.Hour)
	earlyUTCSample := targetDate.Add(2 * time.Hour) // Jan 9 evening in NY, inside targetDate UTC
	midUTCSample := targetDate.Add(8 * time.Hour)
	nextUTCSample := targetDate.Add(26 * time.Hour)

	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_metric_samples (bucket, source, streams_total, streams_direct, egress_kbps)
		VALUES ($1, 'shared', 77, 77, 77000),
		       ($2, 'shared', 90, 90, 9000),
		       ($3, 'shared', 4, 4, 2000),
		       ($4, 'shared', 88, 88, 88000)`,
		prevUTCSample, earlyUTCSample, midUTCSample, nextUTCSample); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_node_egress_samples (bucket, node_id, egress_kbps)
		VALUES ($1, $5, 77000), ($2, $5, 9000), ($3, $5, 2000), ($4, $5, 88000)`,
		prevUTCSample, earlyUTCSample, midUTCSample, nextUTCSample, edgeNodeID); err != nil {
		t.Fatal(err)
	}

	winQ := AdminInsightsWindowQuery{Range: InsightsRangeCustom, FromDate: dayStr, ToDate: dayStr, TZ: "America/New_York"}
	bwQ := AdminInsightsBandwidthQuery{Range: InsightsRangeCustom, FromDate: dayStr, ToDate: dayStr, TZ: "America/New_York"}
	actBefore, err := h.ReadAdminInsightsActivity(ctx, winQ, true)
	if err != nil {
		t.Fatal(err)
	}
	bwBefore, err := h.ReadAdminInsightsBandwidth(ctx, bwQ, true)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := int64((9000 + 2000) * 7500)
	if actBefore.Summary.PeakConcurrentStreams != 90 || bwBefore.Summary.TotalEgressBytes != wantBytes ||
		bwBefore.Summary.NodeEgressBytes != wantBytes || bwBefore.Summary.PeakEgressKbps != 9000 {
		t.Fatalf("before prune: peak=%d bw=%+v", actBefore.Summary.PeakConcurrentStreams, bwBefore.Summary)
	}

	// Archive targetDate and prune its raw minute samples.
	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_metric_daily
			(day, peak_streams, peak_direct, peak_streams_direct, peak_egress_kbps, egress_bytes, peak_streams_at, peak_egress_at)
		VALUES ($1, 90, 90, 90, 9000, $2, $3, $3)`, dayStr, wantBytes, earlyUTCSample); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO dashboard_node_egress_daily (day, node_id, node_name, node_type, egress_bytes, peak_egress_kbps)
		VALUES ($1, $2, 'edge-eu-1', 'proxy', $3, 9000)`, dayStr, edgeNodeID, wantBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM dashboard_metric_samples WHERE bucket >= $1 AND bucket < $2`,
		targetDate, targetDate.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM dashboard_node_egress_samples WHERE bucket >= $1 AND bucket < $2`,
		targetDate, targetDate.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}

	actAfter, err := h.ReadAdminInsightsActivity(ctx, winQ, true)
	if err != nil {
		t.Fatal(err)
	}
	bwAfter, err := h.ReadAdminInsightsBandwidth(ctx, bwQ, true)
	if err != nil {
		t.Fatal(err)
	}
	if actAfter.Summary.PeakConcurrentStreams != actBefore.Summary.PeakConcurrentStreams ||
		bwAfter.Summary.TotalEgressBytes != bwBefore.Summary.TotalEgressBytes ||
		bwAfter.Summary.NodeEgressBytes != bwBefore.Summary.NodeEgressBytes ||
		bwAfter.Summary.PeakEgressKbps != bwBefore.Summary.PeakEgressKbps {
		t.Fatalf("pruning changed query answer: before=(peak=%d bytes=%d node=%d) after=(peak=%d bytes=%d node=%d)",
			actBefore.Summary.PeakConcurrentStreams, bwBefore.Summary.TotalEgressBytes, bwBefore.Summary.NodeEgressBytes,
			actAfter.Summary.PeakConcurrentStreams, bwAfter.Summary.TotalEgressBytes, bwAfter.Summary.NodeEgressBytes)
	}
}

func assertEffectiveMediaAndAudioTrackDB(t *testing.T, pool *pgxpool.Pool, h *AdminHandler, userID int) {
	t.Helper()
	ctx := t.Context()
	var reqFileID, altFileID int
	audioJSON := `[{"index":0,"codec":"truehd","channels":8},{"index":1,"codec":"aac","channels":2}]`
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, content_id, container, codec_video, resolution, codec_audio, audio_channels, audio_tracks)
		VALUES ((SELECT id FROM media_folders LIMIT 1), '/movies/dune-4k.mkv', 'movie-1', 'mkv', 'hevc', '4k', 'truehd', 8, $1::jsonb)
		RETURNING id`, audioJSON).Scan(&reqFileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, content_id, container, codec_video, resolution, codec_audio, audio_channels, audio_tracks)
		VALUES ((SELECT id FROM media_folders LIMIT 1), '/movies/dune-1080p.mp4', 'movie-1', 'mp4', 'h264', '1080p', 'truehd', 8, $1::jsonb)
		RETURNING id`, audioJSON).Scan(&altFileID); err != nil {
		t.Fatal(err)
	}

	trackIdx := 1
	sessID := "sess-alt-" + uuid.NewString()
	start := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Second)
	store := NewPGPlaybackAdminStore(pool, nil)
	if err := store.RecordHistory(ctx, AdminPlaybackHistoryEntry{
		SessionID: sessID, UserID: userID, ProfileID: "p-alice", ProfileName: "Alice",
		MediaItemID: "movie-1", MediaFileID: reqFileID, EffectiveMediaFileID: altFileID,
		AudioTrackIndex: &trackIdx, PlayMethod: string(playback.PlayDirect),
		VideoDecision: "direct", AudioDecision: "direct",
		StartedAt: start.Format(time.RFC3339Nano), EndedAt: start.Add(20 * time.Minute).Format(time.RFC3339Nano),
		ClientName: "AltEditionClient", DevicePlatform: "iOS", RouteFactsRecorded: true,
	}); err != nil {
		t.Fatal(err)
	}

	detail, err := h.ReadAdminPlaybackHistoryEntry(ctx, sessID)
	if err != nil || detail == nil {
		t.Fatalf("ReadAdminPlaybackHistoryEntry: %+v, %v", detail, err)
	}
	if detail.SourceVideoCodec == nil || *detail.SourceVideoCodec != "h264" ||
		detail.SourceAudioCodec == nil || *detail.SourceAudioCodec != "aac" ||
		detail.SourceAudioChannels == nil || *detail.SourceAudioChannels != 2 {
		t.Fatalf("effective edition/audio track mismatch: video=%v audio=%v ch=%v",
			detail.SourceVideoCodec, detail.SourceAudioCodec, detail.SourceAudioChannels)
	}

	devs, err := h.ReadAdminInsightsDevices(ctx, AdminInsightsDevicesQuery{
		AdminInsightsWindowQuery: AdminInsightsWindowQuery{Range: "7d", TZ: "UTC"}, MinSessions: 1,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, row := range devs.Matrix {
		if row.ClientName != "AltEditionClient" {
			continue
		}
		found = true
		if len(row.Cells) != 1 || row.Cells[0].SourceVideoCodec != "h264" || row.Cells[0].DirectSessions != 1 {
			t.Fatalf("Devices credited wrong edition codec: %+v", row.Cells)
		}
	}
	if !found {
		t.Fatal("AltEditionClient missing from Devices matrix")
	}
}

func assertDevicesCaseInsensitiveMergeDB(t *testing.T, pool *pgxpool.Pool, h *AdminHandler, userID int) {
	t.Helper()
	ctx := t.Context()
	var hevcFileID, h264FileID int
	if err := pool.QueryRow(ctx, `SELECT id FROM media_files WHERE codec_video = 'hevc' LIMIT 1`).Scan(&hevcFileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM media_files WHERE codec_video = 'h264' LIMIT 1`).Scan(&h264FileID); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second)
	for i := range 3 {
		if _, err := pool.Exec(ctx, `
			INSERT INTO admin_playback_history
				(session_id, user_id, profile_id, media_item_id, media_file_id, play_method, started_at, ended_at, client_name, device_platform)
			VALUES ($1, $2, 'p-alice', 'movie-1', $3, 'direct', $4, $5, 'MixedCaseApp', 'Web'),
			       ($6, $2, 'p-alice', 'movie-1', $7, 'transcode', $4, $5, 'mixedcaseapp', 'web')`,
			"sess-mc-h264-"+uuid.NewString(), userID, h264FileID, base.Add(time.Duration(i)*time.Minute), base.Add(time.Duration(i+1)*time.Minute),
			"sess-mc-hevc-"+uuid.NewString(), hevcFileID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO admin_playback_history
			(session_id, user_id, profile_id, media_item_id, media_file_id, play_method, started_at, ended_at, client_name, device_platform)
		VALUES ($1, $2, 'p-alice', 'movie-1', $3, 'transcode', $4, $5, 'RareApp', 'TV')`,
		"sess-rare-"+uuid.NewString(), userID, hevcFileID, base, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	devs, err := h.ReadAdminInsightsDevices(ctx, AdminInsightsDevicesQuery{
		AdminInsightsWindowQuery: AdminInsightsWindowQuery{Range: "7d", TZ: "UTC"}, MinSessions: 5,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	// A client under the matrix threshold still ranks among the transcoders.
	if slices.ContainsFunc(devs.Matrix, func(r AdminInsightsClientMatrixRow) bool { return r.ClientName == "RareApp" }) ||
		!slices.ContainsFunc(devs.TopTranscoders, func(r AdminInsightsTranscoderRow) bool { return r.ClientName == "RareApp" }) {
		t.Fatalf("RareApp: want it out of the min_sessions=5 matrix and in the transcoder ranking; ranking %+v", devs.TopTranscoders)
	}
	var merged *AdminInsightsClientMatrixRow
	for i := range devs.Matrix {
		if devs.Matrix[i].ClientName == "MixedCaseApp" || devs.Matrix[i].ClientName == "mixedcaseapp" {
			merged = &devs.Matrix[i]
			break
		}
	}
	if merged == nil || merged.TotalSessions != 6 || len(merged.Cells) != 2 {
		t.Fatalf("case-variant client should merge into 1 row of 6 sessions at min_sessions=5, got %+v", merged)
	}

	// Client names are client-reported, so the matrix caps how many it returns.
	if _, err := pool.Exec(ctx, `
		INSERT INTO admin_playback_history
			(session_id, user_id, profile_id, media_item_id, media_file_id, play_method, started_at, ended_at, client_name, device_platform)
		SELECT 'sess-cap-' || g || '-' || $3, $1, 'p-alice', 'movie-1', $2, 'direct', $4, $5, 'cap-client-' || g, 'Web'
		FROM generate_series(1, $6::int) g`,
		userID, h264FileID, uuid.NewString(), base, base.Add(time.Minute), insightsMatrixMaxRows+5); err != nil {
		t.Fatal(err)
	}
	capped, err := h.ReadAdminInsightsDevices(ctx, AdminInsightsDevicesQuery{
		AdminInsightsWindowQuery: AdminInsightsWindowQuery{Range: "7d", TZ: "UTC"}, MinSessions: 1,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped.Matrix) != insightsMatrixMaxRows ||
		!slices.ContainsFunc(capped.Matrix, func(r AdminInsightsClientMatrixRow) bool { return r.TotalSessions == 6 }) {
		t.Fatalf("matrix at min_sessions=1 has %d rows, want the cap of %d with the 6-session client kept", len(capped.Matrix), insightsMatrixMaxRows)
	}
}

func assertPeopleCountsFirstTimeLiveAccountDB(t *testing.T, pool *pgxpool.Pool, h *AdminHandler) {
	t.Helper()
	ctx := t.Context()
	var carolID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ('carol', 'user') RETURNING id`).Scan(&carolID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO playback_sessions_sync (session_id, user_id, profile_id) VALUES ('live-carol', $1, 'p-carol')`, carolID); err != nil {
		t.Fatal(err)
	}
	people, err := h.ReadAdminInsightsPeople(ctx, AdminInsightsPeopleQuery{
		AdminInsightsWindowQuery: AdminInsightsWindowQuery{Range: "7d", TZ: "UTC"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if people.LiveAccounts != 2 || people.ActiveAccounts != 2 {
		t.Fatalf("first-time live viewer undercounted: live=%d active=%d, want 2 and 2", people.LiveAccounts, people.ActiveAccounts)
	}
}

func assertHistoryLiteralLikeSearchDB(t *testing.T, pool *pgxpool.Pool, h *AdminHandler, userID int) {
	t.Helper()
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title)
		VALUES ('m-pct', 'movie', '100% Pure'),
		       ('m-num', 'movie', '1000 Miles'),
		       ('m-und', 'movie', 'A_B Test'),
		       ('m-axb', 'movie', 'AXB Test')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	for i, itemID := range []string{"m-pct", "m-num", "m-und", "m-axb"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO admin_playback_history
				(session_id, user_id, profile_id, media_item_id, media_file_id, play_method, started_at, ended_at)
			VALUES ($1, $2, 'p-alice', $3, 0, 'direct', $4, $5)`,
			"sess-like-"+uuid.NewString(), userID, itemID, now.Add(time.Duration(i)*time.Minute), now.Add(time.Duration(i+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	pctPage, err := h.ListAdminPlaybackHistoryPage(ctx, AdminPlaybackHistoryFilter{Search: "100%"}, nil, 10)
	if err != nil || len(pctPage.Items) != 1 || pctPage.Items[0].MediaItemID != "m-pct" {
		t.Fatalf("literal %% search got %+v (err=%v), want only m-pct", pctPage.Items, err)
	}
	undPage, err := h.ListAdminPlaybackHistoryPage(ctx, AdminPlaybackHistoryFilter{Search: "A_B"}, nil, 10)
	if err != nil || len(undPage.Items) != 1 || undPage.Items[0].MediaItemID != "m-und" {
		t.Fatalf("literal _ search got %+v (err=%v), want only m-und", undPage.Items, err)
	}
}

func TestResolveInsightsWindowMidnightDST(t *testing.T) {
	// America/Santiago springs forward from 00:00 (-04) directly to 01:00 (-03) on 2026-09-06.
	win, err := resolveInsightsWindow(
		time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
		InsightsRangeCustom, "2026-09-06", "2026-09-06", "America/Santiago",
	)
	if err != nil {
		t.Fatal(err)
	}
	wantFrom := time.Date(2026, 9, 6, 4, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, 9, 7, 3, 0, 0, 0, time.UTC)
	if !win.From.Equal(wantFrom) || !win.To.Equal(wantTo) {
		t.Fatalf("Santiago DST start day = [%v, %v), want [%v, %v)", win.From, win.To, wantFrom, wantTo)
	}
}

func TestMetricDaySpanPresetExactUTCDays(t *testing.T) {
	// 2026-11-22 05:00Z is still 2026-11-21 21:00 in America/Los_Angeles.
	// A 7d preset must still span exactly 7 UTC calendar days (2026-11-16..2026-11-22).
	nowUTC := time.Date(2026, 11, 22, 5, 0, 0, 0, time.UTC)
	win, err := resolveInsightsWindow(nowUTC, InsightsRange7d, "", "", "America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	fromDay, toDay, upperUTC := metricDaySpanForWindow(win, nowUTC)
	if fromDay != "2026-11-16" || toDay != "2026-11-22" || !upperUTC.Equal(nowUTC) {
		t.Fatalf("7d LA metricDaySpan = [%s, %s] upper=%v, want [2026-11-16, 2026-11-22]", fromDay, toDay, upperUTC)
	}
}
