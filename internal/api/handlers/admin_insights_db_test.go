package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	if act.Summary.Plays != 3 || act.Summary.Movies != 2 || act.Summary.Episodes != 1 ||
		act.Summary.MeasuredPlays != 2 || act.Coverage.RouteFactsPlays != 2 ||
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
		len(people.Accounts) != 2 || people.Accounts[0].Username != "alice" || !people.Accounts[0].IsLive || len(people.Accounts[0].Profiles) != 2 {
		t.Fatalf("unexpected people: %+v", people)
	}
}

func assertInsightsDevicesDB(t *testing.T, h *AdminHandler) {
	t.Helper()
	devs, err := h.ReadAdminInsightsDevices(t.Context(), AdminInsightsDevicesQuery{
		AdminInsightsWindowQuery: AdminInsightsWindowQuery{Range: "7d", TZ: "UTC"}, MinPlays: 1,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if devs.Summary.TotalPlays != 3 || devs.Summary.HWTranscodePlays != 1 || devs.Summary.SubtitleBurnInPlays != 1 ||
		devs.Summary.HDRToneMapHWPlays != 1 || len(devs.Matrix) != 2 || len(devs.AdaptationReasons) != 2 ||
		len(devs.TopTranscodingAccount) != 1 || devs.TopTranscodingAccount[0].Username != "bob" {
		t.Fatalf("unexpected devices: %+v", devs)
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
		bw.Summary.RemotePlays != 1 || bw.Summary.LocalPlays != 1 || bw.Summary.UnknownLocationPlays != 1 {
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
