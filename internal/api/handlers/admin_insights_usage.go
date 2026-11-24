package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// The Devices tab's client-version, protocol, subtitle and audio breakdowns.
// Like the rest of Devices they count sessions: each session has its own
// client, route and tracks.

// insightsClientVersionsLimit caps the client-version table. Names and
// versions are what clients report, so the cap bounds the response however
// many history holds.
const insightsClientVersionsLimit = 50

// insightsJellyfinClientSQL is true for a session from a Jellyfin-protocol
// client: one that came through the compatibility layer, or whose name
// carries one of jellyfinClientTokens (isJellyfinEcosystemClient's rule).
// tokensParam is a text[] parameter holding the tokens.
func insightsJellyfinClientSQL(tokensParam string) string {
	return fmt.Sprintf(`(COALESCE(h.compat_origin, FALSE) OR EXISTS (
		SELECT 1 FROM unnest(%s::text[]) AS tok
		WHERE strpos(LOWER(COALESCE(h.client_name, '')), tok) > 0))`, tokensParam)
}

func queryInsightsClientVersions(ctx context.Context, tx pgx.Tx, libraryID int, whereSQL string, whereArgs []any, out *AdminInsightsDevices) error {
	args := append(append([]any{}, whereArgs...), jellyfinClientTokens, insightsClientVersionsLimit)
	tokensParam, limitParam := len(args)-1, len(args)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT
			COALESCE(MIN(NULLIF(BTRIM(h.client_name), '')), '') AS client_name,
			COALESCE(MIN(NULLIF(BTRIM(h.device_platform), '')), '') AS device_platform,
			BTRIM(h.client_version) AS client_version,
			BOOL_OR(%[1]s) AS is_jellyfin,
			COUNT(*)::bigint AS sessions,
			MAX(h.ended_at) AS last_seen_at
		FROM admin_playback_history h %[2]s
		WHERE %[3]s AND NULLIF(BTRIM(COALESCE(h.client_version, '')), '') IS NOT NULL
		GROUP BY LOWER(COALESCE(NULLIF(BTRIM(h.client_name), ''), '')),
			LOWER(COALESCE(NULLIF(BTRIM(h.device_platform), ''), '')), BTRIM(h.client_version)
		ORDER BY 5 DESC, 6 DESC, 1, 2, 3
		LIMIT $%[4]d
	`, insightsJellyfinClientSQL(fmt.Sprintf("$%d", tokensParam)), insightsLibraryJoinSQL(libraryID), whereSQL, limitParam), args...)
	if err != nil {
		return fmt.Errorf("querying insights client versions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row AdminInsightsClientVersionRow
		if err := rows.Scan(&row.ClientName, &row.DevicePlatform, &row.ClientVersion, &row.IsJellyfinClient, &row.Sessions, &row.LastSeenAt); err != nil {
			return fmt.Errorf("scanning insights client version: %w", err)
		}
		row.LastSeenAt = normalizeTimePtrUTC(row.LastSeenAt)
		out.ClientVersions = append(out.ClientVersions, row)
	}
	return rows.Err()
}

// queryInsightsProtocolBuckets splits each day/week/month of sessions between
// Silo's own clients and Jellyfin-protocol clients, in the window's timezone.
func queryInsightsProtocolBuckets(ctx context.Context, tx pgx.Tx, win resolvedInsightsWindow, libraryID int, whereSQL string, whereArgs []any, out *AdminInsightsDevices) error {
	args := append(append([]any{}, whereArgs...), jellyfinClientTokens, win.BucketUnit, win.TZ)
	tokensParam, unitParam, tzParam := len(args)-2, len(args)-1, len(args)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		WITH b AS (
			SELECT date_trunc($%[1]d, h.started_at AT TIME ZONE $%[2]d) AS local_bucket, %[3]s AS is_jellyfin
			FROM admin_playback_history h %[4]s
			WHERE %[5]s
		)
		SELECT local_bucket AT TIME ZONE $%[2]d, to_char(local_bucket, 'YYYY-MM-DD'),
			COUNT(*) FILTER (WHERE NOT is_jellyfin)::bigint, COUNT(*) FILTER (WHERE is_jellyfin)::bigint
		FROM b GROUP BY local_bucket ORDER BY local_bucket
	`, unitParam, tzParam, insightsJellyfinClientSQL(fmt.Sprintf("$%d", tokensParam)), insightsLibraryJoinSQL(libraryID), whereSQL), args...)
	if err != nil {
		return fmt.Errorf("querying insights protocol buckets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var bucket AdminInsightsProtocolBucket
		var start time.Time
		if err := rows.Scan(&start, &bucket.LabelDate, &bucket.NativeSessions, &bucket.JellyfinSessions); err != nil {
			return fmt.Errorf("scanning insights protocol bucket: %w", err)
		}
		bucket.BucketStart = start.UTC()
		out.ProtocolBuckets = append(out.ProtocolBuckets, bucket)
	}
	return rows.Err()
}

// normalizeInsightsLanguage folds a track language onto one code per
// language ("eng" and "en" are one row). Empty and undetermined tags are
// unknown.
func normalizeInsightsLanguage(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" || strings.EqualFold(v, "und") || strings.EqualFold(v, insightsUnknownKey) {
		return insightsUnknownKey
	}
	code, err := subtitles.NormalizeLanguageCode(v)
	if err != nil || code == "" || code == "und" {
		return insightsUnknownKey
	}
	return code
}

// queryInsightsSubtitleUsage reads which subtitles sessions with route facts
// showed. A row without route facts may predate subtitle recording, so it is
// left out rather than counted as "no subtitle".
func queryInsightsSubtitleUsage(ctx context.Context, tx pgx.Tx, libraryID int, whereSQL string, whereArgs []any, out *AdminInsightsDevices) error {
	joinSQL := insightsLibraryJoinSQL(libraryID)
	scoped := whereSQL + ` AND h.route_facts_recorded`
	usage := &out.Subtitles
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)::bigint,
			COUNT(*) FILTER (WHERE h.subtitle IS NOT NULL)::bigint,
			COUNT(*) FILTER (WHERE (h.subtitle->>'forced')::boolean)::bigint,
			COUNT(*) FILTER (WHERE (h.subtitle->>'hearing_impaired')::boolean)::bigint
		FROM admin_playback_history h `+joinSQL+`
		WHERE `+scoped, whereArgs...).Scan(&usage.Sessions, &usage.SubtitledSessions, &usage.ForcedSessions, &usage.HearingImpairedSessions); err != nil {
		return fmt.Errorf("querying insights subtitle usage: %w", err)
	}
	var err error
	usage.Languages, err = queryLabeledCountMap(ctx, tx, `
		SELECT COALESCE(h.subtitle->>'language', ''), COUNT(*)::bigint
		FROM admin_playback_history h `+joinSQL+`
		WHERE `+scoped+` AND h.subtitle IS NOT NULL
		GROUP BY 1
	`, whereArgs, normalizeInsightsLanguage)
	if err != nil {
		return fmt.Errorf("querying insights subtitle languages: %w", err)
	}
	usage.Deliveries, err = queryLabeledCountMap(ctx, tx, `
		SELECT COALESCE(NULLIF(LOWER(BTRIM(h.subtitle->>'delivery')), ''), '`+insightsUnknownKey+`'), COUNT(*)::bigint
		FROM admin_playback_history h `+joinSQL+`
		WHERE `+scoped+` AND h.subtitle IS NOT NULL
		GROUP BY 1
	`, whereArgs, nil)
	if err != nil {
		return fmt.Errorf("querying insights subtitle deliveries: %w", err)
	}
	return nil
}

// insightsPlayedAudioTrackSQL is the audio track a history row played: the
// recorded index into its file's audio_tracks, else the file's default
// track, else its first (pickAdminAudioTrack's order).
const insightsPlayedAudioTrackSQL = `COALESCE(
	CASE WHEN h.audio_track_index IS NOT NULL THEN mf.audio_tracks -> h.audio_track_index END,
	(SELECT t FROM jsonb_array_elements(CASE WHEN jsonb_typeof(mf.audio_tracks) = 'array' THEN mf.audio_tracks ELSE '[]'::jsonb END) AS t
		WHERE (t->>'default')::boolean LIMIT 1),
	CASE WHEN jsonb_typeof(mf.audio_tracks) = 'array' THEN mf.audio_tracks -> 0 END)`

// queryInsightsAudioUsage reads the audio sessions with route facts played:
// the track's language, whether the audio was copied or converted, and the
// channels delivered (the target's when converted, else the track's).
func queryInsightsAudioUsage(ctx context.Context, tx pgx.Tx, whereSQL string, whereArgs []any, out *AdminInsightsDevices) error {
	usage := &out.Audio
	rows, err := tx.Query(ctx, `
		WITH a AS (
			SELECT h.audio_decision, h.target_audio_channels, mf.audio_channels AS file_channels,
				`+insightsPlayedAudioTrackSQL+` AS track
			FROM admin_playback_history h
			LEFT JOIN media_files mf ON mf.id = COALESCE(h.effective_media_file_id, h.media_file_id)
			LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
			WHERE `+whereSQL+` AND h.route_facts_recorded
		)
		SELECT
			COALESCE(track->>'language', ''),
			COALESCE(NULLIF(LOWER(BTRIM(audio_decision)), ''), '`+insightsUnknownKey+`'),
			CASE WHEN LOWER(BTRIM(COALESCE(audio_decision, ''))) = 'transcode' AND target_audio_channels > 0
				THEN target_audio_channels
				ELSE COALESCE(NULLIF(track->>'channels', '')::int, file_channels) END,
			COUNT(*)::bigint
		FROM a GROUP BY 1, 2, 3
	`, whereArgs...)
	if err != nil {
		return fmt.Errorf("querying insights audio usage: %w", err)
	}
	defer rows.Close()
	languages, decisions, channels := map[string]int64{}, map[string]int64{}, map[string]int64{}
	for rows.Next() {
		var language, decision string
		var delivered *int
		var sessions int64
		if err := rows.Scan(&language, &decision, &delivered, &sessions); err != nil {
			return fmt.Errorf("scanning insights audio usage: %w", err)
		}
		usage.Sessions += sessions
		languages[normalizeInsightsLanguage(language)] += sessions
		decisions[decision] += sessions
		channels[insightsChannelClass(delivered)] += sessions
	}
	if err := rows.Err(); err != nil {
		return err
	}
	usage.Languages = sortedLabeledCounts(languages)
	usage.Decisions = sortedLabeledCounts(decisions)
	usage.DeliveredChannels = sortedLabeledCounts(channels)
	return nil
}

// insightsChannelClass names a channel count: mono, stereo or surround.
func insightsChannelClass(channels *int) string {
	switch {
	case channels == nil || *channels <= 0:
		return insightsUnknownKey
	case *channels == 1:
		return "mono"
	case *channels == 2:
		return "stereo"
	default:
		return "surround"
	}
}
