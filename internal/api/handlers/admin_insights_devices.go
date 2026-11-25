package handlers

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func normalizeHWAccelKey(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "", insightsHWAutoKey, insightsUnknownKey:
		return insightsUnknownKey
	case AdminTerminateDeliveryNone, insightsHWSoftwareKey, "cpu":
		return insightsHWSoftwareKey
	default:
		return v
	}
}

func queryAdminInsightsDevices(ctx context.Context, pool *pgxpool.Pool, q AdminInsightsDevicesQuery) (*AdminInsightsDevices, error) {
	if pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	if err := validateInsightsScope(q.AdminInsightsWindowQuery); err != nil {
		return nil, err
	}
	ctx, tx, done, err := beginAdminHistoryRead(ctx, pool, insightsReadWorkMem)
	if err != nil {
		return nil, fmt.Errorf("beginning insights devices read: %w", err)
	}
	defer done()

	oldestHist, oldestMeas, oldestRoute, oldestMetric, dbNow, err := queryInsightsGlobalBounds(ctx, tx)
	if err != nil {
		return nil, err
	}
	win, err := resolveInsightsWindow(dbNow, q.Range, q.FromDate, q.ToDate, q.TZ)
	if err != nil {
		return nil, err
	}
	resolveAllTimeBounds(&win, oldestHist)
	whereSQL, whereArgs := buildInsightsHistoryWhere(win, q.LibraryID, q.UserID, q.ProfileID)

	out := &AdminInsightsDevices{
		From: win.From, To: win.To, TZ: win.TZ,
		Coverage: AdminInsightsCoverage{
			OldestHistoryAt: oldestHist, OldestMeasuredAt: oldestMeas,
			OldestRouteFactsAt: oldestRoute, OldestMetricAt: oldestMetric,
		},
		Codecs: make([]string, 0), Matrix: make([]AdminInsightsClientMatrixRow, 0),
		AdaptationReasons: make([]AdminInsightsAdaptationReason, 0),
		HWAccelBreakdown:  make([]AdminInsightsLabeledCount, 0), ToneMapBreakdown: make([]AdminInsightsLabeledCount, 0),
		SubtitleBurnInCodecs: make([]AdminInsightsLabeledCount, 0),
		TopTranscoders:       make([]AdminInsightsTranscoderRow, 0),
		SourceResolutions:    make([]AdminInsightsLabeledCount, 0), DeliveredResolutions: make([]AdminInsightsLabeledCount, 0),
		ResolutionChanges: make([]AdminInsightsResolutionChange, 0),
		BucketUnit:        win.BucketUnit, ClientVersions: make([]AdminInsightsClientVersionRow, 0),
		ProtocolBuckets: make([]AdminInsightsProtocolBucket, 0),
	}

	if err := queryInsightsDevicesSessions(ctx, tx, win, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := queryInsightsDevicesFiles(ctx, tx, q.LibraryID, whereSQL, whereArgs, clampInsightsMinSessions(q.MinSessions), out); err != nil {
		return nil, err
	}
	if err := queryInsightsClientVersions(ctx, tx, q.LibraryID, whereSQL, whereArgs, out); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing insights devices read: %w", err)
	}
	return out, nil
}

// The grouping sets of queryInsightsDevicesSessions, as GROUPING(k_hw,
// k_bucket, k_sub_rf, k_reasons) reports them: one bit per key, set when the
// row's set leaves that key out.
const (
	insightsDevicesSetSummary   = 0b0111
	insightsDevicesSetProtocol  = 0b1011
	insightsDevicesSetSubtitles = 0b1101
	insightsDevicesSetReasons   = 0b1110
)

// queryInsightsDevicesSessions reads, in one scan of the window's sessions,
// the summary KPIs with the HW-accel, tone-map and burn-in breakdowns, the
// adaptation reasons, the native/Jellyfin protocol buckets and subtitle
// usage, each from its own grouping set. The Jellyfin client check runs once
// per bucket and client name instead of once per session.
func queryInsightsDevicesSessions(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, libraryID int, whereSQL string, whereArgs []any, out *AdminInsightsDevices) error {
	query, args := insightsDevicesSessionsSQL(win, libraryID, whereSQL, whereArgs)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("querying insights devices sessions: %w", err)
	}
	defer rows.Close()
	tally := newInsightsDevicesSessionTally(out)
	for rows.Next() {
		var grp int
		var hw, tm, burn, bucketLabel, lang, delivery *string
		var bucketStart *time.Time
		var jellyfin, subRF, hasSub, forced, hearingImpaired *bool
		var reasonList []string
		var c insightsDevicesCounts
		if err := rows.Scan(&grp, &hw, &tm, &burn, &bucketStart, &bucketLabel, &jellyfin,
			&subRF, &hasSub, &forced, &hearingImpaired, &lang, &delivery, &reasonList,
			&c.total, &c.measured, &c.routeFacts, &c.direct, &c.remux, &c.transcode); err != nil {
			return fmt.Errorf("scanning insights devices sessions: %w", err)
		}
		switch grp {
		case insightsDevicesSetSummary:
			tally.addSummary(hw, tm, burn, c)
		case insightsDevicesSetProtocol:
			if bucketStart != nil && bucketLabel != nil {
				tally.addProtocol(*bucketStart, *bucketLabel, jellyfin != nil && *jellyfin, c.total)
			}
		case insightsDevicesSetSubtitles:
			if subRF != nil && *subRF {
				tally.subtitles.add(*hasSub, *forced, *hearingImpaired, *lang, *delivery, c.total)
			}
		case insightsDevicesSetReasons:
			tally.addReasons(reasonList, c.total)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	tally.finish()
	return nil
}

// insightsDevicesCounts are the session counts of one grouping-set row.
type insightsDevicesCounts struct {
	total, measured, routeFacts, direct, remux, transcode int64
}

// insightsDevicesSessionTally assembles queryInsightsDevicesSessions' reads.
type insightsDevicesSessionTally struct {
	out                               *AdminInsightsDevices
	hwAccel, toneMap, burnIn, reasons map[string]int64
	buckets                           map[time.Time]*AdminInsightsProtocolBucket
	subtitles                         *insightsSubtitleTally
}

func newInsightsDevicesSessionTally(out *AdminInsightsDevices) *insightsDevicesSessionTally {
	return &insightsDevicesSessionTally{
		out: out, hwAccel: map[string]int64{}, toneMap: map[string]int64{}, burnIn: map[string]int64{},
		reasons: map[string]int64{}, buckets: map[time.Time]*AdminInsightsProtocolBucket{},
		subtitles: newInsightsSubtitleTally(&out.Subtitles),
	}
}

func (t *insightsDevicesSessionTally) addSummary(hw, tm, burn *string, c insightsDevicesCounts) {
	sum := &t.out.Summary
	sum.TotalSessions += c.total
	t.out.Coverage.MeasuredSessions += c.measured
	sum.RouteFactsSessions += c.routeFacts
	sum.DirectSessions += c.direct
	sum.RemuxSessions += c.remux
	sum.TranscodeSessions += c.transcode
	if hw != nil {
		k := normalizeHWAccelKey(*hw)
		t.hwAccel[k] += c.total
		switch k {
		case insightsHWSoftwareKey:
			sum.SWTranscodeSessions += c.total
		case insightsUnknownKey:
		default:
			sum.HWTranscodeSessions += c.total
		}
	}
	if tm != nil {
		t.toneMap[*tm] += c.total
		sum.HDRToneMapSessions += c.total
		switch *tm {
		case "hardware":
			sum.HDRToneMapHWSessions += c.total
		case "software":
			sum.HDRToneMapSWSessions += c.total
		}
	}
	if burn != nil {
		t.burnIn[*burn] += c.total
		sum.SubtitleBurnInSessions += c.total
	}
}

func (t *insightsDevicesSessionTally) addProtocol(bucketStart time.Time, label string, jellyfin bool, sessions int64) {
	start := bucketStart.UTC()
	b := t.buckets[start]
	if b == nil {
		b = &AdminInsightsProtocolBucket{BucketStart: start, LabelDate: label}
		t.buckets[start] = b
	}
	if jellyfin {
		b.JellyfinSessions += sessions
	} else {
		b.NativeSessions += sessions
	}
}

func (t *insightsDevicesSessionTally) addReasons(reasons []string, sessions int64) {
	for _, reason := range reasons {
		if norm := playback.NormalizeAdaptationReasons([]string{reason}); len(norm) > 0 {
			t.reasons[norm[0]] += sessions
		}
	}
}

func (t *insightsDevicesSessionTally) finish() {
	out := t.out
	out.Summary.DirectRate = roundInsightsRate(out.Summary.DirectSessions, out.Summary.TotalSessions)
	out.Coverage.TotalSessions = out.Summary.TotalSessions
	out.Coverage.RouteFactsSessions = out.Summary.RouteFactsSessions
	out.HWAccelBreakdown = sortedLabeledCounts(t.hwAccel)
	out.ToneMapBreakdown = sortedLabeledCounts(t.toneMap)
	out.SubtitleBurnInCodecs = sortedLabeledCounts(t.burnIn)
	adaptedTotal := out.Summary.RemuxSessions + out.Summary.TranscodeSessions
	for reason, count := range t.reasons {
		out.AdaptationReasons = append(out.AdaptationReasons, AdminInsightsAdaptationReason{
			Reason: reason, Sessions: count, ShareOfAdapted: roundInsightsRate(count, adaptedTotal),
		})
	}
	slices.SortFunc(out.AdaptationReasons, func(a, b AdminInsightsAdaptationReason) int {
		if c := cmp.Compare(b.Sessions, a.Sessions); c != 0 {
			return c
		}
		return cmp.Compare(a.Reason, b.Reason)
	})
	for _, b := range t.buckets {
		out.ProtocolBuckets = append(out.ProtocolBuckets, *b)
	}
	slices.SortFunc(out.ProtocolBuckets, func(a, b AdminInsightsProtocolBucket) int {
		return a.BucketStart.Compare(b.BucketStart)
	})
	t.subtitles.finish()
}

// insightsDevicesSessionsTemplate is queryInsightsDevicesSessions' statement:
// %[1]s..%[3]s the direct, remux and transcode predicates, %[4]s the library
// join, %[5]s the window and scope, $%[6]d the bucket unit, $%[7]d the
// timezone, $%[8]d the client tokens, %[9]s the unknown key and %[10]d the
// protocol set's GROUPING value.
const insightsDevicesSessionsTemplate = `
		WITH g AS (
			SELECT GROUPING(k_hw, k_bucket, k_sub_rf, k_reasons) AS grp,
				k_hw, k_tm, k_burn, k_bucket, k_compat, k_client,
				k_sub_rf, k_has_sub, k_forced, k_hi, k_lang, k_delivery, k_reasons,
				COUNT(*)::bigint AS total,
				COUNT(*) FILTER (WHERE measured)::bigint AS measured,
				COUNT(*) FILTER (WHERE route_facts)::bigint AS route_facts,
				COUNT(*) FILTER (WHERE direct)::bigint AS direct,
				COUNT(*) FILTER (WHERE remux)::bigint AS remux,
				COUNT(*) FILTER (WHERE transcode)::bigint AS transcode
			FROM (
				SELECT h.measured, h.route_facts_recorded AS route_facts,
					%[1]s AS direct, %[2]s AS remux, %[3]s AS transcode,
					CASE WHEN %[3]s AND h.route_facts_recorded THEN COALESCE(h.transcode_hw_accel, '') END AS k_hw,
					NULLIF(LOWER(BTRIM(COALESCE(h.tone_map_mode, ''))), '') AS k_tm,
					CASE WHEN h.subtitle->>'delivery' = 'burn_in' THEN COALESCE(NULLIF(LOWER(BTRIM(h.subtitle->>'codec')), ''), 'unknown') END AS k_burn,
					date_trunc($%[6]d, h.started_at AT TIME ZONE $%[7]d) AS k_bucket,
					COALESCE(h.compat_origin, FALSE) AS k_compat,
					LOWER(COALESCE(h.client_name, '')) AS k_client,
					-- Subtitle usage counts only sessions with route facts: an
					-- older row may predate subtitle recording.
					h.route_facts_recorded AS k_sub_rf,
					CASE WHEN h.route_facts_recorded THEN h.subtitle IS NOT NULL END AS k_has_sub,
					CASE WHEN h.route_facts_recorded THEN COALESCE((h.subtitle->>'forced')::boolean, FALSE) END AS k_forced,
					CASE WHEN h.route_facts_recorded THEN COALESCE((h.subtitle->>'hearing_impaired')::boolean, FALSE) END AS k_hi,
					CASE WHEN h.route_facts_recorded THEN COALESCE(h.subtitle->>'language', '') END AS k_lang,
					CASE WHEN h.route_facts_recorded THEN COALESCE(NULLIF(LOWER(BTRIM(h.subtitle->>'delivery')), ''), '%[9]s') END AS k_delivery,
					CASE WHEN NOT (%[1]s) THEN h.adaptation_reasons END AS k_reasons
				FROM admin_playback_history h %[4]s
				WHERE %[5]s
			) s
			GROUP BY GROUPING SETS ((k_hw, k_tm, k_burn), (k_bucket, k_compat, k_client),
				(k_sub_rf, k_has_sub, k_forced, k_hi, k_lang, k_delivery), (k_reasons))
		)
		SELECT grp, k_hw, k_tm, k_burn, k_bucket AT TIME ZONE $%[7]d, to_char(k_bucket, 'YYYY-MM-DD'),
			CASE WHEN grp = %[10]d THEN k_compat OR EXISTS (
				SELECT 1 FROM unnest($%[8]d::text[]) AS tok WHERE strpos(k_client, tok) > 0) END,
			k_sub_rf, k_has_sub, k_forced, k_hi, k_lang, k_delivery, k_reasons,
			total, measured, route_facts, direct, remux, transcode
		FROM g
	`

func insightsDevicesSessionsSQL(win resolvedInsightsWindow, libraryID int, whereSQL string, whereArgs []any) (string, []any) {
	args := append(append([]any{}, whereArgs...), jellyfinClientTokens, win.BucketUnit, win.TZ)
	tokensParam, unitParam, tzParam := len(args)-2, len(args)-1, len(args)
	query := fmt.Sprintf(insightsDevicesSessionsTemplate, insightsPlayDirectSQL, insightsPlayRemuxSQL, insightsPlayTranscodeSQL, insightsLibraryJoinSQL(libraryID), whereSQL,
		unitParam, tzParam, tokensParam, insightsUnknownKey, insightsDevicesSetProtocol)
	return query, args
}

// insightsMatrixMaxRows caps the client and platform pairs the matrix returns,
// the ones with the most sessions. Client names and platforms are what clients
// report, so their number is whatever the clients make it; the cap keeps one
// read's memory and response bounded however many identities history holds.
const insightsMatrixMaxRows = 200

// The grouping sets of queryInsightsDevicesFiles, as GROUPING(client_key,
// resolution, language) reports them.
const (
	insightsDevicesFileSetMatrix     = 0b011
	insightsDevicesFileSetResolution = 0b101
	insightsDevicesFileSetAudio      = 0b110
)

// The kind column of queryInsightsDevicesFiles' rows.
const (
	insightsDevicesRowMatrix     = "matrix"
	insightsDevicesRowResolution = "resolution"
	insightsDevicesRowAudio      = "audio_usage"
)

// queryInsightsDevicesFiles reads the client codec matrix, the resolution
// breakdowns and audio usage: the Devices reads that need each session's media
// file. One scan of the window's sessions, each joined to its file once, feeds
// all three. Only the matrix's rows (over the threshold, within the cap) and
// the top transcoders (whatever their size) leave the database.
func queryInsightsDevicesFiles(ctx context.Context, tx pgx.Tx, libraryID int, whereSQL string, whereArgs []any, minSessions int, out *AdminInsightsDevices) error {
	query, args := insightsDevicesFilesSQL(libraryID, whereSQL, whereArgs, minSessions)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("querying insights devices files: %w", err)
	}
	defer rows.Close()
	matrix := newInsightsMatrixTally()
	resolutions := newInsightsResolutionTally()
	audio := newInsightsAudioTally(&out.Audio)
	// Each row's columns, by kind (see the SELECT): matrix rows carry client,
	// platform, codec, compat origin, in-matrix and four session counts;
	// resolution rows the file's label, play method, video decision, target
	// codec and resolution, width, height and route facts; audio rows the
	// language, decision and delivered channels. Sessions are always n1.
	for rows.Next() {
		var kind, t1, t2, t3, t4, t5 string
		var width, height int
		var channels *int
		var b1, b2 bool
		var n1, n2, n3, n4 int64
		if err := rows.Scan(&kind, &t1, &t2, &t3, &t4, &t5, &width, &height, &channels, &b1, &b2, &n1, &n2, &n3, &n4); err != nil {
			return fmt.Errorf("scanning insights devices files: %w", err)
		}
		switch kind {
		case insightsDevicesRowMatrix:
			matrix.add(t1, t2, t3, b1, b2, n1, n2, n3, n4)
		case insightsDevicesRowResolution:
			resolutions.add(t1, width, height, t2, t3, t4, t5, b1, n1)
		case insightsDevicesRowAudio:
			audio.add(t1, t2, channels, n1)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	matrix.finish(out)
	resolutions.finish(out)
	audio.finish()
	return nil
}

// insightsDevicesFilesTemplate is queryInsightsDevicesFiles' statement: %[1]s..%[3]s
// the direct, remux and transcode predicates, %[4]s the window and scope,
// $%[5]d..$%[7]d the matrix threshold, cap and top-transcoder count, %[8]s the
// file join, %[9]s the unknown key, %[10]d..%[12]d the GROUPING values of the
// matrix, resolution and audio sets, %[13]s the file alias and %[14]s..%[16]s
// the row kinds.
const insightsDevicesFilesTemplate = `
		WITH g AS (
			-- One pass over the window's sessions joined to their files, one
			-- grouping set per read: the matrix's cells, the resolution facts,
			-- and audio. No CTE sits under it, so the scan and the file join
			-- can run in parallel.
			SELECT GROUPING(client_key, resolution, language) AS grp,
				client_key, platform_key, source_codec,
				MIN(client_name) AS client_name, MIN(device_platform) AS device_platform,
				BOOL_OR(compat_origin) AS compat_origin,
				resolution, width, height, play_method, video_decision, target_video_codec, target_resolution, route_facts,
				language, audio_decision, delivered,
				COUNT(*)::bigint AS sessions,
				COUNT(*) FILTER (WHERE direct)::bigint AS direct_sessions,
				COUNT(*) FILTER (WHERE remux)::bigint AS remux_sessions,
				COUNT(*) FILTER (WHERE transcode)::bigint AS transcode_sessions
			FROM (
				SELECT
					LOWER(COALESCE(NULLIF(BTRIM(h.client_name), ''), 'unknown')) AS client_key,
					LOWER(COALESCE(NULLIF(BTRIM(h.device_platform), ''), 'unknown')) AS platform_key,
					NULLIF(BTRIM(h.client_name), '') AS client_name,
					NULLIF(BTRIM(h.device_platform), '') AS device_platform,
					COALESCE(h.compat_origin, FALSE) AS compat_origin,
					LOWER(COALESCE(NULLIF(BTRIM(h.codec_video), ''), 'unknown')) AS source_codec,
					%[1]s AS direct, %[2]s AS remux, %[3]s AS transcode,
					COALESCE(h.resolution, '') AS resolution,
					CASE WHEN h.video_tracks->0->>'width' ~ '^[0-9]{1,5}$' THEN (h.video_tracks->0->>'width')::int ELSE 0 END AS width,
					CASE WHEN h.video_tracks->0->>'height' ~ '^[0-9]{1,5}$' THEN (h.video_tracks->0->>'height')::int ELSE 0 END AS height,
					LOWER(BTRIM(h.play_method)) AS play_method,
					LOWER(BTRIM(COALESCE(h.video_decision, ''))) AS video_decision,
					LOWER(BTRIM(COALESCE(h.target_video_codec, ''))) AS target_video_codec,
					COALESCE(h.target_resolution, '') AS target_resolution,
					h.route_facts_recorded AS route_facts,
					-- Audio usage counts only sessions with route facts. The
					-- track is computed here, in the projection, so it only
					-- reads files of sessions the window and filters kept.
					CASE WHEN h.route_facts_recorded THEN COALESCE(h.track->>'language', '') END AS language,
					CASE WHEN h.route_facts_recorded THEN h.decision END AS audio_decision,
					CASE WHEN NOT h.route_facts_recorded THEN NULL
						WHEN h.decision = 'transcode' AND h.target_audio_channels > 0 THEN h.target_audio_channels
						ELSE COALESCE(NULLIF(h.track->>'channels', '')::int, h.file_channels) END AS delivered
				FROM (
					SELECT h.*, %[13]s.codec_video, %[13]s.resolution, %[13]s.video_tracks,
						%[13]s.audio_channels AS file_channels,
						-- The track a session played: its recorded index into
						-- the file's audio_tracks, else the file's default
						-- track, else its first (pickAdminAudioTrack's order).
						CASE WHEN h.route_facts_recorded THEN COALESCE(
							CASE WHEN h.audio_track_index IS NOT NULL THEN %[13]s.audio_tracks -> h.audio_track_index END,
							(SELECT t FROM jsonb_array_elements(CASE WHEN jsonb_typeof(%[13]s.audio_tracks) = 'array' THEN %[13]s.audio_tracks ELSE '[]'::jsonb END) AS t
								WHERE (t->>'default')::boolean LIMIT 1),
							CASE WHEN jsonb_typeof(%[13]s.audio_tracks) = 'array' THEN %[13]s.audio_tracks -> 0 END) END AS track,
						COALESCE(NULLIF(LOWER(BTRIM(h.audio_decision)), ''), '%[9]s') AS decision
					FROM admin_playback_history h %[8]s
					WHERE %[4]s
				) h
			) s
			GROUP BY GROUPING SETS (
				(client_key, platform_key, source_codec),
				(resolution, width, height, play_method, video_decision, target_video_codec, target_resolution, route_facts),
				(language, audio_decision, delivered, route_facts))
		),
		cells AS (
			SELECT client_key, platform_key,
				COALESCE(client_name, 'Unknown') AS client_name,
				COALESCE(device_platform, 'Unknown') AS device_platform,
				source_codec, compat_origin, sessions, direct_sessions, remux_sessions, transcode_sessions
			FROM g WHERE grp = %[10]d
		),
		pairs AS (
			SELECT client_key, platform_key,
				SUM(sessions) AS total, SUM(transcode_sessions) AS transcodes
			FROM cells
			GROUP BY 1, 2
		),
		ranked AS (
			SELECT client_key, platform_key,
				(total >= $%[5]d AND ROW_NUMBER() OVER (
					ORDER BY (total >= $%[5]d) DESC, total DESC, client_key, platform_key) <= $%[6]d) AS in_matrix,
				(transcodes > 0 AND ROW_NUMBER() OVER (
					ORDER BY transcodes DESC, total DESC, client_key, platform_key) <= $%[7]d) AS top_transcoder
			FROM pairs
		)
		SELECT '%[14]s', c.client_name, c.device_platform, c.source_codec, '', '', 0, 0, NULL::int,
			c.compat_origin, r.in_matrix, c.sessions, c.direct_sessions, c.remux_sessions, c.transcode_sessions
		FROM cells c
		JOIN ranked r USING (client_key, platform_key)
		WHERE r.in_matrix OR r.top_transcoder
		UNION ALL
		SELECT '%[15]s', resolution, play_method, video_decision, target_video_codec, target_resolution, width, height, NULL,
			route_facts, FALSE, sessions, 0, 0, 0
		FROM g WHERE grp = %[11]d
		UNION ALL
		SELECT '%[16]s', language, audio_decision, '', '', '', 0, 0, delivered, FALSE, FALSE, sessions, 0, 0, 0
		FROM g WHERE grp = %[12]d AND route_facts
	`

func insightsDevicesFilesSQL(libraryID int, whereSQL string, whereArgs []any, minSessions int) (string, []any) {
	args := append(append([]any{}, whereArgs...), minSessions, insightsMatrixMaxRows, insightsTopTranscodersLimit)
	minParam, capParam, topParam := len(args)-2, len(args)-1, len(args)
	// A library filter already joins each session's file as mf; reuse it.
	fileJoin, fileAlias := insightsLibraryJoinSQL(libraryID), "mf"
	if libraryID <= 0 {
		fileJoin, fileAlias = "LEFT JOIN media_files fm ON fm.id = COALESCE(h.effective_media_file_id, h.media_file_id)", "fm"
	}
	query := fmt.Sprintf(insightsDevicesFilesTemplate, insightsPlayDirectSQL, insightsPlayRemuxSQL, insightsPlayTranscodeSQL, whereSQL, minParam, capParam, topParam,
		fileJoin, insightsUnknownKey,
		insightsDevicesFileSetMatrix, insightsDevicesFileSetResolution, insightsDevicesFileSetAudio, fileAlias,
		insightsDevicesRowMatrix, insightsDevicesRowResolution, insightsDevicesRowAudio)
	return query, args
}

type insightsMatrixKey struct{ client, platform string }

// insightsMatrixTally assembles the client codec matrix from its cells.
type insightsMatrixTally struct {
	rows     map[insightsMatrixKey]*AdminInsightsClientMatrixRow
	inMatrix map[insightsMatrixKey]bool
}

func newInsightsMatrixTally() *insightsMatrixTally {
	return &insightsMatrixTally{
		rows:     make(map[insightsMatrixKey]*AdminInsightsClientMatrixRow),
		inMatrix: make(map[insightsMatrixKey]bool),
	}
}

func (m *insightsMatrixTally) add(clientName, platform, codec string, compatOrigin, matrixRow bool, sessions, direct, remux, transcode int64) {
	k := insightsMatrixKey{client: strings.ToLower(clientName), platform: strings.ToLower(platform)}
	row := m.rows[k]
	if row == nil {
		row = &AdminInsightsClientMatrixRow{
			ClientName: clientName, DevicePlatform: platform,
			IsJellyfinClient: compatOrigin || isJellyfinEcosystemClient(clientName, ""),
			Cells:            make([]AdminInsightsClientCodecCell, 0, 4),
		}
		m.rows[k] = row
	} else {
		mergeMatrixRowIdentity(row, clientName, platform, compatOrigin)
	}
	m.inMatrix[k] = matrixRow
	row.TotalSessions += sessions
	row.DirectSessions += direct
	row.RemuxSessions += remux
	row.TranscodeSessions += transcode
	row.Cells = append(row.Cells, AdminInsightsClientCodecCell{
		SourceVideoCodec: codec, Sessions: sessions, DirectSessions: direct,
		RemuxSessions: remux, TranscodeSessions: transcode, DirectRate: roundInsightsRate(direct, sessions),
	})
}

func (m *insightsMatrixTally) finish(out *AdminInsightsDevices) {
	out.TopTranscoders = topInsightsTranscoders(slices.Collect(maps.Values(m.rows)))
	codecTotals := make(map[string]int64)
	for k, row := range m.rows {
		if !m.inMatrix[k] {
			continue
		}
		row.DirectRate = roundInsightsRate(row.DirectSessions, row.TotalSessions)
		for _, cell := range row.Cells {
			codecTotals[cell.SourceVideoCodec] += cell.Sessions
		}
		out.Matrix = append(out.Matrix, *row)
	}
	orderClientCodecMatrix(out, codecTotals)
}

func mergeMatrixRowIdentity(row *AdminInsightsClientMatrixRow, clientName, platform string, compatOrigin bool) {
	if clientName < row.ClientName {
		row.ClientName = clientName
	}
	if platform < row.DevicePlatform {
		row.DevicePlatform = platform
	}
	if compatOrigin {
		row.IsJellyfinClient = true
	}
}

func orderClientCodecMatrix(out *AdminInsightsDevices, codecTotals map[string]int64) {
	slices.SortFunc(out.Matrix, func(a, b AdminInsightsClientMatrixRow) int {
		if c := cmp.Compare(b.TotalSessions, a.TotalSessions); c != 0 {
			return c
		}
		if c := cmp.Compare(a.ClientName, b.ClientName); c != 0 {
			return c
		}
		return cmp.Compare(a.DevicePlatform, b.DevicePlatform)
	})
	for codec := range codecTotals {
		out.Codecs = append(out.Codecs, codec)
	}
	slices.SortFunc(out.Codecs, func(a, b string) int {
		if a == insightsUnknownKey && b != insightsUnknownKey {
			return 1
		}
		if b == insightsUnknownKey && a != insightsUnknownKey {
			return -1
		}
		if c := cmp.Compare(codecTotals[b], codecTotals[a]); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	rank := make(map[string]int, len(out.Codecs))
	for i, c := range out.Codecs {
		rank[c] = i
	}
	for i := range out.Matrix {
		slices.SortFunc(out.Matrix[i].Cells, func(a, b AdminInsightsClientCodecCell) int {
			return cmp.Compare(rank[a.SourceVideoCodec], rank[b.SourceVideoCodec])
		})
	}
}

func queryLabeledCountMap(ctx context.Context, tx pgx.Tx, sql string, args []any, normalize func(string) string) ([]AdminInsightsLabeledCount, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int64)
	for rows.Next() {
		var k string
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if normalize != nil {
			k = normalize(k)
		}
		counts[k] += v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sortedLabeledCounts(counts), nil
}

func sortedLabeledCounts(m map[string]int64) []AdminInsightsLabeledCount {
	res := make([]AdminInsightsLabeledCount, 0, len(m))
	for k, v := range m {
		res = append(res, AdminInsightsLabeledCount{Key: k, Sessions: v})
	}
	slices.SortFunc(res, func(a, b AdminInsightsLabeledCount) int {
		if a.Key == insightsUnknownKey && b.Key != insightsUnknownKey {
			return 1
		}
		if b.Key == insightsUnknownKey && a.Key != insightsUnknownKey {
			return -1
		}
		if c := cmp.Compare(b.Sessions, a.Sessions); c != 0 {
			return c
		}
		return cmp.Compare(a.Key, b.Key)
	})
	return res
}

// insightsTopTranscodersLimit caps the Devices tab's transcoder ranking.
const insightsTopTranscodersLimit = 10

// topInsightsTranscoders ranks clients by transcoded sessions. It ignores the
// matrix's minimum-session threshold, which keeps direct-play rates from
// resting on a handful of sessions; a client that transcodes still belongs in
// this ranking however few sessions it has.
func topInsightsTranscoders(rows []*AdminInsightsClientMatrixRow) []AdminInsightsTranscoderRow {
	out := make([]AdminInsightsTranscoderRow, 0, insightsTopTranscodersLimit)
	for _, row := range rows {
		if row.TranscodeSessions > 0 {
			out = append(out, AdminInsightsTranscoderRow{
				ClientName: row.ClientName, DevicePlatform: row.DevicePlatform, IsJellyfinClient: row.IsJellyfinClient,
				TotalSessions: row.TotalSessions, TranscodeSessions: row.TranscodeSessions,
			})
		}
	}
	slices.SortFunc(out, func(a, b AdminInsightsTranscoderRow) int {
		if c := cmp.Compare(b.TranscodeSessions, a.TranscodeSessions); c != 0 {
			return c
		}
		if c := cmp.Compare(b.TotalSessions, a.TotalSessions); c != 0 {
			return c
		}
		if c := cmp.Compare(strings.ToLower(a.ClientName), strings.ToLower(b.ClientName)); c != 0 {
			return c
		}
		return cmp.Compare(strings.ToLower(a.DevicePlatform), strings.ToLower(b.DevicePlatform))
	})
	if len(out) > insightsTopTranscodersLimit {
		out = out[:insightsTopTranscodersLimit]
	}
	return out
}
