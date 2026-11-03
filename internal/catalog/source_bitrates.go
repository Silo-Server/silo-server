package catalog

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SourceBitrates reads the bitrates of the video files a viewer can play, for
// the connection speed test's target.
type SourceBitrates struct{ Pool *pgxpool.Pool }

// Percentile returns the fraction-th percentile (for example 0.999), in kbps,
// of the planned bitrates of the present video files the filter admits, or 0
// when there are none. Audio-only files and cover-art streams are not video.
//
// A file counts only when playback would authorize it: the file passes
// MediaFileAccessSQL, and its item passes the predicates EnsureAccessible
// applies (item library membership, maturity limits, excluded media types).
func (s *SourceBitrates) Percentile(ctx context.Context, filter AccessFilter, fraction float64) (int, error) {
	if s == nil || s.Pool == nil {
		return 0, nil
	}
	args := []any{fraction}
	conditions, args := MediaFileAccessSQL("mf", filter, args)
	argIdx := len(args) + 1
	appendLibraryAccessConditions("mi.content_id", filter, &conditions, &args, &argIdx)
	applyAccessFilter("mi", AccessFilter{MaturityLimits: filter.MaturityLimits, ExcludedMediaTypes: filter.ExcludedMediaTypes}, &conditions, &args, &argIdx)
	conditions = append(conditions,
		"planned.kbps > 0",
		"mf.missing_since IS NULL",
		"COALESCE(mf.codec_video, '') NOT IN ('', 'mjpeg', 'png')",
	)
	var kbps int
	// A file counts at the bitrate playback plans it at
	// (playback.SourceDescriptorFromFileV3): its first video track's when
	// positive, else the file's, with a legacy bps figure over 10,000,000 read
	// as kbps.
	err := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(percentile_disc($1) WITHIN GROUP (ORDER BY planned.kbps), 0)
		FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id
		CROSS JOIN LATERAL (SELECT CASE
			WHEN jsonb_typeof(mf.video_tracks->0->'bitrate') <> 'number' THEN mf.bitrate
			WHEN (mf.video_tracks->0->>'bitrate')::numeric > 0 THEN (mf.video_tracks->0->>'bitrate')::numeric
			ELSE mf.bitrate END AS kbps) recorded
		CROSS JOIN LATERAL (SELECT trunc(CASE WHEN recorded.kbps > 10000000 THEN recorded.kbps / 1000 ELSE recorded.kbps END)::bigint AS kbps) planned
		WHERE `+strings.Join(conditions, " AND "), args...).Scan(&kbps)
	return kbps, err
}
