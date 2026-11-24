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
// signed values and leading zeroes accepted by strconv.Atoi. The common probe
// values are matched as stored first, so most files skip the trim, lower-case
// and regex work of the general case; each maps to the rank the general case
// gives it.
const overlaySummaryNormalizedResolutionSQL = `lower(btrim(coalesce(mf.resolution, ''), ` + SQLTrimSpaceChars + `))`
const overlaySummaryResolutionNumberSQL = `substring(` + overlaySummaryNormalizedResolutionSQL + ` from '^([+-]?0*[0-9]{1,19})p$')::numeric`
const overlaySummaryResolutionRankSQL = `CASE mf.resolution
				WHEN '1080p' THEN 1080 WHEN '720p' THEN 720 WHEN '2160p' THEN 2160 WHEN '480p' THEN 480
				ELSE CASE ` + overlaySummaryNormalizedResolutionSQL + `
				WHEN '480p' THEN 480 WHEN '720p' THEN 720 WHEN '1080p' THEN 1080
				WHEN '2160p' THEN 2160 WHEN '4320p' THEN 4320 WHEN '4k' THEN 2160 WHEN 'uhd' THEN 2160
				ELSE CASE WHEN ` + overlaySummaryNormalizedResolutionSQL + ` ~ '^[+-]?0*[0-9]{1,19}p$'
					THEN CASE WHEN ` + overlaySummaryResolutionNumberSQL + ` BETWEEN -9223372036854775808 AND 9223372036854775807
						THEN ` + overlaySummaryResolutionNumberSQL + ` ELSE 0 END
					ELSE 0 END
				END
			END`

// The JSON path regex uses exactly the Go TrimSpace rune set documented by
// SQLTrimSpaceChars. PostgreSQL's locale-dependent [:space:] omits characters
// such as NBSP, changing the winner for equal-resolution HDR files.
const overlaySummarySpaceRegex = `[\u0009-\u000d \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]`

// overlaySummaryRangeGateSQL holds for every track that could rank as Dolby
// Vision or HDR in overlaySummaryRangeRankSQL, without running a regex. Each
// test there implies one here: a Dolby Vision string implies the key exists,
// a DOVI, HDR10 or HLG video_range_type is not "SDR", and an smpte2084 or
// arib-std-b67 color_transfer is none of the SDR transfers listed. Most files
// are SDR and fail it, so they skip the like_regex tests, which cost more
// than reading the rest of the row.
const overlaySummaryRangeGateSQL = `jsonb_path_exists(coalesce(mf.video_tracks, '[]'::jsonb),
					'$[*] ? (@.video_range_type != "SDR" || @.hdr10_plus == true || exists(@.dolby_vision) || @.dv_profile > 0 || (@.color_transfer != "bt709" && @.color_transfer != "smpte170m" && @.color_transfer != "bt470bg" && @.color_transfer != "unknown" && @.color_transfer != ""))')`

// overlaySummaryRangeRankSQL mirrors overlays.rangeRank. Dolby Vision reads
// video_range_type verbatim; HDR detection trims it. JSON null is not a DV tag.
// A file that fails overlaySummaryRangeGateSQL ranks by its hdr flag alone.
const overlaySummaryRangeRankSQL = `CASE WHEN ` + overlaySummaryRangeGateSQL + ` THEN CASE
				WHEN jsonb_path_exists(coalesce(mf.video_tracks, '[]'::jsonb),
					'$[*] ? ((@.dolby_vision.type() == "string" && @.dolby_vision != "") || @.dv_profile > 0 || @.video_range_type like_regex "^DOVI")') THEN 3
				WHEN jsonb_path_exists(coalesce(mf.video_tracks, '[]'::jsonb),
					'$[*] ? (@.hdr10_plus == true || @.video_range_type like_regex "HDR10Plus" || @.video_range_type like_regex "^` + overlaySummarySpaceRegex + `*HDR10` + overlaySummarySpaceRegex + `*$" || @.video_range_type like_regex "WithHDR10` + overlaySummarySpaceRegex + `*$" || @.video_range_type like_regex "^` + overlaySummarySpaceRegex + `*HLG` + overlaySummarySpaceRegex + `*$" || @.video_range_type like_regex "WithHLG` + overlaySummarySpaceRegex + `*$" || @.color_transfer like_regex "smpte2084" flag "i" || @.color_transfer like_regex "arib-std-b67" flag "i")') THEN 2
				WHEN coalesce(mf.hdr, false) THEN 1
				ELSE 0
			END
			WHEN coalesce(mf.hdr, false) THEN 1
			ELSE 0
		END`

// overlaySummaryHasSubtitlesSQL is whether a file has any embedded or external
// subtitle track, the only fact overlays takes from those columns. Deciding it
// in SQL keeps the largest JSON columns out of every card's row.
const overlaySummaryHasSubtitlesSQL = `(
			CASE jsonb_typeof(mf.subtitle_tracks) WHEN 'array' THEN jsonb_array_length(mf.subtitle_tracks) > 0 ELSE false END
			OR CASE jsonb_typeof(mf.external_subtitles) WHEN 'array' THEN jsonb_array_length(mf.external_subtitles) > 0 ELSE false END)`

// Equal-ranked files tie on these columns, ascending. The file ID comes last
// so equal-ranked files always resolve to the same winner. Sections keep their
// content/episode/file order; catalog lists use the file ID, matching their
// scanner projections.
var (
	overlaySectionTieColumns = []string{cursorContentIDColumn, "episode_id", "id"}
	overlayCatalogTieColumns = []string{"id"}
)

// ListOverlaySummaries reduces accessible files to one badge winner per card.
func ListOverlaySummaries(ctx context.Context, pool *pgxpool.Pool, contentIDs []string, filter AccessFilter) (map[string]*models.OverlaySummary, error) {
	return listOverlaySummaries(ctx, pool, contentIDs, filter, overlaySectionTieColumns)
}

// ListOverlaySummaries reads only each displayed item's winning file metadata.
// A repository without a pool returns an error so callers use their file
// projection rather than treating every card as having no badges.
func (r *ItemRepository) ListOverlaySummaries(ctx context.Context, contentIDs []string, filter AccessFilter) (map[string]*models.OverlaySummary, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("listing overlay summaries: item repository has no database pool")
	}
	return listOverlaySummaries(ctx, r.pool, contentIDs, filter, overlayCatalogTieColumns)
}

func listOverlaySummaries(ctx context.Context, pool *pgxpool.Pool, contentIDs []string, filter AccessFilter, tieColumns []string) (map[string]*models.OverlaySummary, error) {
	summaries := make(map[string]*models.OverlaySummary, len(contentIDs))
	if pool == nil || len(contentIDs) == 0 {
		return summaries, nil
	}

	args := []any{contentIDs}
	conditions := []string{playableFileExists}
	accessConditions, args := MediaFileAccessSQL("mf", filter, args)
	conditions = append(conditions, accessConditions...)
	where := strings.Join(conditions, " AND ")
	ranked := `mf.id, mf.content_id, mf.episode_id,
				` + overlaySummaryResolutionRankSQL + ` AS resolution_rank,
				` + overlaySummaryRangeRankSQL + ` AS range_rank`

	// Each branch reads a requested card's files through the content or
	// episode index and ranks them in the same pass, so a file's row is read
	// once and the scan can run in parallel. A multi-episode file also badges
	// each later episode of its range. Each card keeps its first file by
	// resolution, then range, then the caller's tie columns.
	query := fmt.Sprintf(`
		WITH winners AS (
			SELECT DISTINCT ON (group_key) group_key, id
			FROM (
				SELECT mf.content_id AS group_key, %[2]s
				FROM media_files mf
				WHERE mf.content_id = ANY($1) AND %[3]s
				UNION ALL
				SELECT mf.episode_id AS group_key, %[2]s
				FROM media_files mf
				WHERE mf.episode_id = ANY($1) AND %[3]s
				UNION ALL
				SELECT covered.content_id AS group_key, %[2]s
				FROM episodes covered`+spannedEpisodeJoinSQL+`
				WHERE covered.content_id = ANY($1) AND %[3]s
			) candidates
			ORDER BY group_key, resolution_rank DESC, range_rank DESC, %[1]s
		)
		SELECT winners.group_key, mf.file_path, coalesce(mf.resolution, ''), coalesce(mf.hdr, false),
			coalesce(mf.codec_video, ''), coalesce(mf.codec_audio, ''), coalesce(mf.audio_channels, 0),
			coalesce(mf.container, ''), coalesce(mf.edition_key, ''), mf.video_tracks, mf.audio_tracks,
			%[4]s
		FROM winners
		JOIN media_files mf ON mf.id = winners.id
	`, strings.Join(tieColumns, ", "), ranked, where, overlaySummaryHasSubtitlesSQL)

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
