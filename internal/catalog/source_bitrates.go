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
// of the bitrates of the present video files the filter admits, or 0 when
// there are none. Audio-only files and cover-art streams are not video.
func (s *SourceBitrates) Percentile(ctx context.Context, filter AccessFilter, fraction float64) (int, error) {
	if s == nil || s.Pool == nil {
		return 0, nil
	}
	args := []any{fraction}
	conditions, args := MediaFileAccessSQL("mf", filter, args)
	conditions = append(conditions,
		"mf.bitrate > 0",
		"mf.missing_since IS NULL",
		"COALESCE(mf.codec_video, '') NOT IN ('', 'mjpeg', 'png')",
	)
	var kbps int
	err := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(percentile_disc($1) WITHIN GROUP (ORDER BY mf.bitrate), 0)
		FROM media_files mf
		WHERE `+strings.Join(conditions, " AND "), args...).Scan(&kbps)
	return kbps, err
}
