package handlers

import (
	"context"
	"fmt"
	"strings"

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

func queryInsightsClientVersions(ctx context.Context, tx pgx.Tx, libraryID int, whereSQL string, whereArgs []any, out *AdminInsightsDevices) error {
	args := append(append([]any{}, whereArgs...), jellyfinClientTokens, insightsClientVersionsLimit)
	tokensParam, limitParam := len(args)-1, len(args)
	// The Jellyfin client check runs once per group, on its lower-cased,
	// trimmed name: trimming never changes whether a name contains a token.
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT client_name, device_platform, client_version,
			compat OR EXISTS (SELECT 1 FROM unnest($%[1]d::text[]) AS tok WHERE strpos(client_key, tok) > 0),
			sessions, last_seen_at
		FROM (
			SELECT
				LOWER(COALESCE(NULLIF(BTRIM(h.client_name), ''), '')) AS client_key,
				COALESCE(MIN(NULLIF(BTRIM(h.client_name), '')), '') AS client_name,
				COALESCE(MIN(NULLIF(BTRIM(h.device_platform), '')), '') AS device_platform,
				BTRIM(h.client_version) AS client_version,
				BOOL_OR(COALESCE(h.compat_origin, FALSE)) AS compat,
				COUNT(*)::bigint AS sessions,
				MAX(h.ended_at) AS last_seen_at
			FROM admin_playback_history h %[2]s
			WHERE %[3]s AND NULLIF(BTRIM(COALESCE(h.client_version, '')), '') IS NOT NULL
			GROUP BY 1, LOWER(COALESCE(NULLIF(BTRIM(h.device_platform), ''), '')), BTRIM(h.client_version)
		) v
		ORDER BY 5 DESC, 6 DESC, 1, 2, 3
		LIMIT $%[4]d
	`, tokensParam, insightsLibraryJoinSQL(libraryID), whereSQL, limitParam), args...)
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

// insightsSubtitleTally sums subtitle usage over sessions with route facts.
type insightsSubtitleTally struct {
	usage                 *AdminInsightsSubtitleUsage
	languages, deliveries map[string]int64
}

func newInsightsSubtitleTally(usage *AdminInsightsSubtitleUsage) *insightsSubtitleTally {
	return &insightsSubtitleTally{usage: usage, languages: map[string]int64{}, deliveries: map[string]int64{}}
}

func (s *insightsSubtitleTally) add(hasSubtitle, forced, hearingImpaired bool, language, delivery string, sessions int64) {
	s.usage.Sessions += sessions
	if !hasSubtitle {
		return
	}
	s.usage.SubtitledSessions += sessions
	if forced {
		s.usage.ForcedSessions += sessions
	}
	if hearingImpaired {
		s.usage.HearingImpairedSessions += sessions
	}
	s.languages[normalizeInsightsLanguage(language)] += sessions
	s.deliveries[delivery] += sessions
}

func (s *insightsSubtitleTally) finish() {
	s.usage.Languages = sortedLabeledCounts(s.languages)
	s.usage.Deliveries = sortedLabeledCounts(s.deliveries)
}

// insightsAudioTally sums the audio sessions with route facts played: the
// track's language, whether the audio was copied or converted, and the
// channels delivered (the target's when converted, else the track's).
type insightsAudioTally struct {
	usage                          *AdminInsightsAudioUsage
	languages, decisions, channels map[string]int64
}

func newInsightsAudioTally(usage *AdminInsightsAudioUsage) *insightsAudioTally {
	return &insightsAudioTally{usage: usage, languages: map[string]int64{}, decisions: map[string]int64{}, channels: map[string]int64{}}
}

func (a *insightsAudioTally) add(language, decision string, delivered *int, sessions int64) {
	a.usage.Sessions += sessions
	a.languages[normalizeInsightsLanguage(language)] += sessions
	a.decisions[decision] += sessions
	a.channels[insightsChannelClass(delivered)] += sessions
}

func (a *insightsAudioTally) finish() {
	a.usage.Languages = sortedLabeledCounts(a.languages)
	a.usage.Decisions = sortedLabeledCounts(a.decisions)
	a.usage.DeliveredChannels = sortedLabeledCounts(a.channels)
}

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
