package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/overlays"
)

// overlaySummaryResolutionRankSQL mirrors overlays.resolutionRank: "4k"/"uhd"
// rank as 2160p, signed digits followed by "p" rank as their value, otherwise 0.
// Whitespace is trimmed with catalog.SQLTrimSpaceChars so the set matches Go's
// strings.TrimSpace exactly, Unicode spaces included.
// Limit numeric matches to 18 digits so they fit in int64, as required by Atoi.
const overlaySummaryResolutionRankSQL = `CASE
				WHEN lower(btrim(coalesce(mf.resolution, ''), ` + catalog.SQLTrimSpaceChars + `)) IN ('4k', 'uhd') THEN 2160
				WHEN lower(btrim(coalesce(mf.resolution, ''), ` + catalog.SQLTrimSpaceChars + `)) ~ '^[+-]?[0-9]{1,18}p$'
					THEN substring(lower(btrim(mf.resolution, ` + catalog.SQLTrimSpaceChars + `)) from '^([+-]?[0-9]{1,18})p$')::numeric
				ELSE 0
			END`

// overlaySummaryRangeRankSQL mirrors overlays.rangeRank. Dolby Vision reads
// video_range_type verbatim; HDR detection trims it. JSON null is not a DV tag.
const overlaySummaryRangeRankSQL = `CASE
				WHEN jsonb_path_exists(coalesce(mf.video_tracks, '[]'::jsonb),
					'$[*] ? ((@.dolby_vision.type() == "string" && @.dolby_vision != "") || @.dv_profile > 0 || @.video_range_type like_regex "^DOVI")') THEN 3
				WHEN jsonb_path_exists(coalesce(mf.video_tracks, '[]'::jsonb),
					'$[*] ? (@.hdr10_plus == true || @.video_range_type like_regex "HDR10Plus" || @.video_range_type like_regex "^[[:space:]]*HDR10[[:space:]]*$" || @.video_range_type like_regex "WithHDR10[[:space:]]*$" || @.video_range_type like_regex "^[[:space:]]*HLG[[:space:]]*$" || @.video_range_type like_regex "WithHLG[[:space:]]*$" || @.color_transfer like_regex "smpte2084" flag "i" || @.color_transfer like_regex "arib-std-b67" flag "i")') THEN 2
				WHEN coalesce(mf.hdr, false) THEN 1
				ELSE 0
			END`

// ListOverlaySummaries returns badges from each card's best accessible file.
// A file can serve both its series and episode cards, so page composition does
// not change the winner. Access filtering and ranking happen in PostgreSQL;
// only the winners' wide track metadata is sent to Go for BuildSummary.
// Each call reads committed files without a result cache, including changes
// made by scanners or API servers in other processes.
func (f *Fetcher) ListOverlaySummaries(ctx context.Context, contentIDs []string, filter catalog.AccessFilter) (map[string]*models.OverlaySummary, error) {
	summaries := make(map[string]*models.OverlaySummary, len(contentIDs))
	if len(contentIDs) == 0 {
		return summaries, nil
	}

	args := []any{contentIDs}
	conditions := []string{
		"(mf.content_id = ANY($1) OR mf.episode_id = ANY($1))",
		"mf.missing_since IS NULL",
		"g.group_key = ANY($1)",
	}
	accessConditions, args := catalog.MediaFileAccessSQL("mf", filter, args)
	conditions = append(conditions, accessConditions...)

	// Filter through the existing content/episode indexes before expanding the
	// requested groups. Sort narrow candidates, then fetch each winner's JSON.
	query := fmt.Sprintf(`
		WITH winners AS (
			SELECT DISTINCT ON (group_key) group_key, id
			FROM (
				SELECT
					g.group_key,
					mf.id, mf.content_id, mf.episode_id,
					%s AS resolution_rank,
					%s AS range_rank
				FROM media_files mf
				CROSS JOIN LATERAL (VALUES (mf.content_id), (mf.episode_id)) AS g(group_key)
				WHERE %s
			) candidates
			-- The final tiebreak repeats the legacy row order (content_id,
			-- episode_id, id) rather than id alone, because overlays.BuildSummary keeps
			-- the earliest file in the slice and that slice arrived in this order.
			ORDER BY group_key, resolution_rank DESC, range_rank DESC, content_id ASC, episode_id ASC, id ASC
		)
		SELECT winners.group_key, mf.content_id, mf.episode_id, mf.file_path, mf.resolution, mf.codec_audio,
			mf.audio_tracks, mf.hdr, mf.video_tracks, mf.codec_video, mf.audio_channels, mf.container,
			mf.subtitle_tracks, mf.external_subtitles, mf.edition_key
		FROM winners
		JOIN media_files mf ON mf.id = winners.id
	`, overlaySummaryResolutionRankSQL, overlaySummaryRangeRankSQL, strings.Join(conditions, " AND "))

	rows, err := f.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying overlay summaries: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var groupKey string
		var contentID string
		var episodeID *string
		var filePath string
		var resolution *string
		var codecAudio *string
		var audioTracksJSON []byte
		var hdr bool
		var videoTracksJSON []byte
		var codecVideo *string
		var audioChannels *int
		var container *string
		var subtitleTracksJSON []byte
		var externalSubtitlesJSON []byte
		var editionKey *string

		if err := rows.Scan(
			&groupKey, &contentID, &episodeID, &filePath, &resolution, &codecAudio, &audioTracksJSON, &hdr,
			&videoTracksJSON, &codecVideo, &audioChannels, &container, &subtitleTracksJSON, &externalSubtitlesJSON, &editionKey,
		); err != nil {
			return nil, fmt.Errorf("scanning overlay summary row: %w", err)
		}

		file := &models.MediaFile{
			ContentID: contentID,
			FilePath:  filePath,
			HDR:       hdr,
		}
		if episodeID != nil {
			file.EpisodeID = *episodeID
		}
		if resolution != nil {
			file.Resolution = *resolution
		}
		if codecAudio != nil {
			file.CodecAudio = *codecAudio
		}
		if codecVideo != nil {
			file.CodecVideo = *codecVideo
		}
		if audioChannels != nil {
			file.AudioChannels = *audioChannels
		}
		if container != nil {
			file.Container = *container
		}
		if editionKey != nil {
			file.EditionKey = *editionKey
		}
		if len(audioTracksJSON) > 0 {
			if err := json.Unmarshal(audioTracksJSON, &file.AudioTracks); err != nil {
				return nil, fmt.Errorf("unmarshaling overlay audio tracks: %w", err)
			}
		}
		if len(videoTracksJSON) > 0 {
			if err := json.Unmarshal(videoTracksJSON, &file.VideoTracks); err != nil {
				return nil, fmt.Errorf("unmarshaling overlay video tracks: %w", err)
			}
		}
		if len(subtitleTracksJSON) > 0 {
			if err := json.Unmarshal(subtitleTracksJSON, &file.SubtitleTracks); err != nil {
				return nil, fmt.Errorf("unmarshaling overlay subtitle tracks: %w", err)
			}
		}
		if len(externalSubtitlesJSON) > 0 {
			if err := json.Unmarshal(externalSubtitlesJSON, &file.ExternalSubtitles); err != nil {
				return nil, fmt.Errorf("unmarshaling overlay external subtitles: %w", err)
			}
		}

		if summary := overlays.BuildSummary([]*models.MediaFile{file}); summary != nil {
			summaries[groupKey] = summary
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating overlay summary rows: %w", err)
	}

	return summaries, nil
}
