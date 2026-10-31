package keptsub

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Track is one kept track: a row of embedded_subtitle_tracks.
type Track struct {
	MediaFileID      int
	StreamIndex      int
	ContainerTrackID string
	Codec            string
	// SourceSize and SourceModTime (Unix nanoseconds) are the source
	// version the track was read from.
	SourceSize    int64
	SourceModTime int64
	BlobKey       string
	// StreamFormat is the FFmpeg input format of the kept stream, and
	// StartTime what a conversion subtracts from its timestamps.
	StreamFormat string
	StartTime    time.Duration
	SHA256       string
	Bytes        int64
	Packets      int
	Completeness string
	Method       string
	// HasOverrideTags is nil when the track's cues weren't looked at for
	// tags (ASS and bitmap tracks).
	HasOverrideTags *bool
}

const (
	MethodCues   = "cues"
	MethodFFmpeg = "ffmpeg"

	CompletenessComplete = "complete"
	CompletenessUnknown  = "unknown"
)

// Repo stores kept tracks and the claims servers hold while reading one.
type Repo interface {
	// Track returns the kept track, or nil when there is none.
	Track(ctx context.Context, fileID, streamIndex int) (*Track, error)
	// FileTracks returns every kept track of a file.
	FileTracks(ctx context.Context, fileID int) ([]Track, error)
	// PutTrack records t, replacing the file's row for the same stream.
	PutTrack(ctx context.Context, t Track) error
	// DeleteTrack removes the stream's row while it still names blobKey.
	DeleteTrack(ctx context.Context, fileID, streamIndex int, blobKey string) error
	// Claim takes the stream's read claim for lease, unless another
	// unexpired claim holds it.
	Claim(ctx context.Context, fileID, streamIndex int, token string, lease time.Duration) (bool, error)
	// Release gives up a claim token holds.
	Release(ctx context.Context, fileID, streamIndex int, token string) error
	// QueueCollection queues an object key for the blob collector, which
	// deletes it after the delay unless a row names it.
	QueueCollection(ctx context.Context, key string, after time.Duration) error
}

// PgRepo is Repo on PostgreSQL.
type PgRepo struct {
	pool *pgxpool.Pool
}

// NewPgRepo returns a Repo on pool.
func NewPgRepo(pool *pgxpool.Pool) *PgRepo { return &PgRepo{pool: pool} }

const trackColumns = `media_file_id, embedded_stream_index, container_track_id, codec, source_size,
	source_mtime_ns, blob_key, stream_format, start_time_us, content_sha256, byte_size, packet_count,
	completeness, method, has_override_tags`

func scanTrack(row pgx.Row) (*Track, error) {
	var t Track
	var startUS int64
	if err := row.Scan(&t.MediaFileID, &t.StreamIndex, &t.ContainerTrackID, &t.Codec, &t.SourceSize,
		&t.SourceModTime, &t.BlobKey, &t.StreamFormat, &startUS, &t.SHA256, &t.Bytes, &t.Packets,
		&t.Completeness, &t.Method, &t.HasOverrideTags); err != nil {
		return nil, err
	}
	t.StartTime = time.Duration(startUS) * time.Microsecond
	return &t, nil
}

func (r *PgRepo) Track(ctx context.Context, fileID, streamIndex int) (*Track, error) {
	t, err := scanTrack(r.pool.QueryRow(ctx, `SELECT `+trackColumns+` FROM public.embedded_subtitle_tracks
		WHERE media_file_id = $1 AND embedded_stream_index = $2`, fileID, streamIndex))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get kept subtitle track: %w", err)
	}
	return t, nil
}

func (r *PgRepo) FileTracks(ctx context.Context, fileID int) ([]Track, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+trackColumns+` FROM public.embedded_subtitle_tracks
		WHERE media_file_id = $1 ORDER BY embedded_stream_index`, fileID)
	if err != nil {
		return nil, fmt.Errorf("list kept subtitle tracks: %w", err)
	}
	defer rows.Close()
	var tracks []Track
	for rows.Next() {
		t, err := scanTrack(rows)
		if err != nil {
			return nil, fmt.Errorf("scan kept subtitle track: %w", err)
		}
		tracks = append(tracks, *t)
	}
	return tracks, rows.Err()
}

func (r *PgRepo) PutTrack(ctx context.Context, t Track) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO public.embedded_subtitle_tracks (`+trackColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (media_file_id, embedded_stream_index) DO UPDATE SET
			container_track_id = EXCLUDED.container_track_id, codec = EXCLUDED.codec,
			source_size = EXCLUDED.source_size, source_mtime_ns = EXCLUDED.source_mtime_ns,
			blob_key = EXCLUDED.blob_key, stream_format = EXCLUDED.stream_format,
			start_time_us = EXCLUDED.start_time_us, content_sha256 = EXCLUDED.content_sha256,
			byte_size = EXCLUDED.byte_size, packet_count = EXCLUDED.packet_count,
			completeness = EXCLUDED.completeness, method = EXCLUDED.method,
			has_override_tags = EXCLUDED.has_override_tags, updated_at = now()`,
		t.MediaFileID, t.StreamIndex, t.ContainerTrackID, t.Codec, t.SourceSize, t.SourceModTime,
		t.BlobKey, t.StreamFormat, t.StartTime.Microseconds(), t.SHA256, t.Bytes, t.Packets,
		t.Completeness, t.Method, t.HasOverrideTags)
	if err != nil {
		return fmt.Errorf("record kept subtitle track: %w", err)
	}
	return nil
}

func (r *PgRepo) DeleteTrack(ctx context.Context, fileID, streamIndex int, blobKey string) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM public.embedded_subtitle_tracks
		WHERE media_file_id = $1 AND embedded_stream_index = $2 AND blob_key = $3`, fileID, streamIndex, blobKey); err != nil {
		return fmt.Errorf("delete kept subtitle track: %w", err)
	}
	return nil
}

func (r *PgRepo) Claim(ctx context.Context, fileID, streamIndex int, token string, lease time.Duration) (bool, error) {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO public.embedded_subtitle_track_claims (media_file_id, embedded_stream_index, claim_token, lease_expires_at)
		VALUES ($1, $2, $3, now() + $4 * interval '1 millisecond')
		ON CONFLICT (media_file_id, embedded_stream_index) DO UPDATE
		SET claim_token = EXCLUDED.claim_token, lease_expires_at = EXCLUDED.lease_expires_at
		WHERE public.embedded_subtitle_track_claims.lease_expires_at <= now()
		RETURNING claim_token`, fileID, streamIndex, token, lease.Milliseconds()).Scan(new(string))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim subtitle track read: %w", err)
	}
	return true, nil
}

func (r *PgRepo) Release(ctx context.Context, fileID, streamIndex int, token string) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM public.embedded_subtitle_track_claims
		WHERE media_file_id = $1 AND embedded_stream_index = $2 AND claim_token = $3`, fileID, streamIndex, token); err != nil {
		return fmt.Errorf("release subtitle track claim: %w", err)
	}
	return nil
}

func (r *PgRepo) QueueCollection(ctx context.Context, key string, after time.Duration) error {
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO public.blob_gc_queue (prefix, not_before)
		VALUES ($1, now() + $2 * interval '1 millisecond')
		ON CONFLICT (prefix) DO UPDATE
		SET not_before = GREATEST(public.blob_gc_queue.not_before, EXCLUDED.not_before)`, key, after.Milliseconds()); err != nil {
		return fmt.Errorf("queue kept subtitle stream for collection: %w", err)
	}
	return nil
}
