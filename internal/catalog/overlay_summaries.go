package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/overlays"
)

// overlaySummaryResolutionRankSQL mirrors overlays.resolutionRank, including
// signed values and leading zeroes accepted by strconv.Atoi. Canonical probe
// values avoid regex matching for every file in an episode-heavy series.
const overlaySummaryNormalizedResolutionSQL = `lower(btrim(coalesce(mf.resolution, ''), ` + SQLTrimSpaceChars + `))`
const overlaySummaryResolutionNumberSQL = `substring(` + overlaySummaryNormalizedResolutionSQL + ` from '^([+-]?0*[0-9]{1,19})p$')::numeric`
const overlaySummaryResolutionRankSQL = `CASE ` + overlaySummaryNormalizedResolutionSQL + `
				WHEN '480p' THEN 480 WHEN '720p' THEN 720 WHEN '1080p' THEN 1080
				WHEN '2160p' THEN 2160 WHEN '4320p' THEN 4320 WHEN '4k' THEN 2160 WHEN 'uhd' THEN 2160
				ELSE CASE WHEN ` + overlaySummaryNormalizedResolutionSQL + ` ~ '^[+-]?0*[0-9]{1,19}p$'
					THEN CASE WHEN ` + overlaySummaryResolutionNumberSQL + ` BETWEEN -9223372036854775808 AND 9223372036854775807
						THEN ` + overlaySummaryResolutionNumberSQL + ` ELSE 0 END
					ELSE 0 END
			END`

// The JSON path regex uses exactly the Go TrimSpace rune set documented by
// SQLTrimSpaceChars. PostgreSQL's locale-dependent [:space:] omits characters
// such as NBSP, changing the winner for equal-resolution HDR files.
const overlaySummarySpaceRegex = `[\u0009-\u000d \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]`

// overlaySummaryRangeRankSQL mirrors overlays.rangeRank. Dolby Vision reads
// video_range_type verbatim; HDR detection trims it. JSON null is not a DV tag.
const overlaySummaryRangeRankSQL = `CASE
				WHEN jsonb_path_exists(coalesce(mf.video_tracks, '[]'::jsonb),
					'$[*] ? ((@.dolby_vision.type() == "string" && @.dolby_vision != "") || @.dv_profile > 0 || @.video_range_type like_regex "^DOVI")') THEN 3
				WHEN jsonb_path_exists(coalesce(mf.video_tracks, '[]'::jsonb),
					'$[*] ? (@.hdr10_plus == true || @.video_range_type like_regex "HDR10Plus" || @.video_range_type like_regex "^` + overlaySummarySpaceRegex + `*HDR10` + overlaySummarySpaceRegex + `*$" || @.video_range_type like_regex "WithHDR10` + overlaySummarySpaceRegex + `*$" || @.video_range_type like_regex "^` + overlaySummarySpaceRegex + `*HLG` + overlaySummarySpaceRegex + `*$" || @.video_range_type like_regex "WithHLG` + overlaySummarySpaceRegex + `*$" || @.color_transfer like_regex "smpte2084" flag "i" || @.color_transfer like_regex "arib-std-b67" flag "i")') THEN 2
				WHEN coalesce(mf.hdr, false) THEN 1
				ELSE 0
			END`

// overlaySummaryHasSubtitlesSQL is whether a file has any embedded or external
// subtitle track, the only fact overlays takes from those columns. Deciding it
// in SQL keeps the largest JSON columns out of every card's row.
const overlaySummaryHasSubtitlesSQL = `(
			CASE jsonb_typeof(mf.subtitle_tracks) WHEN 'array' THEN jsonb_array_length(mf.subtitle_tracks) > 0 ELSE false END
			OR CASE jsonb_typeof(mf.external_subtitles) WHEN 'array' THEN jsonb_array_length(mf.external_subtitles) > 0 ELSE false END)`

// ListOverlaySummaries reduces accessible files to one badge winner per card.
// Sections retain their existing content/episode/file tie order; catalog lists
// use file ID, matching their scanner projections.
func ListOverlaySummaries(ctx context.Context, pool *pgxpool.Pool, contentIDs []string, filter AccessFilter) (map[string]*models.OverlaySummary, error) {
	return listOverlaySummaries(ctx, pool, contentIDs, filter, "content_id ASC, episode_id ASC, id ASC")
}

// ListOverlaySummaries reads only each displayed item's winning file metadata.
// A repository without a pool returns an error so callers use their file
// projection rather than treating every card as having no badges.
func (r *ItemRepository) ListOverlaySummaries(ctx context.Context, contentIDs []string, filter AccessFilter) (map[string]*models.OverlaySummary, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("listing overlay summaries: item repository has no database pool")
	}
	return listOverlaySummaries(ctx, r.pool, contentIDs, filter, "id ASC")
}

func listOverlaySummaries(ctx context.Context, pool *pgxpool.Pool, contentIDs []string, filter AccessFilter, tieOrder string) (map[string]*models.OverlaySummary, error) {
	summaries := make(map[string]*models.OverlaySummary, len(contentIDs))
	if pool == nil || len(contentIDs) == 0 {
		return summaries, nil
	}

	args := []any{contentIDs}
	conditions := []string{
		playableFileExists,
		"g.group_key = ANY($1)",
	}
	accessConditions, args := MediaFileAccessSQL("mf", filter, args)
	conditions = append(conditions, accessConditions...)

	// Filter through the existing content/episode indexes before expanding the
	// requested groups. Sort narrow candidates, then fetch each winner's JSON.
	// A multi-episode file also badges each later episode of its range.
	query := fmt.Sprintf(`
		WITH served AS MATERIALIZED (
			-- Collected once: inlined, the planner can rerun the union for
			-- every media_files row it scans.
			SELECT linked.id AS file_id, linked_group.group_key
			FROM media_files linked
			CROSS JOIN LATERAL (VALUES (linked.content_id), (linked.episode_id)) AS linked_group(group_key)
			WHERE linked.content_id = ANY($1) OR linked.episode_id = ANY($1)
			UNION ALL
			SELECT mf.id, covered.content_id
			FROM episodes covered`+spannedEpisodeJoinSQL+`
			WHERE covered.content_id = ANY($1)
		), winners AS (
			SELECT DISTINCT ON (group_key) group_key, id
			FROM (
				SELECT
					g.group_key,
					mf.id, mf.content_id, mf.episode_id,
					%s AS resolution_rank,
					%s AS range_rank
				FROM served g
				JOIN media_files mf ON mf.id = g.file_id
				WHERE %s
			) candidates
			ORDER BY group_key, resolution_rank DESC, range_rank DESC, %s
		)
		SELECT winners.group_key, mf.file_path, coalesce(mf.resolution, ''), coalesce(mf.hdr, false),
			coalesce(mf.codec_video, ''), coalesce(mf.codec_audio, ''), coalesce(mf.audio_channels, 0),
			coalesce(mf.container, ''), coalesce(mf.edition_key, ''), mf.video_tracks, mf.audio_tracks,
			%s
		FROM winners
		JOIN media_files mf ON mf.id = winners.id
	`, overlaySummaryResolutionRankSQL, overlaySummaryRangeRankSQL, strings.Join(conditions, " AND "), tieOrder,
		overlaySummaryHasSubtitlesSQL)

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying overlay summaries: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var groupKey string
		var file overlays.StoredFile
		if err := rows.Scan(
			&groupKey, &file.FilePath, &file.Resolution, &file.HDR, &file.CodecVideo, &file.CodecAudio,
			&file.AudioChannels, &file.Container, &file.EditionKey, &file.VideoTracks, &file.AudioTracks, &file.HasSubtitles,
		); err != nil {
			return nil, fmt.Errorf("scanning overlay summary row: %w", err)
		}
		summary, err := overlays.BuildStoredSummary(&file)
		if err != nil {
			return nil, fmt.Errorf("building overlay summary: %w", err)
		}
		if summary != nil {
			summaries[groupKey] = summary
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating overlay summary rows: %w", err)
	}

	return summaries, nil
}
