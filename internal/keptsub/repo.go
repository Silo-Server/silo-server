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
	// PutTrack records t, replacing the file's row for the same stream, and
	// ends the stream's reservation.
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
	// FileKeptBytes sums the kept bytes of a file's streams other than except.
	FileKeptBytes(ctx context.Context, fileID, except int) (int64, error)
	// Touch records that the stream's kept track was just used.
	Touch(ctx context.Context, fileID, streamIndex int) error
	// Reserve claims room for a track before its object is stored, evicting
	// the least recently used rows when the store's total is full, and
	// returns the evicted rows' keys. It fails with ErrNoRoom, evicting
	// nothing, when the file's share or the store can't take the track.
	Reserve(ctx context.Context, res Reservation) ([]string, error)
	// Unreserve ends the stream's reservation without a row.
	Unreserve(ctx context.Context, fileID, streamIndex int) error
}

// ErrNoRoom means a track's file share or the store's total can't take it.
var ErrNoRoom = errors.New("keptsub: no room to keep the track")

// Reservation is room asked for one track's bytes.
type Reservation struct {
	MediaFileID int
	StreamIndex int
	Bytes       int64
	// FileMax bounds the file's tracks together, reservations included;
	// StoreMax every track and reservation together.
	FileMax  int64
	StoreMax int64
	// Lease is how long the reservation counts without its row landing.
	Lease time.Duration
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
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("record kept subtitle track: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The row goes first: its byte count locks the usage row, which a
	// reservation takes before its own row, so the two never wait on each
	// other in turn.
	_, err = tx.Exec(ctx, `
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
	if _, err := tx.Exec(ctx, `DELETE FROM public.embedded_subtitle_track_reservations
		WHERE media_file_id = $1 AND embedded_stream_index = $2`, t.MediaFileID, t.StreamIndex); err != nil {
		return fmt.Errorf("record kept subtitle track: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("record kept subtitle track: %w", err)
	}
	return nil
}

func (r *PgRepo) FileKeptBytes(ctx context.Context, fileID, except int) (int64, error) {
	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT COALESCE(SUM(byte_size), 0) FROM public.embedded_subtitle_tracks
		WHERE media_file_id = $1 AND embedded_stream_index <> $2`, fileID, except).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum kept subtitle tracks: %w", err)
	}
	return total, nil
}

// Touch moves updated_at, the row's recency, at most once a minute; the
// store throttles further.
func (r *PgRepo) Touch(ctx context.Context, fileID, streamIndex int) error {
	if _, err := r.pool.Exec(ctx, `UPDATE public.embedded_subtitle_tracks SET updated_at = now()
		WHERE media_file_id = $1 AND embedded_stream_index = $2 AND updated_at < now() - interval '1 minute'`,
		fileID, streamIndex); err != nil {
		return fmt.Errorf("touch kept subtitle track: %w", err)
	}
	return nil
}

// evictionBatch is how many least recently used rows Reserve looks at at a
// time.
const evictionBatch = 32

// Reserve checks the file's share under a per-file advisory lock, so two of
// a file's tracks can't both fit what only one does, then the store's total
// under the usage row's lock, which every keep takes in turn. Eviction skips
// locked rows and reserved streams, so it never waits on a writer or takes
// a track being kept.
func (r *PgRepo) Reserve(ctx context.Context, res Reservation) ([]string, error) {
	if res.Bytes > res.StoreMax {
		return nil, fmt.Errorf("%w: %d bytes over the store's %d", ErrNoRoom, res.Bytes, res.StoreMax)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("reserve kept subtitle track: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('embedded_subtitle_track_reservations'), $1)`, res.MediaFileID); err != nil {
		return nil, fmt.Errorf("reserve kept subtitle track: %w", err)
	}
	var fileBytes int64
	if err := tx.QueryRow(ctx, `SELECT
		COALESCE((SELECT SUM(byte_size) FROM public.embedded_subtitle_tracks
			WHERE media_file_id = $1 AND embedded_stream_index <> $2), 0)
		+ COALESCE((SELECT SUM(byte_size) FROM public.embedded_subtitle_track_reservations
			WHERE media_file_id = $1 AND embedded_stream_index <> $2 AND expires_at > now()), 0)`,
		res.MediaFileID, res.StreamIndex).Scan(&fileBytes); err != nil {
		return nil, fmt.Errorf("reserve kept subtitle track: %w", err)
	}
	if fileBytes+res.Bytes > res.FileMax {
		return nil, fmt.Errorf("%w: file %d keeps %d bytes, %d more would pass its %d", ErrNoRoom, res.MediaFileID, fileBytes, res.Bytes, res.FileMax)
	}
	// The stream's own row is replaced when its new one lands.
	var used int64
	if err := tx.QueryRow(ctx, `SELECT u.kept_bytes
		- COALESCE((SELECT byte_size FROM public.embedded_subtitle_tracks
			WHERE media_file_id = $1 AND embedded_stream_index = $2), 0)
		+ COALESCE((SELECT SUM(byte_size) FROM public.embedded_subtitle_track_reservations
			WHERE (media_file_id, embedded_stream_index) <> ($1, $2) AND expires_at > now()), 0)
		FROM public.embedded_subtitle_track_usage u FOR UPDATE OF u`, res.MediaFileID, res.StreamIndex).Scan(&used); err != nil {
		return nil, fmt.Errorf("reserve kept subtitle track: %w", err)
	}
	var evicted []string
	if need := used + res.Bytes - res.StoreMax; need > 0 {
		if evicted, err = evictLeastRecentlyUsed(ctx, tx, need, res.MediaFileID, res.StreamIndex); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM public.embedded_subtitle_track_reservations WHERE expires_at <= now()`); err != nil {
		return nil, fmt.Errorf("reserve kept subtitle track: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public.embedded_subtitle_track_reservations (media_file_id, embedded_stream_index, byte_size, expires_at)
		VALUES ($1, $2, $3, now() + $4 * interval '1 millisecond')
		ON CONFLICT (media_file_id, embedded_stream_index) DO UPDATE
		SET byte_size = EXCLUDED.byte_size, expires_at = EXCLUDED.expires_at`,
		res.MediaFileID, res.StreamIndex, res.Bytes, res.Lease.Milliseconds()); err != nil {
		return nil, fmt.Errorf("reserve kept subtitle track: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("reserve kept subtitle track: %w", err)
	}
	return evicted, nil
}

// evictLeastRecentlyUsed deletes rows other than the reserving stream's,
// from the least recently used, until they free need bytes, and returns
// their keys; the delete trigger queues them too. It fails with ErrNoRoom
// when every evictable row frees less, and the caller's rollback keeps them.
func evictLeastRecentlyUsed(ctx context.Context, tx pgx.Tx, need int64, fileID, streamIndex int) ([]string, error) {
	var keys []string
	for freed := int64(0); freed < need; {
		rows, err := tx.Query(ctx, `SELECT t.media_file_id, t.embedded_stream_index, t.byte_size
			FROM public.embedded_subtitle_tracks t
			WHERE (t.media_file_id, t.embedded_stream_index) <> ($2, $3)
				AND NOT EXISTS (SELECT 1 FROM public.embedded_subtitle_track_reservations v
				WHERE v.media_file_id = t.media_file_id AND v.embedded_stream_index = t.embedded_stream_index
					AND v.expires_at > now())
			ORDER BY t.updated_at, t.media_file_id, t.embedded_stream_index
			LIMIT $1 FOR UPDATE OF t SKIP LOCKED`, evictionBatch, fileID, streamIndex)
		if err != nil {
			return nil, fmt.Errorf("evict kept subtitle tracks: %w", err)
		}
		var files, streams []int32
		for rows.Next() && freed < need {
			var file, stream int32
			var size int64
			if err := rows.Scan(&file, &stream, &size); err != nil {
				rows.Close()
				return nil, fmt.Errorf("evict kept subtitle tracks: %w", err)
			}
			files, streams, freed = append(files, file), append(streams, stream), freed+size
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("evict kept subtitle tracks: %w", err)
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("%w: the store is full of tracks in use", ErrNoRoom)
		}
		deleted, err := tx.Query(ctx, `DELETE FROM public.embedded_subtitle_tracks t
			USING unnest($1::int[], $2::int[]) AS v(media_file_id, embedded_stream_index)
			WHERE t.media_file_id = v.media_file_id AND t.embedded_stream_index = v.embedded_stream_index
			RETURNING t.blob_key`, files, streams)
		if err != nil {
			return nil, fmt.Errorf("evict kept subtitle tracks: %w", err)
		}
		batch, err := pgx.CollectRows(deleted, pgx.RowTo[string])
		if err != nil {
			return nil, fmt.Errorf("evict kept subtitle tracks: %w", err)
		}
		keys = append(keys, batch...)
	}
	return keys, nil
}

func (r *PgRepo) Unreserve(ctx context.Context, fileID, streamIndex int) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM public.embedded_subtitle_track_reservations
		WHERE media_file_id = $1 AND embedded_stream_index = $2`, fileID, streamIndex); err != nil {
		return fmt.Errorf("end kept subtitle track reservation: %w", err)
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
