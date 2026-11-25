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
// showed in a single scan of admin_playback_history. A row without route facts
// may predate subtitle recording, so it is left out rather than counted as "no
// subtitle".
func queryInsightsSubtitleUsage(ctx context.Context, tx pgx.Tx, libraryID int, whereSQL string, whereArgs []any, out *AdminInsightsDevices) error {
	usage := &out.Subtitles
	rows, err := tx.Query(ctx, `
		SELECT
			(h.subtitle IS NOT NULL),
			COALESCE((h.subtitle->>'forced')::boolean, FALSE),
			COALESCE((h.subtitle->>'hearing_impaired')::boolean, FALSE),
			COALESCE(h.subtitle->>'language', ''),
			COALESCE(NULLIF(LOWER(BTRIM(h.subtitle->>'delivery')), ''), '`+insightsUnknownKey+`'),
			COUNT(*)::bigint
		FROM admin_playback_history h `+insightsLibraryJoinSQL(libraryID)+`
		WHERE `+whereSQL+` AND h.route_facts_recorded
		GROUP BY 1, 2, 3, 4, 5
	`, whereArgs...)
	if err != nil {
		return fmt.Errorf("querying insights subtitle usage: %w", err)
	}
	defer rows.Close()
	languages, deliveries := map[string]int64{}, map[string]int64{}
	for rows.Next() {
		var hasSubtitle, forced, hearingImpaired bool
		var language, delivery string
		var sessions int64
		if err := rows.Scan(&hasSubtitle, &forced, &hearingImpaired, &language, &delivery, &sessions); err != nil {
			return fmt.Errorf("scanning insights subtitle usage: %w", err)
		}
		usage.Sessions += sessions
		if !hasSubtitle {
			continue
		}
		usage.SubtitledSessions += sessions
		if forced {
			usage.ForcedSessions += sessions
		}
		if hearingImpaired {
			usage.HearingImpairedSessions += sessions
		}
		languages[normalizeInsightsLanguage(language)] += sessions
		deliveries[delivery] += sessions
	}
	if err := rows.Err(); err != nil {
		return err
	}
	usage.Languages = sortedLabeledCounts(languages)
	usage.Deliveries = sortedLabeledCounts(deliveries)
	return nil
}

// queryInsightsAudioUsage reads the audio sessions with route facts played:
// the track's language, whether the audio was copied or converted, and the
// channels delivered (the target's when converted, else the track's).
// Sessions are pre-grouped by file before probing media_files so each unique
// file's fallback audio track is resolved once.
func queryInsightsAudioUsage(ctx context.Context, tx pgx.Tx, libraryID int, whereSQL string, whereArgs []any, out *AdminInsightsDevices) error {
	usage := &out.Audio
	rows, err := tx.Query(ctx, `
		WITH h_grouped AS (
			SELECT
				COALESCE(h.effective_media_file_id, h.media_file_id) AS file_id,
				h.audio_track_index,
				COALESCE(NULLIF(LOWER(BTRIM(h.audio_decision)), ''), '`+insightsUnknownKey+`') AS audio_decision,
				h.target_audio_channels,
				COUNT(*)::bigint AS sessions
			FROM admin_playback_history h `+insightsLibraryJoinSQL(libraryID)+`
			WHERE `+whereSQL+` AND h.route_facts_recorded
			GROUP BY 1, 2, 3, 4
		),
		files AS (
			SELECT
				mf.id,
				mf.audio_channels AS file_channels,
				mf.audio_tracks,
				COALESCE(
					(SELECT t FROM jsonb_array_elements(CASE WHEN jsonb_typeof(mf.audio_tracks) = 'array' THEN mf.audio_tracks ELSE '[]'::jsonb END) AS t
						WHERE (t->>'default')::boolean LIMIT 1),
					CASE WHEN jsonb_typeof(mf.audio_tracks) = 'array' THEN mf.audio_tracks -> 0 END
				) AS fallback_track
			FROM (SELECT DISTINCT file_id FROM h_grouped WHERE file_id IS NOT NULL) d
			JOIN media_files mf ON mf.id = d.file_id
		),
		a AS (
			SELECT g.audio_decision, g.target_audio_channels, g.sessions, f.file_channels,
				COALESCE(
					CASE WHEN g.audio_track_index IS NOT NULL THEN f.audio_tracks -> g.audio_track_index END,
					f.fallback_track
				) AS track
			FROM h_grouped g
			LEFT JOIN files f ON f.id = g.file_id
		)
		SELECT
			COALESCE(track->>'language', ''),
			audio_decision,
			CASE WHEN audio_decision = 'transcode' AND target_audio_channels > 0
				THEN target_audio_channels
				ELSE COALESCE(NULLIF(track->>'channels', '')::int, file_channels) END,
			SUM(sessions)::bigint
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
