package subtitles

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const externalTimingColumns = `id, media_file_id, content_sha256, path, format,
	timing_offset_ms, timing_scale, revision`

func scanExternalTiming(row pgx.Row) (*ExternalTiming, error) {
	var t ExternalTiming
	if err := row.Scan(&t.ID, &t.MediaFileID, &t.ContentSHA256, &t.Path, &t.Format,
		&t.Timing.OffsetMS, &t.Timing.Scale, &t.Revision); err != nil {
		return nil, err
	}
	return &t, nil
}

func externalTimingOrNil(t *ExternalTiming, err error, action string) (*ExternalTiming, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s sidecar subtitle timing: %w", action, err)
	}
	return t, nil
}

// ExternalTiming returns the correction stored for a sidecar's bytes, or nil.
func (r *PgRepository) ExternalTiming(ctx context.Context, mediaFileID int, contentSHA256 string) (*ExternalTiming, error) {
	t, err := scanExternalTiming(r.pool.QueryRow(ctx, `SELECT `+externalTimingColumns+`
		FROM external_subtitle_timings WHERE media_file_id = $1 AND content_sha256 = $2`, mediaFileID, contentSHA256))
	return externalTimingOrNil(t, err, "get")
}

// ExternalTimingByID returns a sidecar correction by its row ID, or nil.
func (r *PgRepository) ExternalTimingByID(ctx context.Context, id int64) (*ExternalTiming, error) {
	t, err := scanExternalTiming(r.pool.QueryRow(ctx, `SELECT `+externalTimingColumns+`
		FROM external_subtitle_timings WHERE id = $1`, id))
	return externalTimingOrNil(t, err, "get")
}

// ExternalTimings returns a file's sidecar corrections keyed by content hash.
func (r *PgRepository) ExternalTimings(ctx context.Context, mediaFileID int) (map[string]*ExternalTiming, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+externalTimingColumns+`
		FROM external_subtitle_timings WHERE media_file_id = $1`, mediaFileID)
	if err != nil {
		return nil, fmt.Errorf("list sidecar subtitle timings: %w", err)
	}
	defer rows.Close()
	out := make(map[string]*ExternalTiming)
	for rows.Next() {
		t, err := scanExternalTiming(rows)
		if err != nil {
			return nil, fmt.Errorf("scan sidecar subtitle timing: %w", err)
		}
		out[t.ContentSHA256] = t
	}
	return out, rows.Err()
}

// EnsureExternalTiming returns the correction row for a sidecar's bytes,
// creating one with the original timing, and records path as where those
// bytes were last seen. Recording a path leaves the revision unchanged.
func (r *PgRepository) EnsureExternalTiming(ctx context.Context, mediaFileID int, contentSHA256, path string, format SubtitleFormat) (*ExternalTiming, error) {
	t, err := scanExternalTiming(r.pool.QueryRow(ctx, `
		INSERT INTO external_subtitle_timings (media_file_id, content_sha256, path, format)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (media_file_id, content_sha256) DO UPDATE SET path = EXCLUDED.path, format = EXCLUDED.format
		RETURNING `+externalTimingColumns, mediaFileID, contentSHA256, path, format))
	if err != nil {
		return nil, fmt.Errorf("ensure sidecar subtitle timing: %w", err)
	}
	return t, nil
}

// SetExternalTiming stores timing for a sidecar's bytes while their
// correction is still at revision, where 0 means none is stored yet.
// Otherwise nothing changes and the error is ErrExternalTimingChanged.
func (r *PgRepository) SetExternalTiming(ctx context.Context, mediaFileID int, contentSHA256, path string, format SubtitleFormat, timing Timing, revision int64) (*ExternalTiming, error) {
	timing = timing.Normalized()
	var row pgx.Row
	if revision == 0 {
		row = r.pool.QueryRow(ctx, `
			INSERT INTO external_subtitle_timings (media_file_id, content_sha256, path, format, timing_offset_ms, timing_scale)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (media_file_id, content_sha256) DO NOTHING
			RETURNING `+externalTimingColumns, mediaFileID, contentSHA256, path, format, timing.OffsetMS, timing.Scale)
	} else {
		row = r.pool.QueryRow(ctx, `
			UPDATE external_subtitle_timings
			SET timing_offset_ms = $4, timing_scale = $5, path = $6
			WHERE media_file_id = $1 AND content_sha256 = $2 AND revision = $3
			RETURNING `+externalTimingColumns, mediaFileID, contentSHA256, revision, timing.OffsetMS, timing.Scale, path)
	}
	t, err := scanExternalTiming(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrExternalTimingChanged
	}
	if err != nil {
		return nil, fmt.Errorf("set sidecar subtitle timing: %w", err)
	}
	return t, nil
}
